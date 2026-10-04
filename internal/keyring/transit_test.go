// SPDX-License-Identifier: AGPL-3.0-or-later

package keyring

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeBao is a Transit engine and a Kubernetes auth method, enough of
// OpenBao's API for the keyring.
type fakeBao struct {
	t      *testing.T
	aead   cipher.AEAD
	mu     sync.Mutex
	tokens map[string]bool
	logins int
	calls  int
}

func newFakeBao(t *testing.T) (*fakeBao, *httptest.Server) {
	t.Helper()
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	aead, err := newAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeBao{t: t, aead: aead, tokens: map[string]bool{"static": true}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

// restart forgets every issued token, as a server restart does.
func (f *fakeBao) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = map[string]bool{"static": true}
}

func (f *fakeBao) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/v1/auth/kubernetes-test/login":
		if body["role"] != "araldo" || body["jwt"] != "sa-token" {
			reply(http.StatusBadRequest, map[string]any{"errors": []string{"invalid role or token"}})
			return
		}
		f.logins++
		tok := uuid.NewString()
		f.tokens[tok] = true
		reply(http.StatusOK, map[string]any{"auth": map[string]any{"client_token": tok, "lease_duration": 3600}})
		return
	case "/v1/transit/encrypt/araldo-master", "/v1/transit/decrypt/araldo-master":
	default:
		reply(http.StatusNotFound, map[string]any{"errors": []string{"no handler for " + r.URL.Path}})
		return
	}
	if !f.tokens[r.Header.Get("X-Vault-Token")] {
		reply(http.StatusForbidden, map[string]any{"errors": []string{"permission denied"}})
		return
	}
	f.calls++
	if strings.Contains(r.URL.Path, "/encrypt/") {
		plain, _ := base64.StdEncoding.DecodeString(body["plaintext"])
		nonce := make([]byte, f.aead.NonceSize())
		_, _ = rand.Read(nonce)
		sealed := f.aead.Seal(nonce, nonce, plain, nil)
		reply(http.StatusOK, map[string]any{"data": map[string]string{"ciphertext": "vault:v1:" + base64.StdEncoding.EncodeToString(sealed)}})
		return
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(body["ciphertext"], "vault:v1:"))
	n := f.aead.NonceSize()
	if err != nil || len(raw) < n {
		reply(http.StatusBadRequest, map[string]any{"errors": []string{"invalid ciphertext"}})
		return
	}
	plain, err := f.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		reply(http.StatusBadRequest, map[string]any{"errors": []string{"cipher: message authentication failed"}})
		return
	}
	reply(http.StatusOK, map[string]any{"data": map[string]string{"plaintext": base64.StdEncoding.EncodeToString(plain)}})
}

func jwtFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("sa-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTransit(t *testing.T, srv *httptest.Server, cfg TransitConfig) *Transit {
	t.Helper()
	cfg.Addr, cfg.Key, cfg.HTTP = srv.URL, "araldo-master", srv.Client()
	tr, err := NewTransit(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestTransitWrapsAndBindsDataKeys(t *testing.T) {
	t.Parallel()
	_, srv := newFakeBao(t)
	tr := newTransit(t, srv, TransitConfig{Token: "static"})
	if tr.ID() != "transit:transit/araldo-master" {
		t.Fatalf("ID = %q", tr.ID())
	}
	dek := []byte("0123456789abcdef0123456789abcdef")
	aad := wrapAAD(uuid.New(), 1)
	wrapped, err := tr.Wrap(t.Context(), dek, aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wrapped, dek) {
		t.Fatal("wrapped key contains the data key")
	}
	got, err := tr.Unwrap(t.Context(), wrapped, aad)
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("Unwrap = %q, %v", got, err)
	}
	// Moved to another scope or version, it must not open.
	for name, other := range map[string][]byte{"other scope": wrapAAD(uuid.New(), 1), "other version": append(bytes.Clone(aad[:len(aad)-1]), '2')} {
		if _, err := tr.Unwrap(t.Context(), wrapped, other); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("%s: Unwrap = %v, want ErrDecrypt", name, err)
		}
	}
}

func TestTransitLogsInWithTheServiceAccount(t *testing.T) {
	t.Parallel()
	bao, srv := newFakeBao(t)
	tr := newTransit(t, srv, TransitConfig{Role: "araldo", AuthPath: "kubernetes-test", JWTFile: jwtFile(t)})
	now := time.Now()
	tr.now = func() time.Time { return now }
	aad := wrapAAD(uuid.New(), 1)
	wrapped, err := tr.Wrap(t.Context(), []byte("k"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Unwrap(t.Context(), wrapped, aad); err != nil {
		t.Fatal(err)
	}
	if bao.logins != 1 {
		t.Fatalf("logins = %d, want 1 (the token is reused)", bao.logins)
	}
	// Past half the lease, it logs in again.
	now = now.Add(31 * time.Minute)
	if _, err := tr.Unwrap(t.Context(), wrapped, aad); err != nil || bao.logins != 2 {
		t.Fatalf("after half the lease: %v, logins = %d, want 2", err, bao.logins)
	}
	// A restarted server rejects the token; one fresh login recovers.
	bao.restart()
	if _, err := tr.Unwrap(t.Context(), wrapped, aad); err != nil || bao.logins != 3 {
		t.Fatalf("after a restart: %v, logins = %d, want 3", err, bao.logins)
	}
}

func TestTransitRefusesABadLogin(t *testing.T) {
	t.Parallel()
	_, srv := newFakeBao(t)
	tr := newTransit(t, srv, TransitConfig{Role: "someone-else", AuthPath: "kubernetes-test", JWTFile: jwtFile(t)})
	_, err := tr.Wrap(t.Context(), []byte("k"), []byte("aad"))
	if err == nil || !strings.Contains(err.Error(), "invalid role") {
		t.Fatalf("Wrap = %v, want the login error", err)
	}
	if _, err := NewTransit(TransitConfig{Addr: srv.URL, Key: "k"}); err == nil {
		t.Fatal("NewTransit without a token or role succeeded")
	}
	if _, err := NewTransit(TransitConfig{Addr: srv.URL, Token: "static"}); err == nil {
		t.Fatal("NewTransit without a key succeeded")
	}
}

// Moving an install from a local master key to Transit: Prepend, rewrap,
// then drop the local key. Secrets and signed links survive.
func TestMoveToTransit(t *testing.T) {
	t.Parallel()
	_, srv := newFakeBao(t)
	st := NewMemStore()
	local := masterKeys(t, "k1")
	before := newKeyring(t, local, st)
	org := uuid.New()
	ct, err := before.Encrypt(t.Context(), org, "a", []byte("app-password"))
	if err != nil {
		t.Fatal(err)
	}
	link := before.Derive("media-links")

	mk, err := ParseMasterKeys(local)
	if err != nil {
		t.Fatal(err)
	}
	both, err := mk.Prepend(newTransit(t, srv, TransitConfig{Token: "static"}))
	if err != nil {
		t.Fatal(err)
	}
	during, err := Open(t.Context(), both, st)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := during.RewrapAll(t.Context()); err != nil || n != 2 {
		t.Fatalf("RewrapAll = %d, %v; want 2 (the org's key and the signing key)", n, err)
	}
	use, err := during.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(use) != 2 || !use[0].Primary || use[0].DataKeys != 2 || use[1].ID != "k1" || use[1].DataKeys != 0 {
		t.Fatalf("Usage = %+v; want Transit primary with 2 keys and k1 unused", use)
	}

	only, err := NewMasterKeys(newTransit(t, srv, TransitConfig{Token: "static"}))
	if err != nil {
		t.Fatal(err)
	}
	after, err := Open(t.Context(), only, st)
	if err != nil {
		t.Fatal(err)
	}
	got, err := after.Decrypt(t.Context(), org, "a", ct)
	if err != nil || string(got) != "app-password" {
		t.Fatalf("Decrypt after the move = %q, %v", got, err)
	}
	if !bytes.Equal(after.Derive("media-links"), link) {
		t.Fatal("the move changed signed links")
	}
}

func TestUsageReportsUnconfiguredKeys(t *testing.T) {
	t.Parallel()
	st := NewMemStore()
	old, newer := masterKeys(t, "old"), masterKeys(t, "new")
	newKeyring(t, old, st)
	k := newKeyring(t, newer+","+old, st)
	use, err := k.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(use) != 2 || use[0].ID != "new" || use[1].ID != "old" || use[1].DataKeys != 1 {
		t.Fatalf("Usage = %+v", use)
	}
	// Seen from an install that has dropped "old" too early.
	m, err := ParseMasterKeys(newer)
	if err != nil {
		t.Fatal(err)
	}
	kr := &Keyring{master: m, store: st, cache: &keyCache{aead: map[cacheKey]cipher.AEAD{}}}
	use, err = kr.Usage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if last := use[len(use)-1]; last.ID != "old" || last.Configured || last.DataKeys != 1 {
		t.Fatalf("Usage = %+v; want the unconfigured key listed", use)
	}
}
