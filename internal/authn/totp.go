// SPDX-License-Identifier: AGPL-3.0-or-later

package authn

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP is defined over HMAC-SHA1; authenticator apps expect it
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters (RFC 6238 defaults, which every authenticator app
// supports).
const (
	TOTPPeriod  = 30 * time.Second
	TOTPDigits  = 6
	totpSkew    = 1 // steps of clock drift accepted either side
	totpSecretN = 20
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit TOTP secret.
func NewTOTPSecret() ([]byte, error) {
	s := make([]byte, totpSecretN)
	_, err := rand.Read(s)
	return s, err
}

// TOTPSecretText is the secret as typed into an authenticator app.
func TOTPSecretText(secret []byte) string { return b32.EncodeToString(secret) }

// TOTPURI is the otpauth:// URI shown as a QR code.
func TOTPURI(issuer, account string, secret []byte) string {
	v := url.Values{}
	v.Set("secret", TOTPSecretText(secret))
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprint(TOTPDigits))
	v.Set("period", fmt.Sprint(int(TOTPPeriod/time.Second)))
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

// TOTPStep is the time step containing t.
func TOTPStep(t time.Time) int64 { return t.Unix() / int64(TOTPPeriod/time.Second) }

// TOTPCode is the code for a time step.
func TOTPCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step)) //nolint:gosec // steps are positive
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", TOTPDigits, v%1_000_000)
}

// VerifyTOTP checks code at now, allowing one step of clock drift, and
// refuses any step at or before lastStep so a code cannot be used twice. It
// returns the matched step, which the caller stores as the new lastStep.
func VerifyTOTP(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	for d := -totpSkew; d <= totpSkew; d++ {
		step := cur + int64(d)
		if step <= lastStep {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(TOTPCode(secret, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}
