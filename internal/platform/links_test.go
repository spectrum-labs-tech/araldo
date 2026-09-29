// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import "testing"

func TestShortLink(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"https://x.dev", "x.dev"},
		{"https://x.dev/", "x.dev"},
		{"http://x.dev/about", "x.dev/about"},
		{"https://x.dev/fifteen-chars-", "x.dev/fifteen-chars-"}, // exactly 15: kept
		{"https://x.dev/sixteen-chars-xx", "x.dev/sixteen-char..."},
		{"https://x.dev/a?utm_source=bluesky", "x.dev/a?utm_source..."},
		{"https://x.dev/#top", "x.dev#top"},
		{"ftp://x.dev/file", "ftp://x.dev/file"}, // not a web link: unchanged
	}
	for _, tt := range tests {
		if got := ShortLink(tt.in); got != tt.want {
			t.Errorf("ShortLink(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestShortenLinks(t *testing.T) {
	t.Parallel()
	text, links := ShortenLinks("née https://x.dev/a, then https://y.dev/a/long/path/here.")
	if want := "née x.dev/a, then y.dev/a/long/path/...."; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	want := []struct{ covers, url string }{
		{"x.dev/a", "https://x.dev/a"},
		{"y.dev/a/long/path/...", "https://y.dev/a/long/path/here"},
	}
	if len(links) != len(want) {
		t.Fatalf("links = %+v", links)
	}
	for i, w := range want {
		if got := text[links[i].Start:links[i].End]; got != w.covers || links[i].URL != w.url {
			t.Errorf("link %d covers %q opening %q, want %q opening %q", i, got, links[i].URL, w.covers, w.url)
		}
	}
	if text, links := ShortenLinks("no links"); text != "no links" || links != nil {
		t.Errorf("ShortenLinks without links = %q, %v", text, links)
	}
}
