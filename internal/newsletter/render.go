// SPDX-License-Identifier: AGPL-3.0-or-later

// Package newsletter renders a newsletter issue into email-safe HTML and a
// plain-text version (ADR 0024 decision 8). The body is a small Markdown
// subset, chosen so every piece has a tested rendering across mail
// clients:
//
//	# Heading, ## Heading, ### Heading
//	paragraphs, whose line breaks are kept
//	- or * lists, 1. numbered lists
//	> quotes
//	--- a divider
//	![alt](media_… or https://…) an image on a line of its own
//	[Label](https://…){.button} a button on a line of its own
//	**bold**, *italic*, `code`, [text](https://…) and bare https:// links
//
// The HTML is table-based with inline styles, 600 pixels wide, with a
// stylesheet only for dark mode and narrow screens, which clients that
// drop it can do without.
package newsletter

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// Theme is a brand's look in email (ADR 0024 decision 9).
type Theme struct {
	BrandName string
	// LogoURL is a permanent link to the logo, or empty for the name.
	LogoURL string
	// Accent colors links, buttons and quotes: "#rrggbb".
	Accent string
	// PostalAddress is the sender's, which anti-spam laws require.
	PostalAddress string
	// Footer is a line of text above the address, e.g. why the reader
	// gets this.
	Footer string
}

// DefaultAccent is used when a theme has none.
const DefaultAccent = "#1d4ed8"

// Image is an image's source resolved for email.
type Image struct {
	URL           string
	Alt           string
	Width, Height int // 0 when unknown
}

// Options says how to resolve images and links.
type Options struct {
	Theme Theme
	// Image resolves an image's source (a media ID or an https URL).
	Image func(src string) (Image, error)
	// Link rewrites a link's URL; n is its place in the issue, from 1. Nil
	// leaves links as they are.
	Link func(raw string, n int) string
	// Unsubscribe is the unsubscribe link's href: the provider's
	// placeholder for each recipient's own link.
	Unsubscribe string
}

// Issue is what is rendered.
type Issue struct {
	Subject     string
	PreviewText string
	Body        string
}

// Rendered is an issue ready to hand to a provider.
type Rendered struct {
	HTML string
	Text string
}

// Error is a problem with the body, on a line counted from 1.
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// contentWidth is the body's width inside the 600-pixel card.
const contentWidth = 520

const fonts = "-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif"

type blockKind int

const (
	bParagraph blockKind = iota
	bHeading
	bList
	bQuote
	bDivider
	bImage
	bButton
)

type block struct {
	kind    blockKind
	line    int
	level   int      // heading
	lines   []string // paragraph, quote
	items   []string // list
	ordered bool
	alt     string // image
	src     string // image
	label   string // button
	href    string // button
}

var (
	headingRE = regexp.MustCompile(`^(#{1,3})\s+(.+?)\s*#*\s*$`)
	dividerRE = regexp.MustCompile(`^(?:-{3,}|\*{3,}|_{3,})$`)
	imageRE   = regexp.MustCompile(`^!\[([^\]]*)\]\(\s*([^)\s]+)\s*\)$`)
	buttonRE  = regexp.MustCompile(`^\[([^\]]+)\]\(\s*([^)\s]+)\s*\)\{\.button\}$`)
	bulletRE  = regexp.MustCompile(`^[-*+]\s+(.*)$`)
	numberRE  = regexp.MustCompile(`^\d{1,9}[.)]\s+(.*)$`)
)

// parse splits a body into blocks.
func parse(body string) []block {
	var out []block
	var cur *block
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for i, raw := range lines {
		n := i + 1
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flush()
		case headingRE.MatchString(trimmed):
			flush()
			m := headingRE.FindStringSubmatch(trimmed)
			out = append(out, block{kind: bHeading, line: n, level: len(m[1]), lines: []string{m[2]}})
		case dividerRE.MatchString(trimmed):
			flush()
			out = append(out, block{kind: bDivider, line: n})
		case imageRE.MatchString(trimmed):
			flush()
			m := imageRE.FindStringSubmatch(trimmed)
			out = append(out, block{kind: bImage, line: n, alt: m[1], src: m[2]})
		case buttonRE.MatchString(trimmed):
			flush()
			m := buttonRE.FindStringSubmatch(trimmed)
			out = append(out, block{kind: bButton, line: n, label: m[1], href: m[2]})
		case bulletRE.MatchString(trimmed) || numberRE.MatchString(trimmed):
			ordered := numberRE.MatchString(trimmed)
			re := bulletRE
			if ordered {
				re = numberRE
			}
			if cur == nil || cur.kind != bList || cur.ordered != ordered {
				flush()
				cur = &block{kind: bList, line: n, ordered: ordered}
			}
			cur.items = append(cur.items, re.FindStringSubmatch(trimmed)[1])
		case strings.HasPrefix(trimmed, ">"):
			text := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			if cur == nil || cur.kind != bQuote {
				flush()
				cur = &block{kind: bQuote, line: n}
			}
			cur.lines = append(cur.lines, text)
		case cur != nil && cur.kind == bList && line != trimmed:
			// An indented line continues the list's last item.
			cur.items[len(cur.items)-1] += " " + trimmed
		default:
			if cur == nil || cur.kind != bParagraph {
				flush()
				cur = &block{kind: bParagraph, line: n}
			}
			cur.lines = append(cur.lines, trimmed)
		}
	}
	flush()
	return out
}

// renderer holds one render's state.
type renderer struct {
	o      Options
	accent string
	links  int
	err    error
}

// safeLink reports whether a link may be followed from an email.
func safeLink(u string) bool {
	l := strings.ToLower(u)
	return strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "mailto:")
}

// link numbers and rewrites a link.
func (r *renderer) link(u string) string {
	r.links++
	if r.o.Link == nil || strings.HasPrefix(strings.ToLower(u), "mailto:") {
		return u
	}
	return r.o.Link(u, r.links)
}

var bareURL = regexp.MustCompile(`^https?://[^\s<>"]+`)

// inline renders a run of text. html is the HTML; text is the plain
// version, with each link's URL after its text.
func (r *renderer) inline(s string) (htm, text string) {
	var h, t strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		switch {
		case rest[0] == '\\' && len(rest) > 1 && strings.ContainsRune("\\`*_[]()#+-.!>{}", rune(rest[1])):
			h.WriteString(html.EscapeString(rest[1:2]))
			t.WriteByte(rest[1])
			i += 2
			continue
		case rest[0] == '`':
			if end := strings.IndexByte(rest[1:], '`'); end >= 0 {
				code := rest[1 : 1+end]
				h.WriteString(`<code style="font-family:Menlo,Consolas,monospace;font-size:14px;">` + html.EscapeString(code) + `</code>`)
				t.WriteString(code)
				i += end + 2
				continue
			}
		case strings.HasPrefix(rest, "**"):
			if end := strings.Index(rest[2:], "**"); end > 0 {
				ih, it := r.inline(rest[2 : 2+end])
				h.WriteString("<strong>" + ih + "</strong>")
				t.WriteString(it)
				i += end + 4
				continue
			}
		case rest[0] == '*' || (rest[0] == '_' && (i == 0 || s[i-1] == ' ')):
			if end := strings.IndexByte(rest[1:], rest[0]); end > 0 && rest[1] != ' ' {
				ih, it := r.inline(rest[1 : 1+end])
				h.WriteString("<em>" + ih + "</em>")
				t.WriteString(it)
				i += end + 2
				continue
			}
		case rest[0] == '[':
			if m := inlineLinkRE.FindStringSubmatch(rest); m != nil {
				ih, it := r.inline(m[1])
				if safeLink(m[2]) {
					u := r.link(m[2])
					h.WriteString(`<a href="` + html.EscapeString(u) + `" style="color:` + r.accent + `;text-decoration:underline;">` + ih + `</a>`)
					t.WriteString(it + " (" + u + ")")
				} else {
					h.WriteString(ih)
					t.WriteString(it)
				}
				i += len(m[0])
				continue
			}
		case rest[0] == 'h':
			if m := bareURL.FindString(rest); m != "" && (i == 0 || !isWordByte(s[i-1])) {
				m = strings.TrimRight(m, ".,;:!?)'")
				u := r.link(m)
				h.WriteString(`<a href="` + html.EscapeString(u) + `" style="color:` + r.accent + `;text-decoration:underline;">` + html.EscapeString(m) + `</a>`)
				t.WriteString(u)
				i += len(m)
				continue
			}
		}
		h.WriteString(html.EscapeString(rest[:1]))
		t.WriteByte(rest[0])
		i++
	}
	return h.String(), t.String()
}

var inlineLinkRE = regexp.MustCompile(`^\[([^\]]+)\]\(\s*([^)\s]+)\s*\)`)

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// lines renders lines joined by line breaks.
func (r *renderer) lines(ls []string) (htm, text string) {
	hs, ts := make([]string, len(ls)), make([]string, len(ls))
	for i, l := range ls {
		hs[i], ts[i] = r.inline(l)
	}
	return strings.Join(hs, "<br>\n"), strings.Join(ts, "\n")
}

var headingSize = map[int]string{1: "26px", 2: "21px", 3: "18px"}

func (r *renderer) block(b block) (htm, text string) {
	switch b.kind {
	case bHeading:
		h, t := r.inline(b.lines[0])
		under := "="
		if b.level > 1 {
			under = "-"
		}
		return fmt.Sprintf(`<h%d style="margin:0 0 16px;font-size:%s;line-height:1.3;font-weight:700;">%s</h%d>`, b.level, headingSize[b.level], h, b.level),
			t + "\n" + strings.Repeat(under, len([]rune(t)))
	case bList:
		tag := "ul"
		if b.ordered {
			tag = "ol"
		}
		var h, t strings.Builder
		h.WriteString(`<` + tag + ` style="margin:0 0 16px;padding-left:24px;">`)
		for i, item := range b.items {
			ih, it := r.inline(item)
			h.WriteString(`<li style="margin:0 0 6px;">` + ih + `</li>`)
			mark := "- "
			if b.ordered {
				mark = strconv.Itoa(i+1) + ". "
			}
			if i > 0 {
				t.WriteByte('\n')
			}
			t.WriteString(mark + it)
		}
		h.WriteString(`</` + tag + `>`)
		return h.String(), t.String()
	case bQuote:
		h, t := r.lines(b.lines)
		return `<blockquote class="muted" style="margin:0 0 16px;padding:4px 0 4px 16px;border-left:4px solid ` + r.accent + `;color:#4b5563;">` + h + `</blockquote>`,
			"> " + strings.ReplaceAll(t, "\n", "\n> ")
	case bDivider:
		return `<hr style="border:0;border-top:1px solid #d1d5db;margin:24px 0;">`, "----"
	case bImage:
		if r.o.Image == nil {
			r.fail(b.line, "images are not available here")
			return "", ""
		}
		img, err := r.o.Image(b.src)
		if err != nil {
			r.fail(b.line, err.Error())
			return "", ""
		}
		alt := b.alt
		if alt == "" {
			alt = img.Alt
		}
		w := contentWidth
		if img.Width > 0 && img.Width < w {
			w = img.Width
		}
		text := ""
		if alt != "" {
			text = "[" + alt + "]"
		}
		return fmt.Sprintf(`<img src="%s" alt="%s" width="%d" style="display:block;width:100%%;max-width:%dpx;height:auto;border:0;margin:0 0 16px;border-radius:4px;">`,
			html.EscapeString(img.URL), html.EscapeString(alt), w, w), text
	case bButton:
		label, labelText := r.inline(b.label)
		if !safeLink(b.href) {
			r.fail(b.line, "a button needs an https:// link")
			return "", ""
		}
		u := r.link(b.href)
		return `<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:8px 0 24px;"><tr>` +
				`<td style="border-radius:6px;background:` + r.accent + `;">` +
				`<a href="` + html.EscapeString(u) + `" style="display:inline-block;padding:12px 24px;font-family:` + fonts + `;font-size:16px;font-weight:600;color:` +
				onAccent(r.accent) + `;text-decoration:none;border-radius:6px;">` + label + `</a></td></tr></table>`,
			labelText + ": " + u
	default:
		h, t := r.lines(b.lines)
		return `<p style="margin:0 0 16px;">` + h + `</p>`, t
	}
}

func (r *renderer) fail(line int, msg string) {
	if r.err == nil {
		r.err = &Error{Line: line, Msg: msg}
	}
}

// Render renders an issue. A problem with the body is an *Error naming
// its line.
func Render(issue Issue, o Options) (Rendered, error) {
	accent := o.Theme.Accent
	if !hexColor.MatchString(accent) {
		accent = DefaultAccent
	}
	r := &renderer{o: o, accent: accent}
	var body, text []string
	for _, b := range parse(issue.Body) {
		h, t := r.block(b)
		body, text = append(body, h), append(text, t)
	}
	if r.err != nil {
		return Rendered{}, r.err
	}
	return Rendered{HTML: page(issue, o, accent, strings.Join(body, "\n")), Text: plain(issue, o, strings.Join(text, "\n\n"))}, nil
}

func page(issue Issue, o Options, accent, body string) string {
	t := o.Theme
	esc := html.EscapeString
	head := `<span style="font-family:` + fonts + `;font-size:20px;font-weight:700;">` + esc(t.BrandName) + `</span>`
	if t.LogoURL != "" {
		head = `<img src="` + esc(t.LogoURL) + `" alt="` + esc(t.BrandName) + `" height="40" style="display:block;height:40px;width:auto;border:0;">`
	}
	var foot []string
	if t.Footer != "" {
		foot = append(foot, esc(t.Footer))
	}
	if t.PostalAddress != "" {
		foot = append(foot, esc(t.BrandName)+" · "+strings.ReplaceAll(esc(t.PostalAddress), "\n", ", "))
	}
	if o.Unsubscribe != "" {
		// The placeholder goes in as it is: the provider replaces it.
		foot = append(foot, `<a href="`+o.Unsubscribe+`" style="color:#6b7280;text-decoration:underline;">Unsubscribe</a>`)
	}
	// Filler after the preview text keeps clients from showing the body's
	// first words after it.
	filler := strings.Repeat("&#8199;&#65279;&#847; ", 40)
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="x-apple-disable-message-reformatting">
<meta name="color-scheme" content="light dark">
<meta name="supported-color-schemes" content="light dark">
<title>` + esc(issue.Subject) + `</title>
<style>
body { margin: 0; padding: 0; }
@media (prefers-color-scheme: dark) {
  .page { background: #111418 !important; }
  .card { background: #1b1f24 !important; }
  .text { color: #e5e7eb !important; }
  .muted { color: #a1a8b3 !important; }
}
@media (max-width: 620px) {
  .card { width: 100% !important; }
  .pad { padding-left: 20px !important; padding-right: 20px !important; }
}
</style>
</head>
<body class="page" style="margin:0;padding:0;background:#f3f4f6;">
<div style="display:none;max-height:0;overflow:hidden;mso-hide:all;">` + esc(issue.PreviewText) + filler + `</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" class="page" style="background:#f3f4f6;">
<tr><td align="center" style="padding:24px 12px;">
<table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" class="card" style="width:600px;max-width:600px;background:#ffffff;border-radius:8px;">
<tr><td class="pad text" style="padding:32px 40px 16px;color:#111827;">` + head + `</td></tr>
<tr><td class="pad text" style="padding:8px 40px 24px;font-family:` + fonts + `;font-size:16px;line-height:1.6;color:#1f2937;">
` + body + `
</td></tr>
</table>
<table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" class="card" style="width:600px;max-width:600px;">
<tr><td class="pad muted" style="padding:16px 40px;font-family:` + fonts + `;font-size:12px;line-height:1.6;color:#6b7280;text-align:center;">
` + strings.Join(foot, "<br>\n") + `
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>
`
}

func plain(issue Issue, o Options, body string) string {
	t := o.Theme
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("\n\n--\n")
	if t.Footer != "" {
		b.WriteString(t.Footer + "\n")
	}
	if t.PostalAddress != "" {
		b.WriteString(t.BrandName + ", " + strings.ReplaceAll(t.PostalAddress, "\n", ", ") + "\n")
	}
	if o.Unsubscribe != "" {
		b.WriteString("Unsubscribe: " + o.Unsubscribe + "\n")
	}
	return b.String()
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ValidAccent reports whether c is "#rrggbb" and readable as link text on
// white: a contrast of at least 4.5:1 (WCAG AA).
func ValidAccent(c string) bool {
	return hexColor.MatchString(c) && Contrast(c, "#ffffff") >= 4.5
}

// onAccent is the button text color that reads best on the accent.
func onAccent(accent string) string {
	if Contrast(accent, "#ffffff") >= Contrast(accent, "#111827") {
		return "#ffffff"
	}
	return "#111827"
}
