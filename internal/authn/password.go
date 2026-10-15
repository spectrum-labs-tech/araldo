// SPDX-License-Identifier: AGPL-3.0-or-later

// Package authn holds the primitives of signing in (ADR 0007): password
// hashing, TOTP, recovery codes, session tokens and API keys. It knows
// nothing about users or storage.
package authn

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password length limits (characters). NIST SP 800-63B: no composition
// rules; length is what matters.
const (
	MinPasswordLen = 12
	MaxPasswordLen = 128
)

// Errors.
var (
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	ErrPasswordTooLong  = fmt.Errorf("password must be at most %d characters", MaxPasswordLen)
	ErrBadHash          = errors.New("authn: malformed password hash")
)

// Argon2Params are argon2id cost parameters; they are stored in every hash
// so they can be raised without invalidating old ones.
type Argon2Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

// DefaultArgon2 follows OWASP's recommended minimum for argon2id.
var DefaultArgon2 = Argon2Params{Memory: 19 * 1024, Time: 2, Threads: 1}

const (
	saltLen = 16
	hashLen = 32
)

// Bounds a stored hash's parameters must keep, so a hash written into the
// database by mistake or malice cannot make one check take gigabytes or
// minutes. Every hash Araldo has made is well inside them: 19 MiB, two
// passes, one thread, a 16-byte salt and a 32-byte sum.
const (
	maxMemory  = 1 << 20 // KiB: 1 GiB
	maxTime    = 64
	maxThreads = 64
	minSaltLen = 16
	minSumLen  = 16
	maxSumLen  = 64
)

// argonSlots bounds how many argon2 computations run at once. Each holds
// its memory (19 MiB by default) for its whole run, so a burst of sign-ins
// would otherwise take as much memory as it has requests; past the number
// of CPUs they only queue for the processor anyway.
var argonSlots = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)))

// argonKey is argon2.IDKey, waiting for a free slot first.
func argonKey(pw, salt []byte, p Argon2Params, n uint32) []byte {
	argonSlots <- struct{}{}
	defer func() { <-argonSlots }()
	return argon2.IDKey(pw, salt, p.Time, p.Memory, p.Threads, n)
}

// ValidatePassword checks a new password's length.
func ValidatePassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	switch {
	case n < MinPasswordLen:
		return ErrPasswordTooShort
	case n > MaxPasswordLen:
		return ErrPasswordTooLong
	}
	return nil
}

// HashPassword returns an argon2id hash in PHC string format.
func HashPassword(pw string) (string, error) {
	return hashWith(pw, DefaultArgon2)
}

func hashWith(pw string, p Argon2Params) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argonKey([]byte(pw), salt, p, hashLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword reports whether pw matches encoded, and whether the hash
// uses weaker parameters than today's and should be replaced.
func VerifyPassword(pw, encoded string) (ok, rehash bool, err error) {
	p, salt, sum, err := parseHash(encoded)
	if err != nil {
		return false, false, err
	}
	got := argonKey([]byte(pw), salt, p, uint32(len(sum))) //nolint:gosec // parseHash bounds it to maxSumLen
	if subtle.ConstantTimeCompare(got, sum) != 1 {
		return false, false, nil
	}
	return true, p.Memory < DefaultArgon2.Memory || p.Time < DefaultArgon2.Time, nil
}

// dummyHash is verified against when an account does not exist, so sign-in
// takes the same time either way. It is made on first use, not on every
// start of every command, which may never check a password.
var dummyHash = sync.OnceValue(func() string {
	h, _ := HashPassword("araldo-timing-equalizer")
	return h
})

// SpendPasswordTime does the work of one password check and discards it.
func SpendPasswordTime(pw string) {
	_, _, _ = VerifyPassword(pw, dummyHash())
}

func parseHash(encoded string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Argon2Params{}, nil, nil, ErrBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Argon2Params{}, nil, nil, ErrBadHash
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return Argon2Params{}, nil, nil, ErrBadHash
	}
	if p.Memory == 0 || p.Time == 0 || p.Threads == 0 || p.Memory > maxMemory || p.Time > maxTime || p.Threads > maxThreads {
		return Argon2Params{}, nil, nil, ErrBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < minSaltLen {
		return Argon2Params{}, nil, nil, ErrBadHash
	}
	sum, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(sum) < minSumLen || len(sum) > maxSumLen {
		return Argon2Params{}, nil, nil, ErrBadHash
	}
	return p, salt, sum, nil
}
