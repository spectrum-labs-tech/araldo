// SPDX-License-Identifier: AGPL-3.0-or-later

package web_test

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The checker TestPagesAreAccessible relies on catches what it should.
func TestA11yChecker(t *testing.T) {
	t.Parallel()
	page := func(body string) string {
		return `<!doctype html><html lang="en"><head><title>T</title></head><body><a href="#main">Skip</a>` +
			`<nav aria-label="Main"><a class="active" aria-current="page" href="/">Home</a></nav><main id="main"><h1>Page</h1>` + body + `</main></body></html>`
	}
	tests := []struct {
		name, body, want string
	}{
		{"clean", `<label>Name <input name="name"></label><label for="e">Email</label><input id="e" name="email"><button>Go</button>`, ""},
		{"unlabeled input", `<input name="q">`, `<input name="q"> has no label`},
		{"submit on change", `<label>Brand <select name="brand" data-autosubmit></select></label>`, "submits on change"},
		{"empty button", `<button><svg aria-hidden="true"></svg></button>`, "a button has no name"},
		{"empty link", `<a href="/x"></a>`, "has no text"},
		{"image link with alt", `<a href="/x"><img src="a.png" alt="Home"></a>`, ""},
		{"image without alt", `<img src="a.png">`, "has no alt"},
		{"skipped heading", `<h3>Deep</h3>`, "skips from h1 to h3"},
		{"two h1", `<h1>Again</h1>`, "2 <h1> elements"},
		{"duplicate id", `<p id="x"></p><p id="x"></p>`, `id "x" is used 2 times`},
		{"broken reference", `<p aria-describedby="nope"></p>`, `missing id "nope"`},
		{"positive tabindex", `<p tabindex="3"></p>`, "tabindex=3"},
		{"table without headers", `<table><tr><td>1</td></tr></table>`, "no header cells"},
		{"unmarked current page", `<nav aria-label="Other"><a class="active" href="/y">Y</a></nav>`, "highlighted true"},
	}
	for _, tt := range tests {
		issues := strings.Join(a11yIssues(page(tt.body), true), "; ")
		if tt.want == "" && issues != "" || !strings.Contains(issues, tt.want) {
			t.Errorf("%s: issues %q, want %q", tt.name, issues, tt.want)
		}
	}
	noSkip := `<!doctype html><html lang="en"><head><title>T</title></head><body><main><h1>x</h1><a href="/a">A</a></main></body></html>`
	if issues := strings.Join(a11yIssues(noSkip, true), "; "); !strings.Contains(issues, "skip link") {
		t.Errorf("a page without a skip link: %q", issues)
	}
}

// TestContrast checks the theme's colors, light and dark, against WCAG 2.2
// AA: 4.5:1 for text (every size the dashboard uses is small text), 3:1
// for the edges of form controls and the focus outline (1.4.11).
func TestContrast(t *testing.T) {
	t.Parallel()
	css, err := os.ReadFile("styles/app.css")
	if err != nil {
		t.Fatal(err)
	}
	s := string(css)
	dark := strings.Index(s, "@media (prefers-color-scheme: dark)")
	themes := map[string]map[string]string{"light": tokens(s[:dark]), "dark": tokens(s[dark:])}
	pairs := []struct {
		fg, bg string
		min    float64
	}{
		// Text.
		{"ink", "bg", 4.5}, {"ink", "panel", 4.5}, {"muted", "bg", 4.5}, {"muted", "panel", 4.5}, {"muted", "subtle", 4.5},
		{"accent", "bg", 4.5}, {"accent", "panel", 4.5}, {"accent", "accent-soft", 4.5}, {"accent-ink", "accent", 4.5},
		{"good", "good-bg", 4.5}, {"good", "panel", 4.5}, {"good-ink", "good", 4.5}, {"warn", "warn-bg", 4.5},
		{"bad", "bad-bg", 4.5}, {"bad", "panel", 4.5}, {"test-ink", "test", 4.5},
		{"syn-keyword", "panel", 4.5}, {"syn-func", "panel", 4.5}, {"syn-var", "panel", 4.5}, {"syn-string", "panel", 4.5},
		{"syn-number", "panel", 4.5}, {"syn-brace", "panel", 4.5},
		// Form controls' edges and the focus outline.
		{"field", "panel", 3}, {"field", "bg", 3}, {"field", "subtle", 3}, {"accent", "bg", 3}, {"accent", "panel", 3},
	}
	for name, theme := range themes {
		for _, p := range pairs {
			fg, bg := theme[p.fg], theme[p.bg]
			if fg == "" || bg == "" {
				t.Errorf("%s: no --%s or --%s", name, p.fg, p.bg)
				continue
			}
			if r := contrast(fg, bg); r < p.min {
				t.Errorf("%s: --%s on --%s is %.2f:1, want at least %.1f:1", name, p.fg, p.bg, r, p.min)
			}
		}
	}
}

var tokenRE = regexp.MustCompile(`--([a-z-]+):\s*(#[0-9a-fA-F]{6})`)

func tokens(css string) map[string]string {
	out := map[string]string{}
	for _, m := range tokenRE.FindAllStringSubmatch(css, -1) {
		if _, seen := out[m[1]]; !seen {
			out[m[1]] = m[2]
		}
	}
	return out
}

// contrast is the WCAG contrast ratio of two #rrggbb colors.
func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

func luminance(hex string) float64 {
	var ch [3]float64
	for i := range ch {
		v, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		c := float64(v) / 255
		if c <= 0.03928 {
			ch[i] = c / 12.92
		} else {
			ch[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*ch[0] + 0.7152*ch[1] + 0.0722*ch[2]
}
