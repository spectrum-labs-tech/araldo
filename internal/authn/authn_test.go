// SPDX-License-Identifier: AGPL-3.0-or-later

package authn

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashing(t *testing.T) {
	t.Parallel()
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash %q does not carry its parameters", h)
	}
	ok, rehash, err := VerifyPassword("correct horse battery staple", h)
	if err != nil || !ok || rehash {
		t.Fatalf("Verify(right) = %v, %v, %v", ok, rehash, err)
	}
	if ok, _, _ := VerifyPassword("correct horse battery stapler", h); ok {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery staple")
	if h == h2 {
		t.Fatal("two hashes of one password are equal: salt missing")
	}
}

func TestWeakHashAsksForRehash(t *testing.T) {
	t.Parallel()
	weak, err := hashWith("a long enough password", Argon2Params{Memory: 1024, Time: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	ok, rehash, err := VerifyPassword("a long enough password", weak)
	if err != nil || !ok || !rehash {
		t.Fatalf("Verify(weak) = %v, %v, %v; want ok and rehash", ok, rehash, err)
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	t.Parallel()
	for _, h := range []string{
		"", "plain", "$argon2i$v=19$m=1,t=1,p=1$AAAA$AAAA",
		"$argon2id$v=19$m=0,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=99999999,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if _, _, err := VerifyPassword("x", h); !errors.Is(err, ErrBadHash) {
			t.Errorf("VerifyPassword(%q) err = %v, want ErrBadHash", h, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pw   string
		want error
	}{
		{"short", ErrPasswordTooShort},
		{"exactly12chr", nil},
		{strings.Repeat("é", 12), nil}, // counted in characters, not bytes
		{strings.Repeat("a", 129), ErrPasswordTooLong},
	}
	for _, tt := range tests {
		if err := ValidatePassword(tt.pw); !errors.Is(err, tt.want) {
			t.Errorf("ValidatePassword(%d chars) = %v, want %v", len([]rune(tt.pw)), err, tt.want)
		}
	}
}

// RFC 6238 appendix B vectors (SHA-1), truncated to six digits.
func TestTOTPMatchesRFC6238(t *testing.T) {
	t.Parallel()
	secret := []byte("12345678901234567890")
	tests := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, tt := range tests {
		if got := TOTPCode(secret, TOTPStep(time.Unix(tt.unix, 0))); got != tt.want {
			t.Errorf("TOTP at %d = %s, want %s", tt.unix, got, tt.want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	t.Parallel()
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	cur := TOTPStep(now)
	tests := []struct {
		name     string
		code     string
		lastStep int64
		wantOK   bool
	}{
		{"current", TOTPCode(secret, cur), 0, true},
		{"with spaces", TOTPCode(secret, cur)[:3] + " " + TOTPCode(secret, cur)[3:], 0, true},
		{"previous step (drift)", TOTPCode(secret, cur-1), 0, true},
		{"next step (drift)", TOTPCode(secret, cur+1), 0, true},
		{"two steps old", TOTPCode(secret, cur-2), 0, false},
		{"replayed", TOTPCode(secret, cur), cur, false},
		{"wrong length", "12345", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			step, ok := VerifyTOTP(secret, tt.code, now, tt.lastStep)
			if ok != tt.wantOK {
				t.Fatalf("VerifyTOTP ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && step <= tt.lastStep {
				t.Fatalf("step %d not after lastStep %d", step, tt.lastStep)
			}
		})
	}
}

func TestTOTPURI(t *testing.T) {
	t.Parallel()
	uri := TOTPURI("Araldo", "ada@example.com", []byte("12345678901234567890"))
	for _, want := range []string{"otpauth://totp/Araldo:ada@example.com?", "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "issuer=Araldo", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI %q lacks %q", uri, want)
		}
	}
}

func TestAPIKeys(t *testing.T) {
	t.Parallel()
	live, test := NewAPIKey(true), NewAPIKey(false)
	if !strings.HasPrefix(live, "ald_live_") || !strings.HasPrefix(test, "ald_test_") {
		t.Fatalf("prefixes: %q %q", live, test)
	}
	if len(live) != len("ald_live_")+38 {
		t.Fatalf("live key length %d", len(live))
	}
	if m, err := ParseAPIKey(live); err != nil || !m {
		t.Fatalf("ParseAPIKey(live) = %v, %v", m, err)
	}
	if m, err := ParseAPIKey(test); err != nil || m {
		t.Fatalf("ParseAPIKey(test) = %v, %v", m, err)
	}
	flipped := []byte(live)
	if flipped[12] == 'A' {
		flipped[12] = 'B'
	} else {
		flipped[12] = 'A'
	}
	for _, bad := range []string{"", "sk_live_abc", live[:len(live)-1], string(flipped), "ald_live_" + strings.Repeat("!", 38)} {
		if _, err := ParseAPIKey(bad); !errors.Is(err, ErrMalformedKey) {
			t.Errorf("ParseAPIKey(%q) = %v, want ErrMalformedKey", bad, err)
		}
	}
	if h := KeyHint(live); !strings.HasPrefix(h, "ald_live_…") || !strings.HasSuffix(h, live[len(live)-4:]) {
		t.Errorf("KeyHint = %q", h)
	}
}

func TestTokens(t *testing.T) {
	t.Parallel()
	a, ha := NewToken()
	b, _ := NewToken()
	if a == b || len(a) != 43 {
		t.Fatalf("tokens %q %q", a, b)
	}
	if string(HashToken(a)) != string(ha) {
		t.Fatal("HashToken disagrees with NewToken")
	}
}

func TestRecoveryCodes(t *testing.T) {
	t.Parallel()
	codes := NewRecoveryCodes(10)
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 19 || strings.Count(c, "-") != 3 {
			t.Fatalf("code %q has the wrong shape", c)
		}
		if seen[c] {
			t.Fatalf("duplicate code %q", c)
		}
		seen[c] = true
	}
	if NormalizeRecoveryCode(" ABCD-efgh-1234-5678 ") != "abcdefgh12345678" {
		t.Fatalf("normalize = %q", NormalizeRecoveryCode(" ABCD-efgh-1234-5678 "))
	}
}
