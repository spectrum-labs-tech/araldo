// SPDX-License-Identifier: AGPL-3.0-or-later

package newsletter

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func opts() Options {
	return Options{
		Theme: Theme{BrandName: "Open B00KS & Co", Accent: "#1d4ed8", PostalAddress: "1 Main St\nSpringfield", Footer: "You signed up at openb00ks.example."},
		Image: func(src string) (Image, error) {
			if src == "media_1" {
				return Image{URL: "https://araldo.example/v1/media/media_1/content?signature=s", Alt: "Library alt", Width: 300}, nil
			}
			if strings.HasPrefix(src, "https://") {
				return Image{URL: src}, nil
			}
			return Image{}, errors.New("no such image: " + src)
		},
		Link:        func(u string, n int) string { return fmt.Sprintf("%s#%d", u, n) },
		Unsubscribe: "{{ unsubscribe }}",
	}
}

func TestRenderBlocks(t *testing.T) {
	t.Parallel()
	body := "# What shipped\n\nHello **builders**, and *welcome*.\nSecond line with `code`.\n\n" +
		"- one [docs](https://openb00ks.example/docs)\n- two\n  continued\n\n1. first\n2. second\n\n> quoted\n> more\n\n---\n\n" +
		"![](media_1)\n\n[Try it](https://openb00ks.example/try){.button}\n\nSee https://openb00ks.example/blog. Or mail me@openb00ks.example."
	r, err := Render(Issue{Subject: "October <news>", PreviewText: "Three things", Body: body}, opts())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<title>October &lt;news&gt;</title>`,
		`>Three things&#8199;`,
		`<h1 style="margin:0 0 16px;font-size:26px;`,
		`Hello <strong>builders</strong>, and <em>welcome</em>.<br>`,
		`<code style="font-family:Menlo,Consolas,monospace;font-size:14px;">code</code>`,
		`<li style="margin:0 0 6px;">one <a href="https://openb00ks.example/docs#1" class="link" style="color:#1d4ed8;text-decoration:underline;">docs</a></li>`,
		`<li style="margin:0 0 6px;">two continued</li>`,
		`<ol style=`,
		`quoted<br>`,
		`<hr style=`,
		`<img src="https://araldo.example/v1/media/media_1/content?signature=s" alt="Library alt" width="300" style="display:block;width:100%;max-width:300px;`,
		`<a href="https://openb00ks.example/try#2" style="display:inline-block;padding:12px 24px;`,
		`color:#ffffff;text-decoration:none;border-radius:6px;">Try it</a>`,
		`See <a href="https://openb00ks.example/blog#3" class="link" style="color:#1d4ed8;text-decoration:underline;">https://openb00ks.example/blog</a>.`,
		`You signed up at openb00ks.example.<br>`,
		`Open B00KS &amp; Co · 1 Main St, Springfield<br>`,
		`<a href="{{ unsubscribe }}" style="color:#6b7280;text-decoration:underline;">Unsubscribe</a>`,
	} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("HTML lacks %s", want)
		}
	}
	for _, want := range []string{
		"What shipped\n============",
		"Hello builders, and welcome.\nSecond line with code.",
		"- one docs (https://openb00ks.example/docs#1)\n- two continued",
		"1. first\n2. second",
		"> quoted\n> more",
		"[Library alt]",
		"Try it: https://openb00ks.example/try#2",
		"See https://openb00ks.example/blog#3.",
		"Open B00KS & Co, 1 Main St, Springfield\nUnsubscribe: {{ unsubscribe }}",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, r.Text)
		}
	}
}

func TestRenderIsSafe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, notHTML, wantHTML string
	}{
		{"markup is text", `<script>alert(1)</script>`, "<script>", "&lt;script&gt;"},
		{"script links are text", `[click](javascript:alert(1))`, "javascript:", "click"},
		{"quotes cannot leave an attribute", `[x](https://a.example/"onmouseover="y)`, `"onmouseover="`, "&#34;onmouseover=&#34;"},
		{"escapes are literal", `\*not italic\*`, "<em>", "*not italic*"},
		{"an unclosed marker is text", `2 * 3 and **open`, "<strong>", "2 * 3 and **open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, err := Render(Issue{Body: tt.body}, opts())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(r.HTML, tt.notHTML) || !strings.Contains(r.HTML, tt.wantHTML) {
				t.Fatalf("HTML should lack %q and have %q:\n%s", tt.notHTML, tt.wantHTML, r.HTML)
			}
		})
	}
}

func TestRenderErrorsNameTheLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		body string
		line int
	}{
		{"Hi\n\n![logo](media_404)", 3},
		{"[Go](ftp://x.example){.button}", 1},
	}
	for _, tt := range tests {
		_, err := Render(Issue{Body: tt.body}, opts())
		var e *Error
		if !errors.As(err, &e) || e.Line != tt.line {
			t.Errorf("%q: %v, want an error on line %d", tt.body, err, tt.line)
		}
	}
}

func TestRenderTheme(t *testing.T) {
	t.Parallel()
	o := opts()
	o.Theme.LogoURL = "https://cdn.example/logo.png"
	o.Theme.Accent = "#fde047" // light: the button text turns dark
	r, err := Render(Issue{Body: "[Go](https://a.example){.button}"}, o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.HTML, `<img src="https://cdn.example/logo.png" alt="Open B00KS &amp; Co" height="40"`) ||
		!strings.Contains(r.HTML, "color:#111827;text-decoration:none") {
		t.Fatalf("theme not applied:\n%s", r.HTML)
	}
	o.Theme.Accent = "red"
	if r, _ := Render(Issue{Body: "x"}, o); !strings.Contains(r.HTML, "<html") || strings.Contains(r.HTML, "red") {
		t.Fatal("an invalid accent falls back to the default")
	}
}

func TestContrast(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want float64
	}{
		{"#000000", "#ffffff", 21},
		{"#ffffff", "#ffffff", 1},
		{"#1d4ed8", "#ffffff", 6.7},
		{"bogus", "#ffffff", 1},
	}
	for _, tt := range tests {
		if got := Contrast(tt.a, tt.b); math.Abs(got-tt.want) > 0.05 {
			t.Errorf("Contrast(%s, %s) = %.2f, want %.2f", tt.a, tt.b, got, tt.want)
		}
	}
	if !ValidAccent("#1d4ed8") || ValidAccent("#fde047") || ValidAccent("blue") {
		t.Fatal("ValidAccent")
	}
}

func TestButtonLinks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		href string
		ok   bool
	}{
		{"https://openb00ks.example/try", true},
		{"http://openb00ks.example/try", true},
		{"mailto:hello@openb00ks.example", true},
		{"javascript:void", false},
		{"data:text/html,hi", false},
		{"ftp://openb00ks.example/file", false},
	}
	for _, tt := range tests {
		_, err := Render(Issue{Body: "[Go](" + tt.href + "){.button}"}, opts())
		if (err == nil) != tt.ok {
			t.Errorf("%s: %v, want ok %v", tt.href, err, tt.ok)
		}
	}
}

// Links take a lighter accent in a mail app's dark mode, light enough to
// read on the dark card; an accent that already reads there is kept.
func TestDarkAccent(t *testing.T) {
	t.Parallel()
	for _, accent := range []string{"#1d4ed8", "#7c2d12", "#000000", DefaultAccent, "not a color"} {
		got := DarkAccent(accent)
		if c := Contrast(got, DarkCard); c < 4.5 {
			t.Errorf("DarkAccent(%q) = %s: contrast %.2f on the dark card, want 4.5", accent, got, c)
		}
	}
	if got := DarkAccent("#fbbf24"); got != "#fbbf24" {
		t.Errorf("an accent that reads on the dark card became %s", got)
	}
	r, err := Render(Issue{Subject: "S", Body: "See [the changelog](https://araldo.dev/changelog)."}, Options{Theme: Theme{Accent: "#1d4ed8", BrandName: "Araldo"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.HTML, `class="link" style="color:#1d4ed8;`) || !strings.Contains(r.HTML, ".link { color: "+DarkAccent("#1d4ed8")+" !important; }") {
		t.Errorf("links lack the dark mode accent:\n%s", r.HTML)
	}
}

// A preview fixed to a scheme shows the dark styles always or never,
// whatever the viewer's own setting; the page sent is left as it is.
func TestInScheme(t *testing.T) {
	t.Parallel()
	r, err := Render(Issue{Subject: "S", Body: "Hello"}, Options{Theme: Theme{BrandName: "Araldo"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.HTML, darkMedia+" {") || !strings.Contains(r.HTML, `content="light dark"`) {
		t.Fatal("the page sent follows the reader's mode")
	}
	for _, tt := range []struct {
		dark         bool
		media, other string
	}{{false, "@media not all {", `content="light"`}, {true, "@media all {", `content="dark"`}} {
		got := InScheme(r.HTML, tt.dark)
		if strings.Contains(got, darkMedia) || !strings.Contains(got, tt.media) || !strings.Contains(got, tt.other) || strings.Contains(got, `content="light dark"`) {
			t.Errorf("dark=%v:\n%s", tt.dark, got)
		}
	}
}
