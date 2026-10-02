// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"

	"github.com/spectrum-labs-tech/araldo/internal/media"
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
	// Images maps each image type the platform takes to its size limit in
	// bytes; 0 means none is documented.
	Images map[string]int64
	// MinAspect and MaxAspect bound an image's width divided by its
	// height; MaxDimensions bounds its width plus height. 0 is unchecked.
	MinAspect, MaxAspect float64
	MaxDimensions        int
	// MaxCaption, when set, replaces MaxLength for a post with media: the
	// text becomes the image's caption.
	MaxCaption  int
	ImageSource string
}

var rules = map[Provider]Rules{
	X: {
		Provider: X, Name: "X", MaxLength: 280, Counting: CountXWeighted, Threads: true, MaxThreadParts: 25, MaxMedia: 4,
		Source:      "https://docs.x.com/fundamentals/counting-characters",
		Images:      map[string]int64{media.JPEG: 5_000_000, media.PNG: 5_000_000, media.WebP: 5_000_000, media.GIF: 15_000_000},
		ImageSource: "https://developer.x.com/en/docs/x-api/v1/media/upload-media/uploading-media/media-best-practices",
	},
	Bluesky: {
		Provider: Bluesky, Name: "Bluesky", MaxLength: 300, Counting: CountBluesky, Threads: true, MaxThreadParts: 25, MaxMedia: 4,
		Source:      "https://docs.bsky.app/docs/advanced-guides/post-richtext",
		Images:      map[string]int64{media.JPEG: 1_000_000, media.PNG: 1_000_000, media.WebP: 1_000_000, media.GIF: 1_000_000},
		ImageSource: "https://github.com/bluesky-social/atproto/blob/main/lexicons/app/bsky/embed/images.json (maxSize)",
	},
	Mastodon: {
		Provider: Mastodon, Name: "Mastodon", MaxLength: 500, Counting: CountMastodon, Threads: true, MaxThreadParts: 25, MaxMedia: 4,
		Source:      "https://docs.joinmastodon.org/user/posting/ (default instance limit; servers may raise it)",
		Images:      map[string]int64{media.JPEG: 16 << 20, media.PNG: 16 << 20, media.WebP: 16 << 20, media.GIF: 16 << 20},
		ImageSource: "https://docs.joinmastodon.org/entities/Instance/#image_size_limit (default; servers may change it)",
	},
	Threads: {
		Provider: Threads, Name: "Threads", MaxLength: 500, Counting: CountRunes, Threads: true, MaxThreadParts: 25, MaxMedia: 10,
		Source:    "https://developers.facebook.com/docs/threads/overview",
		Images:    map[string]int64{media.JPEG: 8_000_000, media.PNG: 8_000_000},
		MinAspect: 0.1, MaxAspect: 10,
		ImageSource: "https://developers.facebook.com/docs/threads/overview#image-specifications",
	},
	LinkedIn: {
		Provider: LinkedIn, Name: "LinkedIn", MaxLength: 3000, Counting: CountRunes, MaxMedia: 9,
		Source:      "https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/posts-api",
		Images:      map[string]int64{media.JPEG: 0, media.PNG: 0, media.GIF: 0},
		ImageSource: "https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/images-api",
	},
	Facebook: {
		Provider: Facebook, Name: "Facebook", MaxLength: 63206, Counting: CountRunes, MaxMedia: 10,
		Source:      "https://developers.facebook.com/docs/pages-api/posts",
		Images:      map[string]int64{media.JPEG: 4_000_000, media.PNG: 4_000_000, media.GIF: 4_000_000},
		ImageSource: "https://developers.facebook.com/docs/graph-api/reference/page/photos/",
	},
	Instagram: {
		Provider: Instagram, Name: "Instagram", MaxLength: 2200, Counting: CountRunes, MediaRequired: true, MaxMedia: 10,
		Source:    "https://developers.facebook.com/docs/instagram-platform/content-publishing",
		Images:    map[string]int64{media.JPEG: 8_000_000},
		MinAspect: 4.0 / 5, MaxAspect: 1.91,
		ImageSource: "https://developers.facebook.com/docs/instagram-platform/instagram-graph-api/reference/ig-user/media#image-specifications",
	},
	Discord: {
		Provider: Discord, Name: "Discord", MaxLength: 2000, Counting: CountRunes, MaxMedia: 10,
		Source:      "https://discord.com/developers/docs/resources/webhook#execute-webhook",
		Images:      map[string]int64{media.JPEG: 10 << 20, media.PNG: 10 << 20, media.WebP: 10 << 20, media.GIF: 10 << 20},
		ImageSource: "https://discord.com/developers/docs/reference#uploading-files",
	},
	Telegram: {
		Provider: Telegram, Name: "Telegram", MaxLength: 4096, Counting: CountRunes, MaxMedia: 10,
		Source:    "https://core.telegram.org/bots/api#sendmessage",
		Images:    map[string]int64{media.JPEG: 10_000_000, media.PNG: 10_000_000, media.WebP: 10_000_000},
		MinAspect: 1.0 / 20, MaxAspect: 20, MaxDimensions: 10_000, MaxCaption: 1024,
		ImageSource: "https://core.telegram.org/bots/api#sendphoto, #sendmediagroup (captions: 1024 characters)",
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
	// Media is the 1-based position of the media item at fault.
	Media  int `json:"media,omitempty"`
	Length int `json:"length,omitempty"`
	Limit  int `json:"limit,omitempty"`
}

// ForMedia returns the rules for a post carrying n media items: on a
// platform where the text becomes a caption, its limit is the caption's.
func (r Rules) ForMedia(n int) Rules {
	if n > 0 && r.MaxCaption > 0 {
		r.MaxLength = r.MaxCaption
	}
	return r
}

// Check returns every way parts and media break r. Call it on
// r.ForMedia(len(media)).
func (r Rules) Check(parts []string, media []Media) []Violation {
	var out []Violation
	empty := len(parts) == 0 || len(parts) == 1 && strings.TrimSpace(parts[0]) == ""
	if empty && len(media) == 0 {
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
	if r.MediaRequired && len(media) == 0 {
		out = append(out, Violation{Code: "media_required", Message: r.Name + " posts need an image or video"})
	}
	if r.MaxMedia >= 0 && len(media) > r.MaxMedia {
		msg := r.Name + " takes at most " + strconv.Itoa(r.MaxMedia) + " images"
		if r.MaxMedia == 0 {
			msg = r.Name + " takes no images"
		}
		out = append(out, Violation{Code: "too_much_media", Message: msg, Length: len(media), Limit: r.MaxMedia})
	}
	for i, m := range media {
		out = append(out, r.checkImage(i+1, m)...)
	}
	return out
}

func (r Rules) checkImage(pos int, m Media) []Violation {
	limit, ok := r.Images[m.Type]
	if !ok {
		var types []string
		for t := range r.Images {
			types = append(types, strings.TrimPrefix(t, "image/"))
		}
		sort.Strings(types)
		return []Violation{{Code: "media_type_unsupported", Media: pos,
			Message: fmt.Sprintf("%s does not take %s images (it takes %s)", r.Name, strings.TrimPrefix(m.Type, "image/"), strings.Join(types, ", "))}}
	}
	var out []Violation
	if limit > 0 && m.Size > limit {
		out = append(out, Violation{Code: "media_too_large", Media: pos, Length: int(m.Size), Limit: int(limit),
			Message: fmt.Sprintf("%s takes images up to %s; image %d is %s", r.Name, humanBytes(limit), pos, humanBytes(m.Size))})
	}
	if m.Width > 0 && m.Height > 0 {
		aspect := float64(m.Width) / float64(m.Height)
		if r.MinAspect > 0 && aspect < r.MinAspect-0.005 || r.MaxAspect > 0 && aspect > r.MaxAspect+0.005 {
			out = append(out, Violation{Code: "media_aspect_ratio", Media: pos,
				Message: fmt.Sprintf("%s takes images from %s to %s (width:height); image %d is %d×%d",
					r.Name, ratio(r.MinAspect), ratio(r.MaxAspect), pos, m.Width, m.Height)})
		}
		if r.MaxDimensions > 0 && m.Width+m.Height > r.MaxDimensions {
			out = append(out, Violation{Code: "media_dimensions", Media: pos, Length: m.Width + m.Height, Limit: r.MaxDimensions,
				Message: fmt.Sprintf("%s takes images whose width plus height is at most %d; image %d is %d×%d",
					r.Name, r.MaxDimensions, pos, m.Width, m.Height)})
		}
	}
	return out
}

// ratio writes an aspect ratio the way platforms document it: 4:5, 1.91:1.
func ratio(f float64) string {
	for _, d := range []int{1, 2, 3, 4, 5, 10, 20} {
		n := f * float64(d)
		if r := math.Round(n); r >= 1 && math.Abs(n-r) < 1e-9 {
			return fmt.Sprintf("%d:%d", int(r), d)
		}
	}
	return strconv.FormatFloat(f, 'f', -1, 64) + ":1"
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1_000_000:
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64), ".0") + " MB"
	case n >= 1000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 0, 64) + " kB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}
