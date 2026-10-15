// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"strings"
	"testing"
)

func TestSummaryLimit(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ in, want int }{{-1, 20}, {0, 20}, {1, 1}, {100, 100}, {101, 100}, {1 << 30, 100}} {
		if got := summaryLimit(tt.in); got != tt.want {
			t.Errorf("summaryLimit(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// TestPrefixedPostCols checks the prefixed column list names the same
// columns as postCols, each prefixed once.
func TestPrefixedPostCols(t *testing.T) {
	t.Parallel()
	plain, prefixed := strings.Split(postCols, ", "), strings.Split(prefixedPostCols, ", ")
	if len(plain) != len(prefixed) || len(plain) != len(postColumns) {
		t.Fatalf("%d columns, %d prefixed, %d listed", len(plain), len(prefixed), len(postColumns))
	}
	for i, c := range plain {
		if prefixed[i] != "p."+c || strings.ContainsAny(c, " \n\t.") {
			t.Errorf("column %d: %q, prefixed %q", i, c, prefixed[i])
		}
	}
}
