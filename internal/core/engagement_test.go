// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestNextReading(t *testing.T) {
	t.Parallel()
	pub := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		now  time.Duration // after publishing
		want time.Duration // after publishing; 0: none
	}{
		{0, time.Hour},
		{time.Hour, 6 * time.Hour}, // read exactly on time: the next one
		{2 * time.Hour, 6 * time.Hour},
		{25 * time.Hour, 3 * 24 * time.Hour},
		{29 * 24 * time.Hour, 30 * 24 * time.Hour},
		{30 * 24 * time.Hour, 0},
		{400 * 24 * time.Hour, 0}, // published long before engagement existed
	}
	for _, tt := range tests {
		got := nextReading(pub, pub.Add(tt.now))
		switch {
		case tt.want == 0 && got != nil:
			t.Errorf("at +%s: next %s, want none", tt.now, got.Sub(pub))
		case tt.want != 0 && (got == nil || !got.Equal(pub.Add(tt.want))):
			t.Errorf("at +%s: next %v, want +%s", tt.now, got, tt.want)
		}
	}
}

func TestThreadCounts(t *testing.T) {
	t.Parallel()
	parts := []platform.RemoteRef{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	counts := map[string]platform.Counts{
		"a": {Likes: 10, Reposts: 2, Replies: 4}, // one reply is our part b
		"b": {Likes: 3, Replies: 1},              // ...and one is our part c
		"c": {Likes: 1, Quotes: 1},
	}
	got, found := threadCounts(parts, counts)
	if !found || got != (platform.Counts{Likes: 14, Reposts: 2, Replies: 3, Quotes: 1}) {
		t.Fatalf("thread = %+v (found %v), want replies less the thread's own two", got, found)
	}
	if _, found := threadCounts(parts, map[string]platform.Counts{"b": {Likes: 1}}); found {
		t.Fatal("a thread whose first part was deleted counted as found")
	}
	if _, found := threadCounts(nil, counts); found {
		t.Fatal("a target with no parts on record counted as found")
	}
	// A single post's replies are all other people's.
	if got, _ := threadCounts(parts[:1], counts); got.Replies != 4 {
		t.Fatalf("single post replies %d, want 4", got.Replies)
	}
	// The thread's own replies never push the count below zero.
	if got, _ := threadCounts(parts[:2], map[string]platform.Counts{"a": {}, "b": {}}); got.Replies != 0 {
		t.Fatalf("replies %d, want 0", got.Replies)
	}
}
