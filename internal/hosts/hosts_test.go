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
	where, err := s.SignIn("araldo.example.com", "https://araldo.example.com", "Araldo (key cli)", "ald_test_secret", false, false)
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
	c, err := s.Resolve("", false)
	if err != nil || c.Token != "ald_test_secret" || c.Where != InKeyring || c.URL != "https://araldo.example.com" {
		t.Fatalf("Resolve = %+v, %v", c, err)
	}
	if err := s.SignOut("araldo.example.com"); err != nil {
		t.Fatal(err)
	}
	if len(kr.secrets) != 0 {
		t.Fatal("signing out left the token in the keychain")
	}
	if _, err := s.Resolve("", false); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Resolve after signing out = %v", err)
	}
}

// Without a keychain the token goes in hosts.yaml, readable only by the
// user, and Resolve says where it came from.
func TestSignInFallsBackToTheFile(t *testing.T) {
	clearEnv(t)
	s, _ := newStore(t, true)
	where, err := s.SignIn("araldo.example.com", "https://araldo.example.com", "", "ald_live_secret", true, false)
	if err != nil || where != InFile {
		t.Fatalf("SignIn = %q, %v", where, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(s.Dir, "hosts.yaml"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("hosts.yaml mode %v, %v; want 0600", info.Mode().Perm(), err)
		}
	}
	if c, err := s.Resolve("araldo.example.com", true); err != nil || c.Token != "ald_live_secret" || c.Where != InFile {
		t.Fatalf("Resolve = %+v, %v", c, err)
	}
}

// The environment wins, for CI: ARALDO_TOKEN over a stored token, and
// ARALDO_HOST over the default.
func TestEnvironmentOverrides(t *testing.T) {
	clearEnv(t)
	s, _ := newStore(t, false)
	if _, err := s.SignIn("a.example.com", "https://a.example.com", "", "stored", false, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARALDO_TOKEN", "from-env")
	t.Setenv("ARALDO_HOST", "b.example.com")
	c, err := s.Resolve("", false)
	if err != nil || c.Name != "b.example.com" || c.Token != "from-env" || c.Where != InEnv {
		t.Fatalf("Resolve = %+v, %v", c, err)
	}
	// A host never signed in to, without a token in the environment.
	t.Setenv("ARALDO_TOKEN", "")
	if _, err := s.Resolve("c.example.com", false); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Resolve for an unknown host = %v", err)
	}
}

// A host keeps a test key and a live key side by side, as the Stripe CLI
// does; each mode resolves to its own, and a missing one says how to add it.
func TestTestAndLiveKeys(t *testing.T) {
	clearEnv(t)
	s, kr := newStore(t, false)
	const name, base = "araldo.example.com", "https://araldo.example.com"
	if _, err := s.SignIn(name, base, "test key", "ald_test_1", false, false); err != nil {
		t.Fatal(err)
	}
	_, err := s.Resolve("", true)
	if !errors.Is(err, ErrNotSignedIn) || !contains(err.Error(), "araldo auth login --hostname araldo.example.com --live") {
		t.Fatalf("Resolve live with only a test key = %v", err)
	}
	if _, err := s.SignIn(name, base, "live key", "ald_live_1", true, false); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		live       bool
		token, who string
	}{
		{false, "ald_test_1", "test key"},
		{true, "ald_live_1", "live key"},
	}
	for _, tt := range tests {
		c, err := s.Resolve("", tt.live)
		if err != nil || c.Token != tt.token || c.User != tt.who || c.Live != tt.live {
			t.Errorf("Resolve(live=%v) = %+v, %v", tt.live, c, err)
		}
	}
	// Signing in again replaces only that mode's key.
	if _, err := s.SignIn(name, base, "test key 2", "ald_test_2", false, false); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Resolve("", true); c.Token != "ald_live_1" {
		t.Fatalf("a new test key replaced the live one: %+v", c)
	}
	if err := s.SignOut(name); err != nil {
		t.Fatal(err)
	}
	if len(kr.secrets) != 0 {
		t.Fatalf("signing out left keys in the keychain: %v", kr.secrets)
	}
}

// A hosts.yaml written before modes held one key; it is read as the test
// key, which is what the CLI made then.
func TestLegacyHostIsTheTestKey(t *testing.T) {
	clearEnv(t)
	s, _ := newStore(t, true)
	legacy := `default: araldo.example.com
hosts:
  araldo.example.com:
    url: https://araldo.example.com
    user: old
    token: ald_test_old
`
	if err := os.WriteFile(filepath.Join(s.Dir, "hosts.yaml"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Resolve("", false); err != nil || c.Token != "ald_test_old" || c.User != "old" {
		t.Fatalf("Resolve test = %+v, %v", c, err)
	}
	if _, err := s.Resolve("", true); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Resolve live = %v", err)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
