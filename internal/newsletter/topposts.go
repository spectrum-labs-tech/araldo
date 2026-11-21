// SPDX-License-Identifier: AGPL-3.0-or-later

package newsletter

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// TopPost is one published post for a "top posts" block (ADR 0024
// phase 2).
type TopPost struct {
	// Text is the post as published, Network where its link goes and Link
	// that copy's permalink ("" when the platform gave none).
	Text, Network, Link string
	// Image is a media ID with its alt text, both "" when the post has no
	// image.
	Image, Alt              string
	Likes, Reposts, Replies int64
}

// excerptRunes bounds a post's text in the block.
const excerptRunes = 280

// TopPostsMarkdown writes posts as issue Markdown under heading: each
// post's image, its text, and a link to it with its counts. It is
// inserted into the body for the writer to edit, so what is previewed is
// what is sent.
func TopPostsMarkdown(heading string, posts []TopPost) string {
	var b strings.Builder
	b.WriteString("## " + Escape(heading) + "\n")
	for _, p := range posts {
		b.WriteString("\n")
		if p.Image != "" && p.Alt != "" {
			b.WriteString("![" + strings.NewReplacer("[", "", "]", "", "\n", " ").Replace(p.Alt) + "](" + p.Image + ")\n\n")
		}
		b.WriteString(Escape(excerpt(p.Text)) + "\n")
		var meta []string
		if p.Link != "" {
			meta = append(meta, "[On "+Escape(p.Network)+"]("+p.Link+")")
		}
		for _, c := range []struct {
			n    int64
			one  string
			many string
		}{{p.Likes, "like", "likes"}, {p.Reposts, "repost", "reposts"}, {p.Replies, "reply", "replies"}} {
			switch {
			case c.n == 1:
				meta = append(meta, "1 "+c.one)
			case c.n > 1:
				meta = append(meta, fmt.Sprintf("%d %s", c.n, c.many))
			}
		}
		if len(meta) > 0 {
			b.WriteString(strings.Join(meta, " · ") + "\n")
		}
	}
	return b.String()
}

// excerpt is a post's text on one line, cut at a word near excerptRunes.
func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= excerptRunes {
		return s
	}
	r := []rune(s)[:excerptRunes]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > excerptRunes/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

// Escape makes s read as plain text in an issue's Markdown: inline markup
// is escaped, and so is anything at its start that would begin a block.
func Escape(s string) string {
	var b strings.Builder
	for i, r := range s {
		if strings.ContainsRune("\\`*_[]", r) || i == 0 && strings.ContainsRune("#>-+!", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	out := b.String()
	// "1. " would start a list.
	if m := numberRE.FindStringIndex(out); m != nil {
		if d := strings.IndexAny(out, ".)"); d > 0 {
			out = out[:d] + "\\" + out[d:]
		}
	}
	return out
}
