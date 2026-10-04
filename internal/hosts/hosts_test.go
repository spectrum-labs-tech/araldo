// SPDX-License-Identifier: AGPL-3.0-or-later

package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// memKeyring is a keychain in memory; broken makes every call fail, as on a
// machine without one.
type memKeyring struct {
	secrets map[string]string
	broken  bool
}

func (k *memKeyring) Get(service, user string) (string, error) {
	if s, ok := k.secrets[service+"|"+user]; ok && !k.broken {
		return s, nil
	}
	return "", errors.New("not found")
}

func (k *memKeyring) Set(service, user, secret string) error {
	if k.broken {
		return errors.New("no keychain")
	}
	k.secrets[service+"|"+user] = secret
	return nil
}

func (k *memKeyring) Delete(service, user string) error {
	delete(k.secrets, service+"|"+user)
	return nil
}

func newStore(t *testing.T, broken bool) (*Store, *memKeyring) {
	t.Helper()
	kr := &memKeyring{secrets: map[string]string{}, broken: broken}
	return &Store{Dir: t.TempDir(), Keyring: kr}, kr
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ARALDO_HOST", "ARALDO_URL", "ARALDO_TOKEN", "ARALDO_API_KEY"} {
		t.Setenv(k, "")
	}
}

func TestName(t *testing.T) {
	t.Parallel()
	tests := map[string]struct{ name, url string }{
		"araldo.example.com":          {"araldo.example.com", "https://araldo.example.com"},
		"https://araldo.example.com/": {"araldo.example.com", "https://araldo.example.com"},
		"http://localhost:8080":       {"localhost:8080", "http://localhost:8080"},
		"https://example.com/araldo/": {"example.com", "https://example.com/araldo"},
	}
	for in, want := range tests {
		name, url, err := Name(in)
		if err != nil || name != want.name || url != want.url {
			t.Errorf("Name(%q) = %q, %q, %v; want %q, %q", in, name, url, err, want.name, want.url)
		}
	}
	for _, bad := range []string{"", "ftp://x", "https://"} {
		if _, _, err := Name(bad); err == nil {
			t.Errorf("Name(%q) succeeded", bad)
		}
	}
}

// Signing in keeps the token in the keychain, never in the file; the
// first host becomes the default; signing out forgets both.
func TestSignInUsesTheKeychain(t *testing.T) {
	clearEnv(t)
	s, kr := newStore(t, false)
	where, err := s.SignIn("araldo.example.com", "https://araldo.example.com", "Otium (key cli)", "ald_test_secret", false)
	if err != nil || where != InKeyring {
		t.Fatalf("SignIn = %q, %v", where, err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, "hosts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(raw), "ald_test_secret") {
		t.Fatalf("hosts.yaml holds the token:\n%s", raw)
	}
	c, err := s.Resolve("")
	if err != nil || c.Token != "ald_test_secret" || c.Where != InKeyring || c.URL != "https://araldo.example.com" {
		t.Fatalf("Resolve = %+v, %v", c, err)
	}
	if err := s.SignOut("araldo.example.com"); err != nil {
		t.Fatal(err)
	}
	if len(kr.secrets) != 0 {
		t.Fatal("signing out left the token in the keychain")
	}
	if _, err := s.Resolve(""); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Resolve after signing out = %v", err)
	}
}

// Without a keychain the token goes in hosts.yaml, readable only by the
// user, and Resolve says where it came from.
func TestSignInFallsBackToTheFile(t *testing.T) {
	clearEnv(t)
	s, _ := newStore(t, true)
	where, err := s.SignIn("araldo.example.com", "https://araldo.example.com", "", "ald_live_secret", false)
	if err != nil || where != InFile {
		t.Fatalf("SignIn = %q, %v", where, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(s.Dir, "hosts.yaml"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("hosts.yaml mode %v, %v; want 0600", info.Mode().Perm(), err)
		}
	}
	if c, err := s.Resolve("araldo.example.com"); err != nil || c.Token != "ald_live_secret" || c.Where != InFile {
		t.Fatalf("Resolve = %+v, %v", c, err)
	}
}

// The environment wins, for CI: ARALDO_TOKEN over a stored token, and
// ARALDO_HOST over the default.
func TestEnvironmentOverrides(t *testing.T) {
	clearEnv(t)
	s, _ := newStore(t, false)
	if _, err := s.SignIn("a.example.com", "https://a.example.com", "", "stored", false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARALDO_TOKEN", "from-env")
	t.Setenv("ARALDO_HOST", "b.example.com")
	c, err := s.Resolve("")
	if err != nil || c.Name != "b.example.com" || c.Token != "from-env" || c.Where != InEnv {
		t.Fatalf("Resolve = %+v, %v", c, err)
	}
	// A host never signed in to, without a token in the environment.
	t.Setenv("ARALDO_TOKEN", "")
	if _, err := s.Resolve("c.example.com"); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Resolve for an unknown host = %v", err)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
