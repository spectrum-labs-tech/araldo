// SPDX-License-Identifier: AGPL-3.0-or-later

package authn

import (
	"crypto/rand"
	"hash/crc32"
	"math/big"
	"strings"
)

// User tokens (ADR 0028): ald_user_<32 random base62><6 base62 CRC32>, the
// same shape as API keys, so secret scanners find them the same way.
const UserTokenPrefix = "ald_user_"

// NewUserToken returns a new user token.
func NewUserToken() string {
	body := randomBase62(apiKeyRandomLen)
	return UserTokenPrefix + body + base62Fixed(crc32.ChecksumIEEE([]byte(UserTokenPrefix+body)), apiKeyChecksumLen)
}

// IsUserToken checks a token's shape and checksum, with no database.
func IsUserToken(token string) bool {
	rest, ok := strings.CutPrefix(token, UserTokenPrefix)
	if !ok || len(rest) != apiKeyRandomLen+apiKeyChecksumLen {
		return false
	}
	for i := range len(rest) {
		if !strings.ContainsRune(base62, rune(rest[i])) {
			return false
		}
	}
	body, sum := rest[:apiKeyRandomLen], rest[apiKeyRandomLen:]
	return base62Fixed(crc32.ChecksumIEEE([]byte(UserTokenPrefix+body)), apiKeyChecksumLen) == sum
}

// User codes are what a person types to approve a device (RFC 8628 §6.1):
// eight letters from an alphabet without vowels (so no words) or easily
// confused letters, shown as two groups of four. 20^8 is about 2.6e10,
// plenty for a code that lives 15 minutes and is rate limited.
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

// NewUserCode returns a code like "BDFG-HJKL".
func NewUserCode() string {
	limit := big.NewInt(int64(len(userCodeAlphabet)))
	var sb strings.Builder
	for i := range 8 {
		if i == 4 {
			sb.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			panic(err) // crypto/rand never fails on supported platforms
		}
		sb.WriteByte(userCodeAlphabet[n.Int64()])
	}
	return sb.String()
}

// NormalizeUserCode reads a code as typed: any case, with or without the
// dash or spaces. It returns "" for something that cannot be a code.
func NormalizeUserCode(code string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch {
		case r == '-' || r == ' ':
		case strings.ContainsRune(userCodeAlphabet, r):
			sb.WriteRune(r)
		default:
			return ""
		}
	}
	s := sb.String()
	if len(s) != 8 {
		return ""
	}
	return s[:4] + "-" + s[4:]
}
