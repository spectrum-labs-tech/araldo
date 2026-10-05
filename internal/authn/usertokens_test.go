// SPDX-License-Identifier: AGPL-3.0-or-later

package authn

import (
	"strings"
	"testing"
)

func TestUserTokens(t *testing.T) {
	t.Parallel()
	tok := NewUserToken()
	if !strings.HasPrefix(tok, "ald_user_") || len(tok) != len("ald_user_")+38 || !IsUserToken(tok) {
		t.Fatalf("NewUserToken() = %q", tok)
	}
	if NewUserToken() == tok {
		t.Fatal("two tokens are the same")
	}
	for _, bad := range []string{
		"", "ald_user_", tok[:len(tok)-1], tok + "x",
		tok[:20] + "!" + tok[21:], // not base62
		tok[:len(tok)-1] + string("0123456789"[(strings.IndexByte("0123456789", tok[len(tok)-1])+1)%10]), // checksum
		strings.Replace(tok, "ald_user_", "ald_test_", 1),                                                // a key's prefix
	} {
		if IsUserToken(bad) {
			t.Errorf("IsUserToken(%q) = true", bad)
		}
	}
}

func TestUserCodes(t *testing.T) {
	t.Parallel()
	c := NewUserCode()
	if len(c) != 9 || c[4] != '-' || NormalizeUserCode(c) != c {
		t.Fatalf("NewUserCode() = %q", c)
	}
	for in, want := range map[string]string{
		"bdfg-hjkl":   "BDFG-HJKL",
		"BDFGHJKL":    "BDFG-HJKL",
		" bdfg hjkl ": "BDFG-HJKL",
		"BDFG-HJK":    "",
		"BDFG-HJKLM":  "",
		"ABCD-EFGH":   "", // vowels are never in a code
		"BDF1-HJKL":   "",
		"":            "",
	} {
		if got := NormalizeUserCode(in); got != want {
			t.Errorf("NormalizeUserCode(%q) = %q, want %q", in, got, want)
		}
	}
}
