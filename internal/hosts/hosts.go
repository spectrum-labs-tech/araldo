// SPDX-License-Identifier: AGPL-3.0-or-later

// Package hosts keeps the Araldo servers the CLI is signed in to (ADR
// 0028): hosts.yaml in the config directory names each server and, as the
// Stripe CLI does, up to two keys on it, a test one and a live one, since
// every key belongs to one mode (ADR 0006). Each key lives in the OS
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

// Host is one server and the keys signed in to it, one per mode.
type Host struct {
	// URL is the server, e.g. https://araldo.example.com.
	URL  string   `yaml:"url"`
	Test *Account `yaml:"test,omitempty"`
	Live *Account `yaml:"live,omitempty"`
	// Org is the org a user token acts in by default, set with araldo auth
	// login --org (ADR 0028).
	Org string `yaml:"org,omitempty"`
	// ListenSecret signs the events araldo listen forwards from this server,
	// made the first time and kept, so a local receiver is set up once.
	ListenSecret string `yaml:"listen_secret,omitempty"`
	// User and Token are the one key of a file written before modes, read
	// as the test key: that is what the CLI made then.
	User  string `yaml:"user,omitempty"`
	Token string `yaml:"token,omitempty"`
}

// Account is one key on a host.
type Account struct {
	// User describes the key, as the server reported it at sign-in.
	User string `yaml:"user,omitempty"`
	// Token is set only when there was no keychain to keep it in.
	Token string `yaml:"token,omitempty"`
}

// account is the host's key for a mode, or nil.
func (h *Host) account(live bool) *Account {
	if live {
		return h.Live
	}
	return h.Test
}

// Mode names a mode for messages.
func Mode(live bool) string {
	if live {
		return "live"
	}
	return "test"
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
	for _, h := range f.Hosts {
		if h.Test == nil && (h.User != "" || h.Token != "") {
			h.Test = &Account{User: h.User, Token: h.Token}
		}
		h.User, h.Token = "", ""
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

// keyUser is the keychain entry for a host's key in a mode. The test key
// keeps the name the one key had before modes.
func keyUser(name string, live bool) string {
	if live {
		return name + "#live"
	}
	return name
}

// Where says where a host's credential is kept.
type Where string

// Places a credential can be.
const (
	InKeyring Where = "keyring"
	InFile    Where = "file"
	InEnv     Where = "environment"
)

// SignIn keeps a host's key for a mode, replacing any earlier one for that
// mode: in the keychain, or in hosts.yaml when there is none (or insecure
// is set). The host becomes the default when there is no other.
func (s *Store) SignIn(name, base, user, token string, live, insecure bool) (Where, error) {
	f, err := s.Load()
	if err != nil {
		return "", err
	}
	h := f.Hosts[name]
	if h == nil {
		h = &Host{}
		f.Hosts[name] = h
	}
	h.URL = base
	a := &Account{User: user}
	where := InKeyring
	if insecure || s.Keyring.Set(service(name), keyUser(name, live), token) != nil {
		a.Token, where = token, InFile
	}
	if live {
		h.Live = a
	} else {
		h.Test = a
	}
	if f.Default == "" || f.Hosts[f.Default] == nil {
		f.Default = name
	}
	return where, s.Save(f)
}

// SetOrg sets the org a host's user tokens act in by default ("" for none).
func (s *Store) SetOrg(name, org string) error {
	f, err := s.Load()
	if err != nil {
		return err
	}
	h := f.Hosts[name]
	if h == nil {
		return fmt.Errorf("%w to %s", ErrNotSignedIn, name)
	}
	h.Org = org
	return s.Save(f)
}

// ListenSecret returns the secret araldo listen signs this host's events
// with, making and keeping one the first time.
func (s *Store) ListenSecret(name string, newSecret func() string) (string, error) {
	f, err := s.Load()
	if err != nil {
		return "", err
	}
	h := f.Hosts[name]
	if h == nil {
		h = &Host{}
		f.Hosts[name] = h
	}
	if h.ListenSecret == "" {
		h.ListenSecret = newSecret()
		if err := s.Save(f); err != nil {
			return "", err
		}
	}
	return h.ListenSecret, nil
}

// SignOut forgets a host and both its keys.
func (s *Store) SignOut(name string) error {
	f, err := s.Load()
	if err != nil {
		return err
	}
	if _, ok := f.Hosts[name]; !ok {
		return fmt.Errorf("%w to %s", ErrNotSignedIn, name)
	}
	_ = s.Keyring.Delete(service(name), keyUser(name, false))
	_ = s.Keyring.Delete(service(name), keyUser(name, true))
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
	// Org is the org to act in: ARALDO_ORG, or the host's default.
	Org   string
	Where Where
	// Live is the mode asked for; a token from the environment is in
	// whatever mode it is.
	Live bool
}

// Resolve finds the server and key for a command: the --hostname given (or
// ARALDO_HOST, or the default host), and ARALDO_TOKEN or the stored key for
// the mode (test unless live). ARALDO_URL and ARALDO_API_KEY are read as
// well, as `araldo mcp` always has.
func (s *Store) Resolve(hostname string, live bool) (Credential, error) {
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
	c := Credential{Name: name, URL: base, Live: live, Org: os.Getenv("ARALDO_ORG")}
	h := f.Hosts[name]
	if h != nil && h.URL != "" {
		c.URL = h.URL
	}
	if h != nil && c.Org == "" {
		c.Org = h.Org
	}
	if envToken != "" {
		c.Token, c.Where = envToken, InEnv
		return c, nil
	}
	login := "araldo auth login --hostname " + name
	if live {
		login += " --live"
	}
	var a *Account
	if h != nil {
		a = h.account(live)
	}
	if a == nil {
		return Credential{}, fmt.Errorf("%w to %s in %s mode: run %s", ErrNotSignedIn, name, Mode(live), login)
	}
	c.User = a.User
	if a.Token != "" {
		c.Token, c.Where = a.Token, InFile
		return c, nil
	}
	t, err := s.Keyring.Get(service(name), keyUser(name, live))
	if err != nil {
		return Credential{}, fmt.Errorf("%w to %s in %s mode: run %s (the keychain has no key: %w)", ErrNotSignedIn, name, Mode(live), login, err)
	}
	c.Token, c.Where = t, InKeyring
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
