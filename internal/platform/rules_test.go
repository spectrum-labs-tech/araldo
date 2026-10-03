// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"strings"
	"testing"
	"time"
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
		{"instagram converts an opaque png", rulesOf(t, Instagram), []string{"hi"}, []Media{{Type: "image/png", Size: 1, Width: 1, Height: 1}}, ""},
		{"instagram takes no transparent png", rulesOf(t, Instagram), []string{"hi"},
			[]Media{{Type: "image/png", Size: 1, Width: 1, Height: 1, Transparent: true}}, "media_type_unsupported"},
		{"telegram has no threads", rulesOf(t, Telegram), []string{"a", "b"}, nil, "threads_unsupported"},
		{"telegram panorama, resized", rulesOf(t, Telegram), []string{"a"}, []Media{jpeg(1, 9000, 1500)}, ""},
		{"telegram panorama too wide to keep its shape", rulesOf(t, Telegram), []string{"a"}, []Media{jpeg(1, 21000, 1000)}, "media_aspect_ratio"},
		{"too much media", x, []string{"hi"}, []Media{square, square, square, square, square}, "too_much_media"},
		{"bluesky's 1 MB, resized", rulesOf(t, Bluesky), []string{"hi"}, []Media{jpeg(1_000_000, 1, 1), jpeg(1_000_001, 1, 1)}, ""},
		{"bluesky's 1 MB, a gif", rulesOf(t, Bluesky), []string{"hi"}, []Media{{Type: "image/gif", Size: 1_000_001, Width: 1, Height: 1}}, "media_too_large"},
		{"bluesky's 1 MB, too many pixels", rulesOf(t, Bluesky), []string{"hi"}, []Media{jpeg(9_000_000, 10_000, 6_000)}, "media_too_large"},
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
		{"too large", rulesOf(t, Bluesky), Media{Type: "image/png", Size: 2_345_678, Width: 1, Height: 1, Transparent: true},
			Violation{Code: "media_too_large", Media: 1, Length: 2_345_678, Limit: 1_000_000,
				Message: "Bluesky takes images up to 1 MB; image 1 is 2.3 MB (it has transparent pixels, which a resized JPEG cannot keep)"}},
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

func TestNoticesSayWhatIsResized(t *testing.T) {
	t.Parallel()
	bsky := rulesOf(t, Bluesky)
	big := Media{Type: "image/jpeg", Size: 3_200_000, Width: 4032, Height: 3024}
	small := Media{Type: "image/jpeg", Size: 400_000, Width: 1080, Height: 1080}
	gif := Media{Type: "image/gif", Size: 3_000_000, Width: 500, Height: 500}
	got := bsky.Notices([]Media{small, big, gif})
	if len(got) != 1 || got[0].Code != "media_resized" || got[0].Media != 2 ||
		got[0].Message != "image 2 will be resized into a JPEG for Bluesky: Bluesky takes images up to 1 MB; this is 3.2 MB" {
		t.Fatalf("notices %+v", got)
	}
	rs, needed, possible := bsky.ResizeFor(big)
	if !needed || !possible || rs.MaxBytes != 1_000_000 {
		t.Fatalf("resize %+v, %t, %t", rs, needed, possible)
	}
	if _, needed, _ := bsky.ResizeFor(small); needed {
		t.Fatal("a small image needs no resizing")
	}
	if _, needed, possible := bsky.ResizeFor(gif); !needed || possible {
		t.Fatal("a GIF cannot be resized")
	}
	ig := rulesOf(t, Instagram)
	if rs, needed, possible := ig.ResizeFor(Media{Type: "image/webp", Size: 100, Width: 1080, Height: 1080}); !needed || !possible || rs.MaxBytes != 8_000_000 {
		t.Fatalf("instagram converts a WebP: %+v, %t, %t", rs, needed, possible)
	}
	if got := rulesOf(t, X).Notices([]Media{small}); got == nil || len(got) != 0 {
		t.Fatalf("no notices is an empty list: %#v", got)
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

func TestCheckVideo(t *testing.T) {
	t.Parallel()
	r := Rules{Provider: "test", Name: "Test", MaxLength: 100, Counting: CountRunes, MaxMedia: 4,
		Images: map[string]int64{"image/jpeg": 0},
		Video: &VideoRules{Types: map[string]int64{"video/mp4": 100 << 20}, MinDuration: 3 * time.Second, MaxDuration: 140 * time.Second,
			MinAspect: 1.0 / 3, MaxAspect: 3, MaxFrameRate: 60, Codecs: []string{"avc1"}}}
	video := func(change func(*Media)) Media {
		m := Media{Type: "video/mp4", Size: 10 << 20, Width: 1080, Height: 1920, Duration: 30 * time.Second, FrameRate: 30, VideoCodec: "avc1"}
		if change != nil {
			change(&m)
		}
		return m
	}
	tests := []struct {
		name  string
		r     Rules
		media []Media
		want  string
	}{
		{"fits", r, []Media{video(nil)}, ""},
		{"a platform without video", Rules{Name: "Plain", MaxLength: 100, MaxMedia: 4}, []Media{video(nil)}, "video_unsupported"},
		{"with an image too", r, []Media{video(nil), {Type: "image/jpeg", Size: 1, Width: 1, Height: 1}}, "video_alone"},
		{"quicktime", r, []Media{video(func(m *Media) { m.Type = "video/quicktime" })}, "media_type_unsupported"},
		{"too big", r, []Media{video(func(m *Media) { m.Size = 200 << 20 })}, "media_too_large"},
		{"too short", r, []Media{video(func(m *Media) { m.Duration = time.Second })}, "video_too_short"},
		{"too long", r, []Media{video(func(m *Media) { m.Duration = 3 * time.Minute })}, "video_too_long"},
		{"too narrow", r, []Media{video(func(m *Media) { m.Width = 300 })}, "media_aspect_ratio"},
		{"too fast", r, []Media{video(func(m *Media) { m.FrameRate = 120 })}, "video_frame_rate"},
		{"hevc", r, []Media{video(func(m *Media) { m.VideoCodec = "hvc1" })}, "video_codec_unsupported"},
	}
	for _, tt := range tests {
		var codes []string
		for _, v := range tt.r.Check([]string{"watch"}, tt.media) {
			codes = append(codes, v.Code)
		}
		if got := strings.Join(codes, ","); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	got := r.Check([]string{"x"}, []Media{video(func(m *Media) { m.VideoCodec = "hvc1" })})
	if got[0].Message != "Test takes H.264 video; video 1 is HEVC: export it as H.264" {
		t.Fatalf("message %q", got[0].Message)
	}
	if _, needed, _ := r.ResizeFor(video(nil)); needed || len(r.Notices([]Media{video(nil)})) != 0 {
		t.Fatal("a video is never resized")
	}
}
