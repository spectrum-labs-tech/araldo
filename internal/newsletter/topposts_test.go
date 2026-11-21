// SPDX-License-Identifier: AGPL-3.0-or-later

package newsletter

import (
	"strings"
	"testing"
)

func TestEscapeReadsAsPlainText(t *testing.T) {
	t.Parallel()
	tests := []string{
		"plain words",
		"# not a heading",
		"> not a quote",
		"- not a list",
		"1. not a list either",
		"**not bold** and _not em_ and [not](a link)",
		"back\\slash and `code`",
	}
	for _, in := range tests {
		r, err := Render(Issue{Subject: "s", Body: Escape(in)}, Options{})
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := strings.TrimSpace(strings.SplitN(r.Text, "\n\n", 2)[0]); !strings.Contains(r.Text, in) {
			t.Errorf("Escape(%q) renders as %q", in, got)
		}
		if strings.Contains(r.HTML, "<strong>") || strings.Contains(r.HTML, "<em>") || strings.Contains(r.HTML, "<h2") ||
			strings.Contains(r.HTML, "<li") || strings.Contains(r.HTML, "<blockquote") || strings.Contains(r.HTML, "<code") {
			t.Errorf("Escape(%q) still renders markup", in)
		}
	}
}

func TestTopPostsMarkdown(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("word ", 100)
	md := TopPostsMarkdown("Top posts of the week", []TopPost{
		{Text: "Shipped **v2**\nwith passkeys", Network: "Bluesky", Link: "https://bsky.app/p/1", Image: "media_1", Alt: "The new [login]", Likes: 42, Reposts: 1},
		{Text: long, Network: "Mastodon", Replies: 3},
		{Text: "no image alt", Network: "X", Link: "https://x.com/1", Image: "media_2"},
	})
	for _, want := range []string{
		"## Top posts of the week\n",
		"![The new login](media_1)\n\nShipped \\*\\*v2\\*\\* with passkeys\n[On Bluesky](https://bsky.app/p/1) · 42 likes · 1 repost\n",
		"…\n3 replies\n",
		"\nno image alt\n[On X](https://x.com/1)\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in\n%s", want, md)
		}
	}
	if strings.Contains(md, "media_2") {
		t.Error("an image with no alt text is left out rather than sent undescribed")
	}
	if strings.Contains(md, long) {
		t.Error("a long post is not cut")
	}
	// It renders: a heading, an image, links, and no stray markup.
	r, err := Render(Issue{Subject: "s", Body: md}, Options{Image: func(src string) (Image, error) { return Image{URL: "https://cdn.example/" + src}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.HTML, "https://cdn.example/media_1") || !strings.Contains(r.HTML, `href="https://bsky.app/p/1"`) || strings.Contains(r.HTML, "<strong>") {
		t.Errorf("rendered:\n%s", r.HTML)
	}
}
