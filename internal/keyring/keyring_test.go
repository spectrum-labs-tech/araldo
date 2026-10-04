// SPDX-License-Identifier: AGPL-3.0-or-later

package keyring

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func masterKeys(t *testing.T, ids ...string) string {
	t.Helper()
	s := ""
	for i, kid := range ids {
		k, err := GenerateMasterKey(kid)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			s += ","
		}
		s += k
	}
	return s
}

func newKeyring(t *testing.T, spec string, st Store) *Keyring {
	t.Helper()
	m, err := ParseMasterKeys(spec)
	if err != nil {
		t.Fatal(err)
	}
	return New(m, st)
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	kr := newKeyring(t, masterKeys(t, "k1"), NewMemStore())
	org, row := uuid.New(), uuid.New()
	aad := AAD("channels", "credentials", row)
	ct, err := kr.Encrypt(t.Context(), org, aad, []byte("oauth-token"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("oauth-token")) {
		t.Fatal("ciphertext contains the plaintext")
	}
	got, err := kr.Decrypt(t.Context(), org, aad, ct)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "oauth-token" {
		t.Fatalf("Decrypt = %q", got)
	}
}

func TestDecryptRefusesMovedCiphertext(t *testing.T) {
	t.Parallel()
	kr := newKeyring(t, masterKeys(t, "k1"), NewMemStore())
	org, otherOrg, row := uuid.New(), uuid.New(), uuid.New()
	ct, err := kr.Encrypt(t.Context(), org, AAD("channels", "credentials", row), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.Encrypt(t.Context(), otherOrg, "x", []byte("make a key")); err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Clone(ct)
	tampered[len(tampered)-1] ^= 1
	tests := map[string]struct {
		scope uuid.UUID
		aad   string
		ct    []byte
	}{
		"another row":    {org, AAD("channels", "credentials", uuid.New()), ct},
		"another column": {org, AAD("channels", "refresh_token", row), ct},
		"another org":    {otherOrg, AAD("channels", "credentials", row), ct},
		"tampered":       {org, AAD("channels", "credentials", row), tampered},
		"truncated":      {org, AAD("channels", "credentials", row), ct[:3]},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := kr.Decrypt(t.Context(), tt.scope, tt.aad, tt.ct); err == nil {
				t.Fatal("Decrypt succeeded, want an error")
			}
		})
	}
}

func TestRotationKeepsOldSecretsReadable(t *testing.T) {
	t.Parallel()
	st := NewMemStore()
	k1, k2 := masterKeys(t, "k1"), masterKeys(t, "k2")
	kr := newKeyring(t, k1, st)
	org := uuid.New()
	old, err := kr.Encrypt(t.Context(), org, "a", []byte("before"))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := kr.Rotate(t.Context(), org); err != nil || v != 2 {
		t.Fatalf("Rotate = %d, %v; want version 2", v, err)
	}
	newer, err := kr.Encrypt(t.Context(), org, "a", []byte("after"))
	if err != nil {
		t.Fatal(err)
	}

	// A new primary master key: rewrap everything, then drop k1.
	n, err := newKeyring(t, k2+","+k1, st).RewrapAll(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("RewrapAll = %d, %v; want 2 keys (the signing key is made on first use)", n, err)
	}
	m3, err := ParseMasterKeys(masterKeys(t, "k3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(m3, st).Decrypt(t.Context(), org, "a", old); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Decrypt with an unknown master key = %v, want ErrNoKey", err)
	}
	onlyK2 := newKeyring(t, k2, st)
	for want, ct := range map[string][]byte{"before": old, "after": newer} {
		got, err := onlyK2.Decrypt(t.Context(), org, "a", ct)
		if err != nil || string(got) != want {
			t.Fatalf("Decrypt after rewrap = %q, %v; want %q", got, err, want)
		}
	}
}

func TestDeletedScopeCannotBeRead(t *testing.T) {
	t.Parallel()
	st := NewMemStore()
	kr := newKeyring(t, masterKeys(t, "k1"), st)
	org := uuid.New()
	ct, err := kr.Encrypt(t.Context(), org, "a", []byte("gone"))
	if err != nil {
		t.Fatal(err)
	}
	st.Delete(org)
	kr.Forget(org)
	if _, err := kr.Decrypt(t.Context(), org, "a", ct); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Decrypt after shredding = %v, want ErrNoKey", err)
	}
}

func TestParseMasterKeys(t *testing.T) {
	t.Parallel()
	good := masterKeys(t, "a", "b")
	m, err := ParseMasterKeys(good)
	if err != nil {
		t.Fatal(err)
	}
	if m.Primary() != "a" || len(m.IDs()) != 2 {
		t.Fatalf("primary %q ids %v", m.Primary(), m.IDs())
	}
	bad := map[string]string{
		"empty":     "",
		"no id":     ":AAAA",
		"not b64":   "a:!!!",
		"too short": "a:AAAA",
		"duplicate": masterKeys(t, "a") + "," + masterKeys(t, "a"),
	}
	for name, s := range bad {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseMasterKeys(s); err == nil {
				t.Fatalf("ParseMasterKeys(%q) succeeded", s)
			}
		})
	}
}

func TestDerive(t *testing.T) {
	t.Parallel()
	spec := masterKeys(t, "a", "b")
	first, second, _ := strings.Cut(spec, ",")
	st := NewMemStore()
	a := newKeyring(t, first, st)
	if got := derive(t, a, "media-urls"); len(got) != 32 || bytes.Equal(got, derive(t, a, "other")) {
		t.Fatalf("Derive: %x (labels must give different keys)", got)
	}
	// Earlier versions derived from the primary key's raw bytes; the stored
	// root starts from the same value, so links they signed stay valid.
	_, enc, _ := strings.Cut(first, ":")
	raw, err := decodeKey(enc)
	if err != nil {
		t.Fatal(err)
	}
	if want := hmacSHA256(hmacSHA256(raw, "araldo:v1:derive"), "media-urls"); !bytes.Equal(derive(t, a, "media-urls"), want) {
		t.Fatal("Derive differs from the earlier derivation")
	}
	// A new primary master key leaves the stored root, and so every derived
	// key, unchanged.
	if !bytes.Equal(derive(t, a, "media-urls"), derive(t, newKeyring(t, second+","+first, st), "media-urls")) {
		t.Fatal("a new primary master key changed Derive")
	}
	// Seeded from the same local key, another store starts from the same root.
	if !bytes.Equal(derive(t, a, "media-urls"), derive(t, newKeyring(t, first, NewMemStore()), "media-urls")) {
		t.Fatal("the same local key should seed the same root")
	}
}

func derive(t *testing.T, k *Keyring, label string) []byte {
	t.Helper()
	key, err := k.Derive(t.Context(), label)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// failingKEK is a key service that cannot be reached.
type failingKEK struct{}

func (failingKEK) ID() string { return "down" }
func (failingKEK) Wrap(context.Context, []byte, []byte) ([]byte, error) {
	return nil, unavailable(errors.New("connection refused"))
}
func (failingKEK) Unwrap(context.Context, []byte, []byte) ([]byte, error) {
	return nil, unavailable(errors.New("connection refused"))
}

// Without master keys, or with their service down, the keyring still
// exists: what needs a key fails with ErrUnavailable, nothing panics, and
// it recovers once keys are back.
func TestKeyringDegrades(t *testing.T) {
	t.Parallel()
	down, err := NewMasterKeys(failingKEK{})
	if err != nil {
		t.Fatal(err)
	}
	for name, k := range map[string]*Keyring{"no master keys": New(nil, NewMemStore()), "key service down": New(down, NewMemStore())} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := k.Check(t.Context()); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Check = %v, want ErrUnavailable", err)
			}
			if _, err := k.Encrypt(t.Context(), uuid.New(), "a", []byte("x")); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Encrypt = %v, want ErrUnavailable", err)
			}
			if _, err := k.Derive(t.Context(), "media-links"); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Derive = %v, want ErrUnavailable", err)
			}
		})
	}
	// A data key wrapped by a service that is now down is unavailable, not
	// corrupt.
	st := NewMemStore()
	local := masterKeys(t, "k1")
	ok := newKeyring(t, local, st)
	org := uuid.New()
	ct, err := ok.Encrypt(t.Context(), org, "a", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := New(mustPrepend(t, local, failingKEK{}), st).RewrapAll(t.Context()); !errors.Is(err, ErrUnavailable) || n != 0 {
		t.Fatalf("RewrapAll to a down service = %d, %v; want ErrUnavailable", n, err)
	}
	if got, err := newKeyring(t, local, st).Decrypt(t.Context(), org, "a", ct); err != nil || string(got) != "secret" {
		t.Fatalf("Decrypt after a failed move = %q, %v", got, err)
	}
}

func mustPrepend(t *testing.T, spec string, k KEK) *MasterKeys {
	t.Helper()
	m, err := ParseMasterKeys(spec)
	if err != nil {
		t.Fatal(err)
	}
	both, err := m.Prepend(k)
	if err != nil {
		t.Fatal(err)
	}
	return both
}
