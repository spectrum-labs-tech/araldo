// SPDX-License-Identifier: AGPL-3.0-or-later

// Package keyring encrypts the secrets Araldo stores (ADR 0008): OAuth
// tokens, app passwords, webhook signing secrets, TOTP secrets.
//
// Master keys live outside the database: as local keys from the
// environment, or in a key service (Transit) that never hands them out.
// Each scope (an org, or the install itself for secrets that belong to no
// org) has data keys, stored wrapped by a master key. Secrets are sealed
// with AES-256-GCM under the scope's current data key, with associated data
// naming the table, column and row they belong to, so a ciphertext copied
// elsewhere fails to open.
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
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
)

// Install is the scope of secrets that belong to no org.
var Install = uuid.Nil

// Signing is the scope holding the root of keys made by Derive. Its one
// data key is wrapped like any other, so changing master keys never
// changes it, and links signed long ago stay valid.
var Signing = uuid.MustParse("00000000-0000-0000-0000-000000000001")

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
	// ErrUnavailable means no master key is configured, or its key service
	// cannot be reached: a 503 to retry, not a broken secret. Errors that
	// wrap it say why.
	ErrUnavailable = &apperr.Error{Kind: apperr.KindUnavailable, Code: "keys_unavailable",
		Message: "Stored credentials cannot be read right now: the master keys are not configured or their key service is unreachable. Try again shortly."}
)

// unavailable wraps cause as ErrUnavailable.
func unavailable(cause error) error { return fmt.Errorf("%w: %w", ErrUnavailable, cause) }

// errNoMasterKeys is why a keyring without master keys is unavailable.
var errNoMasterKeys = errors.New("no master keys are configured")

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

// KEK is a key-encryption key: a master key that wraps data keys. aad
// names the data key, and unwrapping under a different aad must fail.
type KEK interface {
	// ID is recorded next to every data key the KEK wraps.
	ID() string
	Wrap(ctx context.Context, plaintext, aad []byte) ([]byte, error)
	Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error)
}

// localKEK is a master key held in memory, from the environment.
type localKEK struct {
	id   string
	aead cipher.AEAD
}

func (k localKEK) ID() string { return k.id }

func (k localKEK) Wrap(_ context.Context, plaintext, aad []byte) ([]byte, error) {
	return seal(k.aead, plaintext, aad)
}

func (k localKEK) Unwrap(_ context.Context, wrapped, aad []byte) ([]byte, error) {
	return open(k.aead, wrapped, aad)
}

// MasterKeys are the key-encryption keys. The first is primary: new data
// keys are wrapped with it; the others only unwrap what they wrapped
// before.
type MasterKeys struct {
	primary string
	keys    map[string]KEK
	order   []string
	// legacy is the signing root earlier versions derived from the first
	// local key, used once to seed the stored one so old links keep
	// working. Nil without a local key.
	legacy []byte
}

// NewMasterKeys returns master keys, the first primary.
func NewMasterKeys(keks ...KEK) (*MasterKeys, error) {
	m := &MasterKeys{keys: map[string]KEK{}}
	for _, k := range keks {
		kid := k.ID()
		if _, dup := m.keys[kid]; dup {
			return nil, fmt.Errorf("keyring: master key %q given twice", kid)
		}
		m.keys[kid] = k
		m.order = append(m.order, kid)
	}
	if len(m.order) == 0 {
		return nil, errors.New("keyring: no master key configured")
	}
	m.primary = m.order[0]
	return m, nil
}

// ParseMasterKeys reads local keys, "id:base64key[,id:base64key...]"
// (standard or URL base64 of 32 bytes). The first key is primary.
func ParseMasterKeys(s string) (*MasterKeys, error) {
	var keks []KEK
	var legacy []byte
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, enc, ok := strings.Cut(part, ":")
		if !ok || kid == "" {
			return nil, errors.New("keyring: master key must be id:base64")
		}
		raw, err := decodeKey(enc)
		if err != nil {
			return nil, fmt.Errorf("keyring: master key %q: %w", kid, err)
		}
		aead, err := newAEAD(raw)
		if err != nil {
			return nil, err
		}
		keks = append(keks, localKEK{id: kid, aead: aead})
		if legacy == nil {
			legacy = hmacSHA256(raw, "araldo:v1:derive")
		}
	}
	m, err := NewMasterKeys(keks...)
	if err != nil {
		return nil, err
	}
	m.legacy = legacy
	return m, nil
}

// Prepend returns these master keys with k first, as the new primary.
// Moving to a key service is Prepend, then RewrapAll, then dropping the
// local keys.
func (m *MasterKeys) Prepend(k KEK) (*MasterKeys, error) {
	keks := []KEK{k}
	for _, kid := range m.order {
		keks = append(keks, m.keys[kid])
	}
	out, err := NewMasterKeys(keks...)
	if err != nil {
		return nil, err
	}
	out.legacy = m.legacy
	return out, nil
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

func (m *MasterKeys) wrap(ctx context.Context, scope uuid.UUID, version int, dek []byte) (WrappedKey, error) {
	sealed, err := m.keys[m.primary].Wrap(ctx, dek, wrapAAD(scope, version))
	if err != nil {
		return WrappedKey{}, fmt.Errorf("keyring: wrap with master key %q: %w", m.primary, err)
	}
	return WrappedKey{Scope: scope, Version: version, KEKID: m.primary, Wrapped: sealed}, nil
}

func (m *MasterKeys) unwrap(ctx context.Context, k WrappedKey) ([]byte, error) {
	kek, ok := m.keys[k.KEKID]
	if !ok {
		return nil, fmt.Errorf("%w: master key %q is not configured", ErrNoKey, k.KEKID)
	}
	dek, err := kek.Unwrap(ctx, k.Wrapped, wrapAAD(k.Scope, k.Version))
	if errors.Is(err, ErrUnavailable) {
		return nil, fmt.Errorf("unwrap with master key %q: %w", k.KEKID, err)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: data key %s v%d under master key %q: %w", ErrDecrypt, k.Scope, k.Version, k.KEKID, err)
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

// Keyring seals and opens secrets. It works without its master keys, failing
// each operation that needs one with ErrUnavailable, so the rest of Araldo
// keeps running while a key service is down or no key is configured.
type Keyring struct {
	master *MasterKeys // nil: none configured
	store  Store
	cache  *keyCache
	sign   *signer
}

// signer holds the root of Derive, loaded on first use and shared by the
// copies With makes. A failed load is retried, but not more often than
// signRetry, so an outage does not become a call per request.
type signer struct {
	mu      sync.Mutex
	root    []byte
	err     error
	retryAt time.Time
}

const signRetry = 15 * time.Second

// keyCache holds unwrapped data keys, shared by a keyring and the copies
// With makes.
type keyCache struct {
	mu   sync.Mutex
	aead map[cacheKey]cipher.AEAD
}

type cacheKey struct {
	scope   uuid.UUID
	version int
}

// New returns a keyring using master keys m (nil when none are configured)
// and data keys in st. It reaches neither: Check does, and so does each
// operation when it needs a key.
func New(m *MasterKeys, st Store) *Keyring {
	return &Keyring{master: m, store: st, cache: &keyCache{aead: map[cacheKey]cipher.AEAD{}}, sign: &signer{}}
}

// Check reports whether secrets can be sealed and opened: the primary
// master key wraps and unwraps a probe, and the signing key loads. A nil
// error means usable; otherwise it says why.
func (k *Keyring) Check(ctx context.Context) error {
	if k.master == nil {
		return unavailable(errNoMasterKeys)
	}
	probe := make([]byte, keySize)
	wk, err := k.master.wrap(ctx, Signing, 0, probe)
	if err == nil {
		_, err = k.master.unwrap(ctx, wk)
	}
	if err != nil {
		return fmt.Errorf("keyring: master key %q: %w", k.master.primary, err)
	}
	if _, err := k.signingKey(ctx); err != nil {
		return fmt.Errorf("keyring: signing key: %w", err)
	}
	return nil
}

// signingKey returns the root of Derive, loading it when needed.
func (k *Keyring) signingKey(ctx context.Context) ([]byte, error) {
	s := k.sign
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root != nil {
		return s.root, nil
	}
	if k.master == nil {
		return nil, unavailable(errNoMasterKeys)
	}
	if time.Now().Before(s.retryAt) {
		return nil, s.err
	}
	root, err := k.signingRoot(ctx)
	if err != nil {
		s.err, s.retryAt = err, time.Now().Add(signRetry)
		return nil, err
	}
	s.root = root
	return root, nil
}

// signingRoot reads the Signing scope's data key, creating it when there
// is none: from the first local master key as earlier versions derived it,
// so links they signed stay valid, or at random.
func (k *Keyring) signingRoot(ctx context.Context) ([]byte, error) {
	wk, err := k.store.DataKey(ctx, Signing, 1)
	if errors.Is(err, ErrNoKey) {
		root := k.master.legacy
		if root == nil {
			root = make([]byte, keySize)
			if _, err := rand.Read(root); err != nil {
				return nil, err
			}
		}
		wk, err = k.master.wrap(ctx, Signing, 1, root)
		if err != nil {
			return nil, err
		}
		err = k.store.InsertDataKey(ctx, wk)
		if errors.Is(err, ErrConflict) {
			// Another process created it first; use theirs.
			wk, err = k.store.DataKey(ctx, Signing, 1)
		}
	}
	if err != nil {
		return nil, err
	}
	return k.master.unwrap(ctx, wk)
}

// With returns this keyring reading and writing data keys through st,
// sharing its cache. Inside a database transaction, use With(tx): the
// keyring's own store would wait for another pooled connection while the
// transaction holds one, and enough of those at once exhaust the pool.
func (k *Keyring) With(st Store) *Keyring {
	return &Keyring{master: k.master, store: st, cache: k.cache, sign: k.sign}
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

// Derive returns a 32-byte key for one purpose (label): for signing, not
// for encrypting stored data. It comes from the stored signing root, so it
// survives master key changes. It fails while the root cannot be loaded.
func (k *Keyring) Derive(ctx context.Context, label string) ([]byte, error) {
	root, err := k.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	return hmacSHA256(root, label), nil
}

func hmacSHA256(key []byte, label string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(label))
	return mac.Sum(nil)
}

// Forget drops a scope's cached data keys (after the scope is deleted).
func (k *Keyring) Forget(scope uuid.UUID) {
	k.cache.mu.Lock()
	defer k.cache.mu.Unlock()
	for ck := range k.cache.aead {
		if ck.scope == scope {
			delete(k.cache.aead, ck)
		}
	}
}

func (k *Keyring) current(ctx context.Context, scope uuid.UUID) (int, cipher.AEAD, error) {
	if k.master == nil {
		return 0, nil, unavailable(errNoMasterKeys)
	}
	wk, err := k.store.CurrentDataKey(ctx, scope)
	if errors.Is(err, ErrNoKey) {
		wk, err = k.create(ctx, scope, 1)
	}
	if err != nil {
		return 0, nil, err
	}
	aead, err := k.load(ctx, wk)
	return wk.Version, aead, err
}

// Rotate starts a new data key version for scope; new secrets use it, old
// ones stay readable.
func (k *Keyring) Rotate(ctx context.Context, scope uuid.UUID) (int, error) {
	if k.master == nil {
		return 0, unavailable(errNoMasterKeys)
	}
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
	wk, err := k.master.wrap(ctx, scope, version, dek)
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
	k.cache.mu.Lock()
	aead, ok := k.cache.aead[cacheKey{scope, version}]
	k.cache.mu.Unlock()
	if ok {
		return aead, nil
	}
	wk, err := k.store.DataKey(ctx, scope, version)
	if err != nil {
		return nil, err
	}
	return k.load(ctx, wk)
}

func (k *Keyring) load(ctx context.Context, wk WrappedKey) (cipher.AEAD, error) {
	ck := cacheKey{wk.Scope, wk.Version}
	k.cache.mu.Lock()
	if aead, ok := k.cache.aead[ck]; ok {
		k.cache.mu.Unlock()
		return aead, nil
	}
	k.cache.mu.Unlock()
	if k.master == nil {
		return nil, unavailable(errNoMasterKeys)
	}
	dek, err := k.master.unwrap(ctx, wk)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(dek)
	if err != nil {
		return nil, err
	}
	k.cache.mu.Lock()
	k.cache.aead[ck] = aead
	k.cache.mu.Unlock()
	return aead, nil
}

// RewrapAll rewraps every data key under the primary master key, so older
// master keys can be retired. Secrets themselves are untouched. It returns
// how many keys changed.
func (k *Keyring) RewrapAll(ctx context.Context) (int, error) {
	if k.master == nil {
		return 0, unavailable(errNoMasterKeys)
	}
	keys, err := k.store.DataKeys(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, wk := range keys {
		if wk.KEKID == k.master.primary {
			continue
		}
		dek, err := k.master.unwrap(ctx, wk)
		if err != nil {
			return n, err
		}
		re, err := k.master.wrap(ctx, wk.Scope, wk.Version, dek)
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

// KeyUse is how many stored data keys one master key wraps.
type KeyUse struct {
	ID string
	// Configured is false for a master key that wraps data keys but is not
	// configured: those keys cannot be read.
	Configured bool
	Primary    bool
	DataKeys   int
}

// Usage reports every configured master key, primary first, and any
// unconfigured one still recorded on a data key. A configured key other
// than the primary that wraps nothing can be removed.
func (k *Keyring) Usage(ctx context.Context) ([]KeyUse, error) {
	keys, err := k.store.DataKeys(ctx)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, wk := range keys {
		counts[wk.KEKID]++
	}
	var out []KeyUse
	var order []string
	if k.master != nil {
		order = k.master.order
	}
	for _, kid := range order {
		out = append(out, KeyUse{ID: kid, Configured: true, Primary: kid == order[0], DataKeys: counts[kid]})
		delete(counts, kid)
	}
	for _, kid := range slices.Sorted(maps.Keys(counts)) {
		out = append(out, KeyUse{ID: kid, DataKeys: counts[kid]})
	}
	return out, nil
}
