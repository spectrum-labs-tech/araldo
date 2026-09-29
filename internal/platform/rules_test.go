// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"strings"
	"testing"
)

func rulesOf(t *testing.T, p Provider) Rules {
	t.Helper()
	r, ok := RulesFor(p)
	if !ok {
		t.Fatalf("no rules for %s", p)
	}
	return r
}

func TestLength(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider Provider
		text     string
		want     int
	}{
		{Bluesky, "hello", 5},
		// A link counts as its short form: "see x.dev/a." and "see example.com/a/very/long/...".
		{Bluesky, "see https://x.dev/a.", 12},
		{Bluesky, "see https://example.com/a/very/long/path?utm_source=bluesky", 4 + 27},
		{Bluesky, "👍🏽", 1}, // emoji with a skin-tone modifier is one grapheme
		{Bluesky, "é", 1},
		{Telegram, "👍🏽", 2}, // but two code points
		{X, "hello", 5},
		{X, "日本", 4}, // CJK weighs 2
		{X, "👍🏽", 2},
		{X, "see https://example.com/a/very/long/path/that/goes/on", 4 + 23},
		{Mastodon, "see https://example.com/a/very/long/path", 4 + 23},
		{LinkedIn, "naïve", 5},
	}
	for _, tt := range tests {
		if got := rulesOf(t, tt.provider).Length(tt.text); got != tt.want {
			t.Errorf("%s Length(%q) = %d, want %d", tt.provider, tt.text, got, tt.want)
		}
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	x := rulesOf(t, X)
	codes := func(vs []Violation) string {
		var out []string
		for _, v := range vs {
			out = append(out, v.Code)
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		name  string
		r     Rules
		parts []string
		media int
		want  string
	}{
		{"fits", x, []string{"hello"}, 0, ""},
		{"empty", x, []string{"  "}, 0, "empty"},
		{"too long", x, []string{strings.Repeat("a", 281)}, 0, "too_long"},
		{"instagram needs media", rulesOf(t, Instagram), []string{"hi"}, 0, "media_required"},
		{"telegram has no threads", rulesOf(t, Telegram), []string{"a", "b"}, 0, "threads_unsupported"},
		{"too much media", x, []string{"hi"}, 5, "too_much_media"},
	}
	for _, tt := range tests {
		if got := codes(tt.r.Check(tt.parts, tt.media)); got != tt.want {
			t.Errorf("%s: violations %q, want %q", tt.name, got, tt.want)
		}
	}
	v := x.Check([]string{strings.Repeat("a", 300)}, 0)[0]
	if v.Length != 300 || v.Limit != 280 || v.Part != 1 {
		t.Errorf("too_long detail = %+v", v)
	}
}

func TestSplit(t *testing.T) {
	t.Parallel()
	bsky := rulesOf(t, Bluesky)
	long := strings.Repeat("word ", 130) // 650 graphemes
	t.Run("fits unchanged", func(t *testing.T) {
		t.Parallel()
		if got := bsky.Split("  short post ", FitError); len(got) != 1 || got[0] != "short post" {
			t.Fatalf("Split = %q", got)
		}
	})
	t.Run("explicit thread breaks", func(t *testing.T) {
		t.Parallel()
		got := bsky.Split("one"+ThreadBreak+"two"+ThreadBreak+"  ", FitError)
		if strings.Join(got, "|") != "one|two" {
			t.Fatalf("Split = %q", got)
		}
	})
	t.Run("error leaves it too long", func(t *testing.T) {
		t.Parallel()
		got := bsky.Split(long, FitError)
		if len(bsky.Check(got, 0)) == 0 {
			t.Fatal("expected a violation")
		}
	})
	t.Run("truncate at a word", func(t *testing.T) {
		t.Parallel()
		got := bsky.Split(long, FitTruncate)
		if len(got) != 1 || !strings.HasSuffix(got[0], "word…") || bsky.Length(got[0]) > 300 {
			t.Fatalf("Split = %q (len %d)", got, bsky.Length(got[0]))
		}
		if vs := bsky.Check(got, 0); len(vs) != 0 {
			t.Fatalf("violations after truncate: %+v", vs)
		}
	})
	t.Run("thread packs words", func(t *testing.T) {
		t.Parallel()
		got := bsky.Split(long, FitThread)
		if len(got) != 3 {
			t.Fatalf("got %d parts, want 3", len(got))
		}
		if vs := bsky.Check(got, 0); len(vs) != 0 {
			t.Fatalf("violations after threading: %+v", vs)
		}
		if strings.Join(got, " ") != strings.TrimSpace(long) {
			t.Fatal("threading lost or changed words")
		}
	})
	t.Run("thread on a platform without threads truncates nothing", func(t *testing.T) {
		t.Parallel()
		tg := rulesOf(t, Telegram)
		huge := strings.Repeat("a ", 3000)
		if got := tg.Split(huge, FitThread); len(got) != 1 {
			t.Fatalf("got %d parts", len(got))
		}
	})
	t.Run("a word longer than a part is cut", func(t *testing.T) {
		t.Parallel()
		got := bsky.Split(strings.Repeat("x", 700), FitThread)
		if len(got) != 3 || bsky.Length(got[0]) != 300 {
			t.Fatalf("parts %d, first %d", len(got), bsky.Length(got[0]))
		}
	})
}
