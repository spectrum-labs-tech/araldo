// SPDX-License-Identifier: AGPL-3.0-or-later

package authn

import (
	"hash/crc32"
	"strings"
)

// Operator keys (ADR 0031): ald_op_<32 random base62><6 base62 CRC32>, the
// shape of API keys and user tokens, for the operator API.
const OperatorKeyPrefix = "ald_op_"

// NewOperatorKey returns a new operator key.
func NewOperatorKey() string {
	body := randomBase62(apiKeyRandomLen)
	return OperatorKeyPrefix + body + base62Fixed(crc32.ChecksumIEEE([]byte(OperatorKeyPrefix+body)), apiKeyChecksumLen)
}

// IsOperatorKey checks a key's shape and checksum, with no database.
func IsOperatorKey(key string) bool {
	rest, ok := strings.CutPrefix(key, OperatorKeyPrefix)
	if !ok || len(rest) != apiKeyRandomLen+apiKeyChecksumLen {
		return false
	}
	for i := range len(rest) {
		if !strings.ContainsRune(base62, rune(rest[i])) {
			return false
		}
	}
	body, sum := rest[:apiKeyRandomLen], rest[apiKeyRandomLen:]
	return base62Fixed(crc32.ChecksumIEEE([]byte(OperatorKeyPrefix+body)), apiKeyChecksumLen) == sum
}
