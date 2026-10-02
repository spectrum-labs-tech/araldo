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
	jpeg := func(size int64, w, h int) Media { return Media{Type: "image/jpeg", Size: size, Width: w, Height: h} }
	square := jpeg(500_000, 1080, 1080)
	tests := []struct {
		name  string
		r     Rules
		parts []string
		media []Media
		want  string
	}{
		{"fits", x, []string{"hello"}, nil, ""},
		{"empty", x, []string{"  "}, nil, "empty"},
		{"an image needs no text", x, []string{""}, []Media{square}, ""},
		{"too long", x, []string{strings.Repeat("a", 281)}, nil, "too_long"},
		{"instagram needs media", rulesOf(t, Instagram), []string{"hi"}, nil, "media_required"},
		{"instagram square", rulesOf(t, Instagram), []string{"hi"}, []Media{square}, ""},
		{"instagram 4:5 portrait", rulesOf(t, Instagram), []string{"hi"}, []Media{jpeg(1, 1080, 1350)}, ""},
		{"instagram too tall", rulesOf(t, Instagram), []string{"hi"}, []Media{jpeg(1, 1080, 1920)}, "media_aspect_ratio"},
		{"instagram takes no png", rulesOf(t, Instagram), []string{"hi"}, []Media{{Type: "image/png", Size: 1, Width: 1, Height: 1}}, "media_type_unsupported"},
		{"telegram has no threads", rulesOf(t, Telegram), []string{"a", "b"}, nil, "threads_unsupported"},
		{"telegram panorama", rulesOf(t, Telegram), []string{"a"}, []Media{jpeg(1, 9000, 1500)}, "media_dimensions"},
		{"too much media", x, []string{"hi"}, []Media{square, square, square, square, square}, "too_much_media"},
		{"bluesky's 1 MB", rulesOf(t, Bluesky), []string{"hi"}, []Media{jpeg(1_000_000, 1, 1), jpeg(1_000_001, 1, 1)}, "media_too_large"},
		{"linkedin documents no size", rulesOf(t, LinkedIn), []string{"hi"}, []Media{jpeg(50<<20, 1, 1)}, ""},
		{"x takes big gifs", x, []string{"hi"}, []Media{{Type: "image/gif", Size: 12_000_000, Width: 1, Height: 1}}, ""},
	}
	for _, tt := range tests {
		if got := codes(tt.r.ForMedia(len(tt.media)).Check(tt.parts, tt.media)); got != tt.want {
			t.Errorf("%s: violations %q, want %q", tt.name, got, tt.want)
		}
	}
	v := x.Check([]string{strings.Repeat("a", 300)}, nil)[0]
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
		if len(bsky.Check(got, nil)) == 0 {
			t.Fatal("expected a violation")
		}
	})
	t.Run("truncate at a word", func(t *testing.T) {
		t.Parallel()
		got := bsky.Split(long, FitTruncate)
		if len(got) != 1 || !strings.HasSuffix(got[0], "word…") || bsky.Length(got[0]) > 300 {
			t.Fatalf("Split = %q (len %d)", got, bsky.Length(got[0]))
		}
		if vs := bsky.Check(got, nil); len(vs) != 0 {
			t.Fatalf("violations after truncate: %+v", vs)
		}
	})
	t.Run("thread packs words", func(t *testing.T) {
		t.Parallel()
		got := bsky.Split(long, FitThread)
		if len(got) != 3 {
			t.Fatalf("got %d parts, want 3", len(got))
		}
		if vs := bsky.Check(got, nil); len(vs) != 0 {
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

func TestCheckExplainsMedia(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		r    Rules
		m    Media
		want Violation
	}{
		{"too large", rulesOf(t, Bluesky), Media{Type: "image/png", Size: 2_345_678, Width: 1, Height: 1},
			Violation{Code: "media_too_large", Media: 1, Length: 2_345_678, Limit: 1_000_000, Message: "Bluesky takes images up to 1 MB; image 1 is 2.3 MB"}},
		{"aspect", rulesOf(t, Instagram), Media{Type: "image/jpeg", Size: 1, Width: 1000, Height: 2000},
			Violation{Code: "media_aspect_ratio", Media: 1, Message: "Instagram takes images from 4:5 to 1.91:1 (width:height); image 1 is 1000×2000"}},
		{"type", rulesOf(t, Threads), Media{Type: "image/gif", Size: 1, Width: 1, Height: 1},
			Violation{Code: "media_type_unsupported", Media: 1, Message: "Threads does not take gif images (it takes jpeg, png)"}},
	}
	for _, tt := range tests {
		got := tt.r.Check([]string{"hi"}, []Media{tt.m})
		if len(got) != 1 || got[0] != tt.want {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestForMedia(t *testing.T) {
	t.Parallel()
	tg := rulesOf(t, Telegram)
	if tg.ForMedia(0).MaxLength != 4096 || tg.ForMedia(1).MaxLength != 1024 {
		t.Fatalf("Telegram limits %d / %d, want 4096 without media and 1024 as a caption", tg.ForMedia(0).MaxLength, tg.ForMedia(1).MaxLength)
	}
	if got := tg.ForMedia(1).Check([]string{strings.Repeat("a", 1025)}, []Media{{Type: "image/jpeg", Size: 1, Width: 1, Height: 1}}); len(got) != 1 || got[0].Code != "too_long" {
		t.Fatalf("a 1025-character caption: %+v", got)
	}
	if b := rulesOf(t, Bluesky); b.ForMedia(4).MaxLength != b.MaxLength {
		t.Fatal("Bluesky's limit should not change with media")
	}
}

func TestEveryPlatformDocumentsItsImages(t *testing.T) {
	t.Parallel()
	for _, p := range Emulable() {
		r := rulesOf(t, p)
		if len(r.Images) == 0 || r.ImageSource == "" || r.MaxMedia < 1 {
			t.Errorf("%s: images %v from %q, max %d: every platform needs image rules with a source", p, r.Images, r.ImageSource, r.MaxMedia)
		}
	}
}
