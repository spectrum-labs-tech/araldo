// SPDX-License-Identifier: AGPL-3.0-or-later

// Package id makes and reads Araldo's public identifiers (ADR 0005): a type
// prefix and a UUIDv7 in lowercase Crockford base32, the TypeID format
// (https://github.com/jetify-com/typeid/tree/main/spec). Postgres stores
// the UUID; the prefix exists only at the edges, so an ID of the wrong type
// is refused before it reaches a query.
package id

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Prefix names the type of object an ID refers to.
type Prefix string

// Object prefixes.
const (
	Org             Prefix = "org"
	Brand           Prefix = "brand"
	User            Prefix = "user"
	Channel         Prefix = "chan"
	Template        Prefix = "tmpl"
	Post            Prefix = "post"
	Target          Prefix = "ptgt"
	Event           Prefix = "evt"
	WebhookEndpoint Prefix = "whep"
	Delivery        Prefix = "whdel"
	APIKey          Prefix = "key"
	Request         Prefix = "req"
	Slot            Prefix = "slot"
	Media           Prefix = "media"
)

// ErrInvalid is returned for a malformed ID or one of another type.
var ErrInvalid = errors.New("invalid id")

const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

var decoding = func() (d [256]byte) {
	for i := range d {
		d[i] = 0xff
	}
	for i := range len(alphabet) {
		d[alphabet[i]] = byte(i)
	}
	return d
}()

// New returns a new time-ordered UUID (version 7).
func New() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// Make returns a new ID with prefix p.
func Make(p Prefix) string { return Format(p, New()) }

// Format renders u as an ID with prefix p.
func Format(p Prefix, u uuid.UUID) string {
	return string(p) + "_" + encode(u)
}

// Parse reads an ID that must carry prefix p.
func Parse(p Prefix, s string) (uuid.UUID, error) {
	rest, ok := strings.CutPrefix(s, string(p)+"_")
	if !ok {
		return uuid.Nil, fmt.Errorf("%w: %q is not a %s ID", ErrInvalid, s, p)
	}
	u, err := decode(rest)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %q: %w", ErrInvalid, s, err)
	}
	return u, nil
}

// encode writes the 128 bits of u as 26 base32 characters, most
// significant first; the first character carries only the top 3 bits.
func encode(u uuid.UUID) string {
	var out [26]byte
	var acc uint16
	bits := 2 // 130 bits of output for 128 of input: two zero bits lead
	pos := 0
	for _, b := range u {
		acc = acc<<8 | uint16(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[pos] = alphabet[(acc>>bits)&31]
			pos++
		}
	}
	return string(out[:])
}

func decode(s string) (uuid.UUID, error) {
	if len(s) != 26 {
		return uuid.Nil, errors.New("suffix must be 26 characters")
	}
	if s[0] > '7' {
		return uuid.Nil, errors.New("suffix overflows 128 bits")
	}
	var u uuid.UUID
	var acc uint16
	bits := -2 // drop the two leading zero bits
	pos := 0
	for i := range len(s) {
		v := decoding[s[i]]
		if v == 0xff {
			return uuid.Nil, fmt.Errorf("invalid character %q", s[i])
		}
		acc = acc<<5 | uint16(v)
		bits += 5
		if bits >= 8 {
			bits -= 8
			u[pos] = byte(acc >> bits) //nolint:gosec // keeps the low 8 bits on purpose
			pos++
		}
	}
	return u, nil
}
