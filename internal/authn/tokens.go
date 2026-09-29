// SPDX-License-Identifier: AGPL-3.0-or-later

package authn

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"hash/crc32"
	"math/big"
	"strings"
)

// NewToken returns a random 256-bit token (for sessions, CSRF, email
// links) and its SHA-256 hash, which is what gets stored.
func NewToken() (token string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand never fails on supported platforms
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken is the stored form of a high-entropy token or key.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func randomBase62(n int) string {
	var sb strings.Builder
	sb.Grow(n)
	limit := big.NewInt(int64(len(base62)))
	for range n {
		i, err := rand.Int(rand.Reader, limit)
		if err != nil {
			panic(err) // crypto/rand never fails on supported platforms
		}
		sb.WriteByte(base62[i.Int64()])
	}
	return sb.String()
}

func base62Fixed(v uint32, width int) string {
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		out[i] = base62[v%62]
		v /= 62
	}
	return string(out)
}

// API keys (ADR 0006): ald_<mode>_<32 random base62><6 base62 CRC32>.
const (
	apiKeyRandomLen   = 32
	apiKeyChecksumLen = 6
	LivePrefix        = "ald_live_"
	TestPrefix        = "ald_test_"
)

// ErrMalformedKey means a string is not an Araldo API key.
var ErrMalformedKey = errors.New("malformed API key")

// NewAPIKey returns a new key for the given mode.
func NewAPIKey(live bool) string {
	prefix := TestPrefix
	if live {
		prefix = LivePrefix
	}
	body := randomBase62(apiKeyRandomLen)
	return prefix + body + base62Fixed(crc32.ChecksumIEEE([]byte(prefix+body)), apiKeyChecksumLen)
}

// ParseAPIKey checks a key's shape and checksum (no database needed, so
// secret scanners can do the same) and reports its mode.
func ParseAPIKey(key string) (live bool, err error) {
	var rest string
	switch {
	case strings.HasPrefix(key, LivePrefix):
		live, rest = true, key[len(LivePrefix):]
	case strings.HasPrefix(key, TestPrefix):
		rest = key[len(TestPrefix):]
	default:
		return false, ErrMalformedKey
	}
	if len(rest) != apiKeyRandomLen+apiKeyChecksumLen {
		return false, ErrMalformedKey
	}
	for i := range len(rest) {
		if !strings.ContainsRune(base62, rune(rest[i])) {
			return false, ErrMalformedKey
		}
	}
	body, sum := rest[:apiKeyRandomLen], rest[apiKeyRandomLen:]
	prefix := key[:len(key)-len(rest)]
	if base62Fixed(crc32.ChecksumIEEE([]byte(prefix+body)), apiKeyChecksumLen) != sum {
		return false, ErrMalformedKey
	}
	return live, nil
}

// KeyHint is what is shown of a key after creation: its prefix and last
// four characters.
func KeyHint(key string) string {
	if len(key) < 4 {
		return "…"
	}
	i := strings.LastIndex(key[:min(len(key), 9)], "_")
	return key[:i+1] + "…" + key[len(key)-4:]
}

// Recovery codes: 16 characters of lowercase Crockford base32 (80 bits),
// shown as four groups. That is enough entropy for a fast hash.
const recoveryAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// NewRecoveryCodes returns n new codes.
func NewRecoveryCodes(n int) []string {
	codes := make([]string, n)
	limit := big.NewInt(int64(len(recoveryAlphabet)))
	for i := range codes {
		var sb strings.Builder
		for j := range 16 {
			if j > 0 && j%4 == 0 {
				sb.WriteByte('-')
			}
			k, err := rand.Int(rand.Reader, limit)
			if err != nil {
				panic(err)
			}
			sb.WriteByte(recoveryAlphabet[k.Int64()])
		}
		codes[i] = sb.String()
	}
	return codes
}

// NormalizeRecoveryCode lowercases a code and drops separators and spaces,
// so a code typed any reasonable way hashes the same.
func NormalizeRecoveryCode(code string) string {
	code = strings.ToLower(code)
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, code)
}
