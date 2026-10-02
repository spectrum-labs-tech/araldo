// SPDX-License-Identifier: AGPL-3.0-or-later

package keyring

import (
	"bytes"
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
		t.Fatalf("RewrapAll = %d, %v; want 2 keys", n, err)
	}
	unrelated := newKeyring(t, masterKeys(t, "k3"), st)
	if _, err := unrelated.Decrypt(t.Context(), org, "a", old); !errors.Is(err, ErrNoKey) {
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
	a := newKeyring(t, first, nil)
	if got := a.Derive("media-urls"); len(got) != 32 || bytes.Equal(got, a.Derive("other")) {
		t.Fatalf("Derive: %x (labels must give different keys)", got)
	}
	// The primary key alone decides; a new primary gives new keys.
	if !bytes.Equal(a.Derive("media-urls"), newKeyring(t, first+","+second, nil).Derive("media-urls")) {
		t.Fatal("Derive depends on more than the primary key")
	}
	if bytes.Equal(a.Derive("media-urls"), newKeyring(t, second+","+first, nil).Derive("media-urls")) {
		t.Fatal("a new primary key should give new derived keys")
	}
}
