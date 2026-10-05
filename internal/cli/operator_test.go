// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"testing"
)

func TestParseLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    [4]int // -1 is none
		wantErr bool
	}{
		{"", [4]int{-1, -1, -1, -1}, false},
		{"brands=3,channels=10,members=5,posts_per_month=200", [4]int{3, 10, 5, 200}, false},
		{" brands=0 , posts_per_month=none", [4]int{0, -1, -1, -1}, false},
		{"brands=-1", [4]int{}, true},
		{"brands", [4]int{}, true},
		{"seats=3", [4]int{}, true},
		{"channels=many", [4]int{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			l, err := parseLimits(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseLimits(%q) err = %v", tt.in, err)
			}
			if tt.wantErr {
				return
			}
			for i, p := range []*int{l.Brands, l.Channels, l.Members, l.PostsMonth} {
				got := -1
				if p != nil {
					got = *p
				}
				if got != tt.want[i] {
					t.Errorf("limit %d = %d, want %d", i, got, tt.want[i])
				}
			}
		})
	}
}
