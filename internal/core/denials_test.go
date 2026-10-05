// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strconv"
	"testing"
	"time"
)

// TestDenialsArePaced checks one actor's denials of one operation are
// recorded once a minute, others' at once, and the memory stays bounded.
func TestDenialsArePaced(t *testing.T) {
	t.Parallel()
	var d denials
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	steps := []struct {
		key  string
		at   time.Duration
		want bool
	}{
		{"org|key1|GET /v1/api_keys", 0, true},
		{"org|key1|GET /v1/api_keys", 10 * time.Second, false},
		{"org|key1|POST /v1/api_keys", 10 * time.Second, true},
		{"org|key2|GET /v1/api_keys", 20 * time.Second, true},
		{"org|key1|GET /v1/api_keys", 61 * time.Second, true},
	}
	for _, s := range steps {
		if got := d.due(s.key, now.Add(s.at)); got != s.want {
			t.Errorf("%s at +%s: due %t, want %t", s.key, s.at, got, s.want)
		}
	}
	later := now.Add(time.Hour)
	for i := range 10001 {
		d.due("org|k|op"+strconv.Itoa(i), later.Add(-2*time.Minute))
	}
	d.due("org|k|fresh", later)
	if n := len(d.last); n > 2 {
		t.Fatalf("%d remembered after forgetting the old", n)
	}
}
