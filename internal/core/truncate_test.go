// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateKeepsCharactersWhole(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly", 7, "exactly"},
		{"abcdef", 3, "abc"},
		{"créé", 3, "cr"},    // é is two bytes: not cut in half
		{"日本語のエラー", 7, "日本"}, // three-byte characters
		{"🚀🚀", 5, "🚀"},
		{"", 0, ""},
	}
	for _, tt := range tests {
		got := truncate(tt.in, tt.n)
		if got != tt.want || !utf8.ValidString(got) || len(got) > tt.n {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}
