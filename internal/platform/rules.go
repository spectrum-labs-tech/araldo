// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// Counting is how a platform measures text length.
type Counting string

// Counting methods.
const (
	// CountRunes counts Unicode code points.
	CountRunes Counting = "characters"
	// CountGraphemes counts user-perceived characters (an emoji with
	// modifiers is one).
	CountGraphemes Counting = "graphemes"
	// CountXWeighted is X's weighted count: most Latin text weighs 1, other
	// scripts and emoji 2, and every URL 23 (twitter-text v3).
	CountXWeighted Counting = "x_weighted"
	// CountMastodon counts code points, but every URL as 23.
	CountMastodon Counting = "mastodon"
	// CountBluesky counts graphemes of the text as posted, where each link
	// is its short form (ShortenLinks) and the full URL is in a facet.
	CountBluesky Counting = "bluesky"
)

// Rules are a platform's limits (ADR 0009). Every limit cites its source.
type Rules struct {
	Provider  Provider
	Name      string
	MaxLength int
	Counting  Counting
	// Threads reports whether long content can be split into a thread of
	// replies; MaxThreadParts bounds it.
	Threads        bool
	MaxThreadParts int
	MediaRequired  bool
	MaxMedia       int
	Source         string
}

var rules = map[Provider]Rules{
	X: {
		Provider: X, Name: "X", MaxLength: 280, Counting: CountXWeighted, Threads: true, MaxThreadParts: 25, MaxMedia: 4,
		Source: "https://docs.x.com/fundamentals/counting-characters",
	},
	Bluesky: {
		Provider: Bluesky, Name: "Bluesky", MaxLength: 300, Counting: CountBluesky, Threads: true, MaxThreadParts: 25, MaxMedia: 4,
		Source: "https://docs.bsky.app/docs/advanced-guides/post-richtext",
	},
	Mastodon: {
		Provider: Mastodon, Name: "Mastodon", MaxLength: 500, Counting: CountMastodon, Threads: true, MaxThreadParts: 25, MaxMedia: 4,
		Source: "https://docs.joinmastodon.org/user/posting/ (default instance limit; servers may raise it)",
	},
	Threads: {
		Provider: Threads, Name: "Threads", MaxLength: 500, Counting: CountRunes, Threads: true, MaxThreadParts: 25, MaxMedia: 10,
		Source: "https://developers.facebook.com/docs/threads/overview",
	},
	LinkedIn: {
		Provider: LinkedIn, Name: "LinkedIn", MaxLength: 3000, Counting: CountRunes, MaxMedia: 9,
		Source: "https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/posts-api",
	},
	Facebook: {
		Provider: Facebook, Name: "Facebook", MaxLength: 63206, Counting: CountRunes, MaxMedia: 10,
		Source: "https://developers.facebook.com/docs/pages-api/posts",
	},
	Instagram: {
		Provider: Instagram, Name: "Instagram", MaxLength: 2200, Counting: CountRunes, MediaRequired: true, MaxMedia: 10,
		Source: "https://developers.facebook.com/docs/instagram-platform/content-publishing",
	},
	Discord: {
		Provider: Discord, Name: "Discord", MaxLength: 2000, Counting: CountRunes, MaxMedia: 10,
		Source: "https://discord.com/developers/docs/resources/webhook#execute-webhook",
	},
	Telegram: {
		Provider: Telegram, Name: "Telegram", MaxLength: 4096, Counting: CountRunes,
		Source: "https://core.telegram.org/bots/api#sendmessage",
	},
}

// RulesFor returns a provider's rules.
func RulesFor(p Provider) (Rules, bool) {
	r, ok := rules[p]
	return r, ok
}

// Emulable lists the providers the sandbox can imitate.
func Emulable() []Provider {
	return []Provider{Bluesky, Mastodon, X, Threads, LinkedIn, Facebook, Instagram, Discord, Telegram}
}

var urlRE = regexp.MustCompile(`https?://[^\s<>"]+`)

// urlWeight is how much a URL counts on X and Mastodon, however long.
const urlWeight = 23

// Length measures s the way the platform does.
func (r Rules) Length(s string) int {
	switch r.Counting {
	case CountGraphemes:
		return uniseg.GraphemeClusterCount(s)
	case CountBluesky:
		short, _ := ShortenLinks(s)
		return uniseg.GraphemeClusterCount(short)
	case CountXWeighted:
		return xWeighted(s)
	case CountMastodon:
		n := 0
		rest := urlRE.ReplaceAllStringFunc(s, func(string) string { n += urlWeight; return "" })
		return n + utf8.RuneCountInString(rest)
	default:
		return utf8.RuneCountInString(s)
	}
}

// xWeighted implements twitter-text v3's weighting: code points in these
// ranges weigh 1, everything else 2; an emoji sequence weighs 2 in total;
// a URL weighs 23.
func xWeighted(s string) int {
	n := 0
	rest := urlRE.ReplaceAllStringFunc(s, func(string) string { n += urlWeight; return "\x00" })
	g := uniseg.NewGraphemes(rest)
	for g.Next() {
		runes := g.Runes()
		if len(runes) == 1 && runes[0] == 0 {
			continue // a URL placeholder, already counted
		}
		if isEmoji(runes) {
			n += 2
			continue
		}
		for _, r := range runes {
			n += xRuneWeight(r)
		}
	}
	return n
}

func xRuneWeight(r rune) int {
	switch {
	case r <= 0x10FF, r >= 0x2000 && r <= 0x200D, r >= 0x2010 && r <= 0x201F, r >= 0x2032 && r <= 0x2037:
		return 1
	}
	return 2
}

func isEmoji(runes []rune) bool {
	for _, r := range runes {
		if r >= 0x1F000 && r <= 0x1FAFF || r >= 0x2600 && r <= 0x27BF {
			return true
		}
	}
	return false
}

// Violation is one way content breaks a platform's rules.
type Violation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Part    int    `json:"part,omitempty"`
	Length  int    `json:"length,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// Check returns every way parts break r.
func (r Rules) Check(parts []string, media int) []Violation {
	var out []Violation
	if len(parts) == 0 || len(parts) == 1 && strings.TrimSpace(parts[0]) == "" {
		out = append(out, Violation{Code: "empty", Message: "the post has no text"})
	}
	if len(parts) > 1 && !r.Threads {
		out = append(out, Violation{Code: "threads_unsupported", Message: r.Name + " does not support threads"})
	}
	if r.Threads && r.MaxThreadParts > 0 && len(parts) > r.MaxThreadParts {
		out = append(out, Violation{Code: "thread_too_long", Message: "too many thread parts", Length: len(parts), Limit: r.MaxThreadParts})
	}
	for i, p := range parts {
		if n := r.Length(p); n > r.MaxLength {
			out = append(out, Violation{
				Code: "too_long", Part: i + 1, Length: n, Limit: r.MaxLength,
				Message: r.Name + " allows " + strconv.Itoa(r.MaxLength) + " " + string(r.Counting) + "; this has " + strconv.Itoa(n),
			})
		}
	}
	if r.MediaRequired && media == 0 {
		out = append(out, Violation{Code: "media_required", Message: r.Name + " posts need an image or video"})
	}
	if r.MaxMedia >= 0 && media > r.MaxMedia {
		out = append(out, Violation{Code: "too_much_media", Message: "too many media items", Length: media, Limit: r.MaxMedia})
	}
	return out
}
