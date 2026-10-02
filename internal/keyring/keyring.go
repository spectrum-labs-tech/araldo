// SPDX-License-Identifier: AGPL-3.0-or-later

// Package keyring encrypts the secrets Araldo stores (ADR 0008): OAuth
// tokens, app passwords, webhook signing secrets, TOTP secrets.
//
// Master keys come from outside the database. Each scope (an org, or the
// install itself for secrets that belong to no org) has data keys, stored
// wrapped by a master key. Secrets are sealed with AES-256-GCM under the
// scope's current data key, with associated data naming the table, column
// and row they belong to, so a ciphertext copied elsewhere fails to open.
package keyring

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Install is the scope of secrets that belong to no org.
var Install = uuid.Nil

// Errors.
var (
	// ErrNoKey means a scope has no data key yet (store) or a master key is
	// not configured (keyring).
	ErrNoKey = errors.New("keyring: no such key")
	// ErrConflict means another process stored the same data key version
	// first.
	ErrConflict = errors.New("keyring: data key version exists")
	// ErrDecrypt means a ciphertext is corrupt, was moved, or was sealed
	// under a key this keyring does not have.
	ErrDecrypt = errors.New("keyring: cannot decrypt")
)

const (
	formatV1  = 1
	keySize   = 32
	headerLen = 1 + 4 // format byte, data key version
)

// WrappedKey is a data key as stored: sealed under master key KEKID.
type WrappedKey struct {
	Scope   uuid.UUID
	Version int
	KEKID   string
	Wrapped []byte
}

// Store keeps wrapped data keys. Implementations must make (Scope,
// Version) unique and return ErrConflict on a duplicate insert.
type Store interface {
	// CurrentDataKey returns the scope's highest version, or ErrNoKey.
	CurrentDataKey(ctx context.Context, scope uuid.UUID) (WrappedKey, error)
	// DataKey returns one version, or ErrNoKey.
	DataKey(ctx context.Context, scope uuid.UUID, version int) (WrappedKey, error)
	InsertDataKey(ctx context.Context, k WrappedKey) error
	// DataKeys lists every stored key, for rewrapping.
	DataKeys(ctx context.Context) ([]WrappedKey, error)
	// RewrapDataKey replaces the wrapping of one key version.
	RewrapDataKey(ctx context.Context, k WrappedKey) error
}

// MasterKeys are the key-encryption keys. The first is primary: new data
// keys are wrapped with it.
type MasterKeys struct {
	primary string
	keys    map[string]cipher.AEAD
	order   []string
	// derive is the root for Derive, from the primary key; the raw key
	// itself is not kept.
	derive []byte
}

// ParseMasterKeys reads "id:base64key[,id:base64key...]" (standard or URL
// base64 of 32 bytes). The first key is primary.
func ParseMasterKeys(s string) (*MasterKeys, error) {
	m := &MasterKeys{keys: map[string]cipher.AEAD{}}
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, enc, ok := strings.Cut(part, ":")
		if !ok || kid == "" {
			return nil, errors.New("keyring: master key must be id:base64")
		}
		if _, dup := m.keys[kid]; dup {
			return nil, fmt.Errorf("keyring: master key %q given twice", kid)
		}
		raw, err := decodeKey(enc)
		if err != nil {
			return nil, fmt.Errorf("keyring: master key %q: %w", kid, err)
		}
		aead, err := newAEAD(raw)
		if err != nil {
			return nil, err
		}
		m.keys[kid] = aead
		m.order = append(m.order, kid)
		if m.primary == "" {
			m.primary = kid
			m.derive = hmacSHA256(raw, "araldo:v1:derive")
		}
	}
	if m.primary == "" {
		return nil, errors.New("keyring: no master key configured")
	}
	return m, nil
}

// GenerateMasterKey returns a new "id:base64" master key entry.
func GenerateMasterKey(kid string) (string, error) {
	b := make([]byte, keySize)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return kid + ":" + base64.StdEncoding.EncodeToString(b), nil
}

// Primary is the ID of the key new data keys are wrapped with.
func (m *MasterKeys) Primary() string { return m.primary }

// IDs lists the configured master key IDs, primary first.
func (m *MasterKeys) IDs() []string { return append([]string(nil), m.order...) }

func decodeKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) != keySize {
				return nil, fmt.Errorf("key is %d bytes, want %d", len(b), keySize)
			}
			return b, nil
		}
	}
	return nil, errors.New("key is not base64")
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("keyring: %w", err)
	}
	return cipher.NewGCM(block)
}

func wrapAAD(scope uuid.UUID, version int) []byte {
	return fmt.Appendf(nil, "araldo:v1:data-key:%s:%d", scope, version)
}

func (m *MasterKeys) wrap(scope uuid.UUID, version int, dek []byte) (WrappedKey, error) {
	aead := m.keys[m.primary]
	sealed, err := seal(aead, dek, wrapAAD(scope, version))
	if err != nil {
		return WrappedKey{}, err
	}
	return WrappedKey{Scope: scope, Version: version, KEKID: m.primary, Wrapped: sealed}, nil
}

func (m *MasterKeys) unwrap(k WrappedKey) ([]byte, error) {
	aead, ok := m.keys[k.KEKID]
	if !ok {
		return nil, fmt.Errorf("%w: master key %q is not configured", ErrNoKey, k.KEKID)
	}
	dek, err := open(aead, k.Wrapped, wrapAAD(k.Scope, k.Version))
	if err != nil {
		return nil, fmt.Errorf("%w: data key %s v%d under master key %q", ErrDecrypt, k.Scope, k.Version, k.KEKID)
	}
	return dek, nil
}

func seal(aead cipher.AEAD, plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

func open(aead cipher.AEAD, sealed, aad []byte) ([]byte, error) {
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrDecrypt
	}
	n := aead.NonceSize()
	return aead.Open(nil, sealed[:n], sealed[n:], aad)
}

// Keyring seals and opens secrets.
type Keyring struct {
	master *MasterKeys
	store  Store

	mu    sync.Mutex
	cache map[cacheKey]cipher.AEAD
}

type cacheKey struct {
	scope   uuid.UUID
	version int
}

// New returns a keyring using master keys m and data keys in st.
func New(m *MasterKeys, st Store) *Keyring {
	return &Keyring{master: m, store: st, cache: map[cacheKey]cipher.AEAD{}}
}

// AAD names the place a secret is stored: its table, column and row.
func AAD(table, column string, row uuid.UUID) string {
	return "araldo:v1:" + table + ":" + column + ":" + row.String()
}

// Encrypt seals plaintext for scope under its current data key, creating
// the scope's first data key if it has none.
func (k *Keyring) Encrypt(ctx context.Context, scope uuid.UUID, aad string, plaintext []byte) ([]byte, error) {
	version, aead, err := k.current(ctx, scope)
	if err != nil {
		return nil, err
	}
	sealed, err := seal(aead, plaintext, []byte(aad))
	if err != nil {
		return nil, err
	}
	out := make([]byte, headerLen, headerLen+len(sealed))
	out[0] = formatV1
	binary.BigEndian.PutUint32(out[1:], uint32(version)) //nolint:gosec // versions are small positive integers
	return append(out, sealed...), nil
}

// Decrypt opens a ciphertext made by Encrypt for the same scope and aad.
func (k *Keyring) Decrypt(ctx context.Context, scope uuid.UUID, aad string, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < headerLen || ciphertext[0] != formatV1 {
		return nil, ErrDecrypt
	}
	version := int(binary.BigEndian.Uint32(ciphertext[1:headerLen]))
	aead, err := k.version(ctx, scope, version)
	if err != nil {
		return nil, err
	}
	plain, err := open(aead, ciphertext[headerLen:], []byte(aad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

// Derive returns a 32-byte key for one purpose (label), from the primary
// master key: for signing, not for encrypting stored data. It changes when
// the primary key does.
func (k *Keyring) Derive(label string) []byte {
	return hmacSHA256(k.master.derive, label)
}

func hmacSHA256(key []byte, label string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(label))
	return mac.Sum(nil)
}

// Forget drops a scope's cached data keys (after the scope is deleted).
func (k *Keyring) Forget(scope uuid.UUID) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for ck := range k.cache {
		if ck.scope == scope {
			delete(k.cache, ck)
		}
	}
}

func (k *Keyring) current(ctx context.Context, scope uuid.UUID) (int, cipher.AEAD, error) {
	wk, err := k.store.CurrentDataKey(ctx, scope)
	if errors.Is(err, ErrNoKey) {
		wk, err = k.create(ctx, scope, 1)
	}
	if err != nil {
		return 0, nil, err
	}
	aead, err := k.load(wk)
	return wk.Version, aead, err
}

// Rotate starts a new data key version for scope; new secrets use it, old
// ones stay readable.
func (k *Keyring) Rotate(ctx context.Context, scope uuid.UUID) (int, error) {
	next := 1
	wk, err := k.store.CurrentDataKey(ctx, scope)
	switch {
	case err == nil:
		next = wk.Version + 1
	case !errors.Is(err, ErrNoKey):
		return 0, err
	}
	created, err := k.create(ctx, scope, next)
	return created.Version, err
}

func (k *Keyring) create(ctx context.Context, scope uuid.UUID, version int) (WrappedKey, error) {
	dek := make([]byte, keySize)
	if _, err := rand.Read(dek); err != nil {
		return WrappedKey{}, err
	}
	wk, err := k.master.wrap(scope, version, dek)
	if err != nil {
		return WrappedKey{}, err
	}
	err = k.store.InsertDataKey(ctx, wk)
	if errors.Is(err, ErrConflict) {
		// Another process created it first; use theirs.
		return k.store.DataKey(ctx, scope, version)
	}
	return wk, err
}

func (k *Keyring) version(ctx context.Context, scope uuid.UUID, version int) (cipher.AEAD, error) {
	k.mu.Lock()
	aead, ok := k.cache[cacheKey{scope, version}]
	k.mu.Unlock()
	if ok {
		return aead, nil
	}
	wk, err := k.store.DataKey(ctx, scope, version)
	if err != nil {
		return nil, err
	}
	return k.load(wk)
}

func (k *Keyring) load(wk WrappedKey) (cipher.AEAD, error) {
	ck := cacheKey{wk.Scope, wk.Version}
	k.mu.Lock()
	if aead, ok := k.cache[ck]; ok {
		k.mu.Unlock()
		return aead, nil
	}
	k.mu.Unlock()
	dek, err := k.master.unwrap(wk)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(dek)
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	k.cache[ck] = aead
	k.mu.Unlock()
	return aead, nil
}

// RewrapAll rewraps every data key under the primary master key, so older
// master keys can be retired. Secrets themselves are untouched. It returns
// how many keys changed.
func (k *Keyring) RewrapAll(ctx context.Context) (int, error) {
	keys, err := k.store.DataKeys(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, wk := range keys {
		if wk.KEKID == k.master.primary {
			continue
		}
		dek, err := k.master.unwrap(wk)
		if err != nil {
			return n, err
		}
		re, err := k.master.wrap(wk.Scope, wk.Version, dek)
		if err != nil {
			return n, err
		}
		if err := k.store.RewrapDataKey(ctx, re); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
