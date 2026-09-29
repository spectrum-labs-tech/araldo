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
	"strings"
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
	sum := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, hashLen)
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
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, uint32(len(sum))) //nolint:gosec // length of our own 32-byte hash
	if subtle.ConstantTimeCompare(got, sum) != 1 {
		return false, false, nil
	}
	return true, p.Memory < DefaultArgon2.Memory || p.Time < DefaultArgon2.Time, nil
}

// dummyHash is verified against when an account does not exist, so sign-in
// takes the same time either way.
var dummyHash, _ = HashPassword("araldo-timing-equalizer")

// SpendPasswordTime does the work of one password check and discards it.
func SpendPasswordTime(pw string) {
	_, _, _ = VerifyPassword(pw, dummyHash)
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
	if p.Memory == 0 || p.Time == 0 || p.Threads == 0 || p.Memory > 1<<21 || p.Time > 64 {
		return Argon2Params{}, nil, nil, ErrBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Argon2Params{}, nil, nil, ErrBadHash
	}
	sum, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(sum) < 16 {
		return Argon2Params{}, nil, nil, ErrBadHash
	}
	return p, salt, sum, nil
}
