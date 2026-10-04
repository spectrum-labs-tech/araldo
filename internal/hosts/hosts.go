// SPDX-License-Identifier: AGPL-3.0-or-later

// Package hosts keeps the Araldo servers the CLI is signed in to, as gh
// keeps GitHub hosts (ADR 0028): hosts.yaml in the config directory names
// each server and the account on it, and each credential lives in the OS
// keychain, or in hosts.yaml (mode 0600) where there is no keychain.
// ARALDO_TOKEN and ARALDO_HOST override both, for CI.
package hosts

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zalando/go-keyring"
	"gopkg.in/yaml.v3"
)

// ErrNotSignedIn means no credential was found for the server.
var ErrNotSignedIn = errors.New("not signed in")

// Host is one server and the account signed in to it.
type Host struct {
	// URL is the server, e.g. https://araldo.example.com.
	URL string `yaml:"url"`
	// User describes the account, as the server reported it at sign-in.
	User string `yaml:"user,omitempty"`
	// Token is set only when there was no keychain to keep it in.
	Token string `yaml:"token,omitempty"`
}

// File is hosts.yaml.
type File struct {
	// Default is the server commands use without --hostname.
	Default string           `yaml:"default,omitempty"`
	Hosts   map[string]*Host `yaml:"hosts,omitempty"`
}

// Store reads and writes hosts.yaml and the keychain.
type Store struct {
	// Dir is the config directory.
	Dir string
	// Keyring is the OS keychain; tests replace it.
	Keyring Keyring
}

// Keyring is the part of the OS keychain the store uses.
type Keyring interface {
	Get(service, user string) (string, error)
	Set(service, user, secret string) error
	Delete(service, user string) error
}

// OSKeyring is the system keychain (macOS Keychain, Windows Credential
// Manager, the Secret Service on Linux).
type OSKeyring struct{}

func (OSKeyring) Get(service, user string) (string, error) { return keyring.Get(service, user) }
func (OSKeyring) Set(service, user, secret string) error   { return keyring.Set(service, user, secret) }
func (OSKeyring) Delete(service, user string) error        { return keyring.Delete(service, user) }

// Default returns the store in ARALDO_CONFIG_DIR, or the platform's config
// directory.
func Default() (*Store, error) {
	dir := os.Getenv("ARALDO_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(base, "araldo")
	}
	return &Store{Dir: dir, Keyring: OSKeyring{}}, nil
}

func (s *Store) path() string { return filepath.Join(s.Dir, "hosts.yaml") }

// Load reads hosts.yaml; a missing file is an empty one.
func (s *Store) Load() (*File, error) {
	f := &File{Hosts: map[string]*Host{}}
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, f); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path(), err)
	}
	if f.Hosts == nil {
		f.Hosts = map[string]*Host{}
	}
	return f, nil
}

// Save writes hosts.yaml, readable only by the user.
func (s *Store) Save(f *File) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(), b, 0o600)
}

// Name turns what the user typed (a URL or a bare host name) into the
// host's name and URL. A bare name means HTTPS.
func Name(s string) (name, base string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", errors.New("no server given")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || (u.Scheme != "https" && u.Scheme != "http") {
		return "", "", fmt.Errorf("%q is not a server URL", s)
	}
	return u.Host, u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

func service(name string) string { return "araldo:" + name }

// Where says where a host's credential is kept.
type Where string

// Places a credential can be.
const (
	InKeyring Where = "keyring"
	InFile    Where = "file"
	InEnv     Where = "environment"
)

// SignIn records an account on a host and keeps its token: in the keychain,
// or in hosts.yaml when there is none (or insecure is set). It becomes the
// default host when there is no other.
func (s *Store) SignIn(name, base, user, token string, insecure bool) (Where, error) {
	f, err := s.Load()
	if err != nil {
		return "", err
	}
	h := &Host{URL: base, User: user}
	where := InKeyring
	if insecure || s.Keyring.Set(service(name), name, token) != nil {
		h.Token, where = token, InFile
	}
	f.Hosts[name] = h
	if f.Default == "" || f.Hosts[f.Default] == nil {
		f.Default = name
	}
	return where, s.Save(f)
}

// SignOut forgets a host and its token.
func (s *Store) SignOut(name string) error {
	f, err := s.Load()
	if err != nil {
		return err
	}
	if _, ok := f.Hosts[name]; !ok {
		return fmt.Errorf("%w to %s", ErrNotSignedIn, name)
	}
	_ = s.Keyring.Delete(service(name), name)
	delete(f.Hosts, name)
	if f.Default == name {
		f.Default = ""
		names := make([]string, 0, len(f.Hosts))
		for n := range f.Hosts {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > 0 {
			f.Default = names[0]
		}
	}
	return s.Save(f)
}

// Credential is what a command calls the API with.
type Credential struct {
	Name, URL, Token, User string
	Where                  Where
}

// Resolve finds the server and token for a command: the --hostname given
// (or ARALDO_HOST, or the default host), and ARALDO_TOKEN or the stored
// token. ARALDO_URL and ARALDO_API_KEY are read as well, as `araldo mcp`
// always has.
func (s *Store) Resolve(hostname string) (Credential, error) {
	if hostname == "" {
		hostname = first(os.Getenv("ARALDO_HOST"), os.Getenv("ARALDO_URL"))
	}
	envToken := first(os.Getenv("ARALDO_TOKEN"), os.Getenv("ARALDO_API_KEY"))
	f, err := s.Load()
	if err != nil {
		return Credential{}, err
	}
	if hostname == "" {
		hostname = f.Default
	}
	if hostname == "" {
		return Credential{}, fmt.Errorf("%w: run araldo auth login", ErrNotSignedIn)
	}
	name, base, err := Name(hostname)
	if err != nil {
		return Credential{}, err
	}
	c := Credential{Name: name, URL: base}
	if h := f.Hosts[name]; h != nil {
		c.URL, c.User = h.URL, h.User
	}
	switch {
	case envToken != "":
		c.Token, c.Where = envToken, InEnv
	case f.Hosts[name] == nil:
		return Credential{}, fmt.Errorf("%w to %s: run araldo auth login --hostname %s", ErrNotSignedIn, name, name)
	case f.Hosts[name].Token != "":
		c.Token, c.Where = f.Hosts[name].Token, InFile
	default:
		t, err := s.Keyring.Get(service(name), name)
		if err != nil {
			return Credential{}, fmt.Errorf("%w to %s: run araldo auth login (the keychain has no token: %w)", ErrNotSignedIn, name, err)
		}
		c.Token, c.Where = t, InKeyring
	}
	return c, nil
}

func first(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
