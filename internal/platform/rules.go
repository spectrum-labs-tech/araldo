// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
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
	// Video is what the platform takes as video, once its adapter can post
	// it; nil means no video (ADR 0027).
	Video *VideoRules
}

// VideoRules are a platform's limits on video, each from its own
// documentation (Source).
type VideoRules struct {
	// Types maps each video type to its size limit in bytes; 0 means none
	// is documented.
	Types                    map[string]int64
	MinDuration, MaxDuration time.Duration
	MinAspect, MaxAspect     float64
	MaxFrameRate             float64
	// Codecs are the video codecs it takes, as sample entry codes ("avc1"
	// is H.264); empty means any.
	Codecs []string
	Source string
}

var rules = map[Provider]Rules{
	X: {
		Provider: X, Name: "X", MaxLength: 280, Counting: CountXWeighted, Threads: true, MaxThreadParts: 25, MaxMedia: 4,
		Source:      "https://docs.x.com/fundamentals/counting-characters",
		Images:      map[string]int64{media.JPEG: 5_000_000, media.PNG: 5_000_000, media.WebP: 5_000_000, media.GIF: 15_000_000},
		ImageSource: "https://developer.x.com/en/docs/x-api/v1/media/upload-media/uploading-media/media-best-practices",
		// MP4 or MOV, H.264, up to 512 MB, 0.5 to 140 seconds, 1:3 to 3:1,
		// at most 60 frames a second (for accounts without longer uploads).
		Video: &VideoRules{Types: map[string]int64{media.MP4: 512 << 20, media.QuickTime: 512 << 20},
			MinDuration: 500 * time.Millisecond, MaxDuration: 140 * time.Second, MinAspect: 1.0 / 3, MaxAspect: 3, MaxFrameRate: 60,
			Codecs: []string{"avc1", "avc3"},
			Source: "https://developer.x.com/en/docs/x-api/v1/media/upload-media/uploading-media/media-best-practices"},
	},
	Bluesky: {
		Provider: Bluesky, Name: "Bluesky", MaxLength: 300, Counting: CountBluesky, Threads: true, MaxThreadParts: 25, MaxMedia: 4,
		Source:      "https://docs.bsky.app/docs/advanced-guides/post-richtext",
		Images:      map[string]int64{media.JPEG: 1_000_000, media.PNG: 1_000_000, media.WebP: 1_000_000, media.GIF: 1_000_000},
		ImageSource: "https://github.com/bluesky-social/atproto/blob/main/lexicons/app/bsky/embed/images.json (maxSize)",
		// The embed takes an MP4 blob of up to 100,000,000 bytes; Bluesky's
		// video service takes up to three minutes.
		Video: &VideoRules{Types: map[string]int64{media.MP4: 100_000_000}, MaxDuration: 3 * time.Minute,
			Source: "https://github.com/bluesky-social/atproto/blob/main/lexicons/app/bsky/embed/video.json (maxSize); " +
				"https://docs.bsky.app/docs/tutorials/video"},
	},
	Mastodon: {
		Provider: Mastodon, Name: "Mastodon", MaxLength: 500, Counting: CountMastodon, Threads: true, MaxThreadParts: 25, MaxMedia: 4,
		Source:      "https://docs.joinmastodon.org/user/posting/ (default instance limit; servers may raise it)",
		Images:      map[string]int64{media.JPEG: 16 << 20, media.PNG: 16 << 20, media.WebP: 16 << 20, media.GIF: 16 << 20},
		ImageSource: "https://docs.joinmastodon.org/entities/Instance/#image_size_limit (default; servers may change it)",
		// The default video_size_limit; servers may change it.
		Video: &VideoRules{Types: map[string]int64{media.MP4: 99 << 20, media.QuickTime: 99 << 20},
			Source: "https://docs.joinmastodon.org/entities/Instance/#video_size_limit (default; servers may change it)"},
	},
	Gab: {
		Provider: Gab, Name: "Gab", MaxLength: 3000, Counting: CountMastodon, Threads: true, MaxThreadParts: 25, MaxMedia: 4,
		// Gab Social is a Mastodon fork: it enforces Mastodon's status length
		// validator, which counts code points and every URL as 23, over a limit it
		// raised to 3000. Gab publishes no API reference of its own, so the shape is
		// cited from Mastodon's and the limit from Gab's own composer.
		Source: "https://docs.joinmastodon.org/methods/statuses/ (Gab Social is a Mastodon fork; its own limit is 3000)",
		// Sizes are left at 0 -- undocumented -- rather than guessed: Gab states no
		// image_size_limit, and inventing one would reject images Gab accepts or
		// pass images it refuses. The types are Mastodon's.
		Images:      map[string]int64{media.JPEG: 0, media.PNG: 0, media.WebP: 0, media.GIF: 0},
		ImageSource: "https://docs.joinmastodon.org/methods/media/ (Gab Social is a Mastodon fork; it documents no size limit)",
		// As for images: Mastodon's types, and no documented size.
		Video: &VideoRules{Types: map[string]int64{media.MP4: 0, media.QuickTime: 0},
			Source: "https://docs.joinmastodon.org/methods/media/ (Gab Social is a Mastodon fork; it documents no video limits)"},
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
		// MP4 up to 500 MB, 3 seconds to 30 minutes.
		Video: &VideoRules{Types: map[string]int64{media.MP4: 500 << 20}, MinDuration: 3 * time.Second, MaxDuration: 30 * time.Minute,
			Source: "https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/videos-api"},
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
	Pinterest: {
		// A pin's description holds 800 characters and its title 100; the
		// adapter takes a short first line as the title, so the whole text is
		// held to the description's limit. One image per pin, and one is
		// required; the API takes JPEG and PNG as base64, up to 20 MB.
		Provider: Pinterest, Name: "Pinterest", MaxLength: 800, Counting: CountRunes, MediaRequired: true, MaxMedia: 1,
		Source:      "https://developers.pinterest.com/docs/api/v5/pins-create",
		Images:      map[string]int64{media.JPEG: 20_000_000, media.PNG: 20_000_000},
		ImageSource: "https://help.pinterest.com/en/business/article/pinterest-product-specs",
	},
	Discord: {
		Provider: Discord, Name: "Discord", MaxLength: 2000, Counting: CountRunes, MaxMedia: 10,
		Source:      "https://discord.com/developers/docs/resources/webhook#execute-webhook",
		Images:      map[string]int64{media.JPEG: 10 << 20, media.PNG: 10 << 20, media.WebP: 10 << 20, media.GIF: 10 << 20},
		ImageSource: "https://discord.com/developers/docs/reference#uploading-files",
		// Attachments, video included, share the same default upload limit.
		Video: &VideoRules{Types: map[string]int64{media.MP4: 10 << 20, media.QuickTime: 10 << 20},
			Source: "https://discord.com/developers/docs/reference#uploading-files"},
	},
	Telegram: {
		Provider: Telegram, Name: "Telegram", MaxLength: 4096, Counting: CountRunes, MaxMedia: 10,
		Source:    "https://core.telegram.org/bots/api#sendmessage",
		Images:    map[string]int64{media.JPEG: 10_000_000, media.PNG: 10_000_000, media.WebP: 10_000_000},
		MinAspect: 1.0 / 20, MaxAspect: 20, MaxDimensions: 10_000, MaxCaption: 1024,
		ImageSource: "https://core.telegram.org/bots/api#sendphoto, #sendmediagroup (captions: 1024 characters)",
		// Bots upload files of up to 50 MB; Telegram plays MPEG4 video.
		Video: &VideoRules{Types: map[string]int64{media.MP4: 50_000_000}, Source: "https://core.telegram.org/bots/api#sendvideo"},
	},
}

// RulesFor returns a provider's rules.
func RulesFor(p Provider) (Rules, bool) {
	r, ok := rules[p]
	return r, ok
}

// Emulable lists the providers the sandbox can imitate.
func Emulable() []Provider {
	return []Provider{Bluesky, Mastodon, Gab, X, Threads, LinkedIn, Facebook, Instagram, Pinterest, Discord, Telegram}
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
	hasVideo := false
	for _, m := range media {
		hasVideo = hasVideo || m.IsVideo()
	}
	if hasVideo && len(media) > 1 {
		out = append(out, Violation{Code: "video_alone", Message: "a post with a video carries nothing else", Length: len(media), Limit: 1})
	}
	if r.MaxMedia >= 0 && len(media) > r.MaxMedia {
		msg := r.Name + " takes at most " + strconv.Itoa(r.MaxMedia) + " images"
		if r.MaxMedia == 0 {
			msg = r.Name + " takes no images"
		}
		out = append(out, Violation{Code: "too_much_media", Message: msg, Length: len(media), Limit: r.MaxMedia})
	}
	for i, m := range media {
		if m.IsVideo() {
			out = append(out, r.checkVideo(i+1, m)...)
		} else {
			out = append(out, r.checkImage(i+1, m)...)
		}
	}
	return out
}

func (r Rules) checkVideo(pos int, m Media) []Violation {
	v := r.Video
	if v == nil {
		return []Violation{{Code: "video_unsupported", Media: pos, Message: r.Name + " posts take no video in Araldo yet"}}
	}
	limit, ok := v.Types[m.Type]
	if !ok {
		return []Violation{{Code: "media_type_unsupported", Media: pos,
			Message: fmt.Sprintf("%s does not take %s video", r.Name, strings.TrimPrefix(m.Type, "video/"))}}
	}
	var out []Violation
	if limit > 0 && m.Size > limit {
		out = append(out, Violation{Code: "media_too_large", Media: pos, Length: int(min(m.Size, math.MaxInt32)), Limit: int(min(limit, math.MaxInt32)),
			Message: fmt.Sprintf("%s takes videos up to %s; video %d is %s", r.Name, humanBytes(limit), pos, humanBytes(m.Size))})
	}
	switch {
	case v.MinDuration > 0 && m.Duration < v.MinDuration:
		out = append(out, Violation{Code: "video_too_short", Media: pos,
			Message: fmt.Sprintf("%s takes videos of at least %s; video %d is %s", r.Name, v.MinDuration, pos, m.Duration.Round(time.Millisecond))})
	case v.MaxDuration > 0 && m.Duration > v.MaxDuration:
		out = append(out, Violation{Code: "video_too_long", Media: pos,
			Message: fmt.Sprintf("%s takes videos up to %s; video %d is %s", r.Name, v.MaxDuration, pos, m.Duration.Round(time.Second))})
	}
	if m.Width > 0 && m.Height > 0 {
		aspect := float64(m.Width) / float64(m.Height)
		if v.MinAspect > 0 && aspect < v.MinAspect-0.005 || v.MaxAspect > 0 && aspect > v.MaxAspect+0.005 {
			out = append(out, Violation{Code: "media_aspect_ratio", Media: pos,
				Message: fmt.Sprintf("%s takes videos from %s to %s (width:height); video %d is %d×%d",
					r.Name, ratio(v.MinAspect), ratio(v.MaxAspect), pos, m.Width, m.Height)})
		}
	}
	if v.MaxFrameRate > 0 && m.FrameRate > v.MaxFrameRate+0.01 {
		out = append(out, Violation{Code: "video_frame_rate", Media: pos,
			Message: fmt.Sprintf("%s takes up to %g frames a second; video %d has %g", r.Name, v.MaxFrameRate, pos, m.FrameRate)})
	}
	if len(v.Codecs) > 0 && !slices.Contains(v.Codecs, m.VideoCodec) {
		out = append(out, Violation{Code: "video_codec_unsupported", Media: pos,
			Message: fmt.Sprintf("%s takes %s video; video %d is %s: export it as H.264", r.Name, codecNames(v.Codecs), pos, codecName(m.VideoCodec))})
	}
	return out
}

// codecName names a video sample entry code.
func codecName(c string) string {
	switch c {
	case "avc1", "avc3":
		return "H.264"
	case "hvc1", "hev1":
		return "HEVC"
	case "av01":
		return "AV1"
	case "vp09":
		return "VP9"
	}
	return c
}

func codecNames(cs []string) string {
	var names []string
	for _, c := range cs {
		if n := codecName(c); !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return strings.Join(names, " or ")
}

// Resize is how an image must change to fit a platform (ADR 0027):
// re-encoded as a JPEG of at most MaxBytes (0: any size) whose width plus
// height is at most MaxSum (0: any).
type Resize struct {
	MaxBytes int64
	MaxSum   int
	// Why says what does not fit, for the preview's notice.
	Why string
}

// ResizeFor reports whether an image must be resized to fit the
// platform's type, size and dimension limits, and how. It is not needed
// when the image fits; it cannot be done for a GIF, a transparent image,
// one over media.MaxPixels, or a platform that takes no JPEGs.
func (r Rules) ResizeFor(m Media) (rs Resize, needed, possible bool) {
	if m.IsVideo() {
		return rs, false, true // video is never transcoded
	}
	limit, accepted := r.Images[m.Type]
	switch {
	case !accepted:
		rs.Why = r.Name + " does not take " + strings.TrimPrefix(m.Type, "image/") + " images"
	case limit > 0 && m.Size > limit:
		rs.Why = fmt.Sprintf("%s takes images up to %s; this is %s", r.Name, humanBytes(limit), humanBytes(m.Size))
	case r.MaxDimensions > 0 && m.Width+m.Height > r.MaxDimensions:
		rs.Why = fmt.Sprintf("%s takes images whose width plus height is at most %d; this is %d×%d", r.Name, r.MaxDimensions, m.Width, m.Height)
	default:
		return rs, false, true
	}
	jpegLimit, takesJPEG := r.Images[media.JPEG]
	if !takesJPEG || !media.Resizable(media.Info{Type: m.Type, Width: m.Width, Height: m.Height, Transparent: m.Transparent}) {
		return rs, true, false
	}
	rs.MaxBytes, rs.MaxSum = jpegLimit, r.MaxDimensions
	return rs, true, true
}

// Notices lists what Araldo will change for the platform, image by image:
// resizing or converting one that does not fit as it is.
func (r Rules) Notices(media []Media) []Violation {
	out := []Violation{}
	for i, m := range media {
		if rs, needed, possible := r.ResizeFor(m); needed && possible {
			out = append(out, Violation{Code: "media_resized", Media: i + 1,
				Message: fmt.Sprintf("image %d will be resized into a JPEG for %s: %s", i+1, r.Name, rs.Why)})
		}
	}
	return out
}

func (r Rules) checkImage(pos int, m Media) []Violation {
	_, needed, possible := r.ResizeFor(m)
	if needed && possible {
		// Resizing fixes the type, size and dimensions; the shape stays.
		return r.checkAspect(pos, m)
	}
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
			Message: fmt.Sprintf("%s takes images up to %s; image %d is %s%s", r.Name, humanBytes(limit), pos, humanBytes(m.Size), whyNotResized(m))})
	}
	out = append(out, r.checkAspect(pos, m)...)
	if m.Width > 0 && m.Height > 0 && r.MaxDimensions > 0 && m.Width+m.Height > r.MaxDimensions {
		out = append(out, Violation{Code: "media_dimensions", Media: pos, Length: m.Width + m.Height, Limit: r.MaxDimensions,
			Message: fmt.Sprintf("%s takes images whose width plus height is at most %d; image %d is %d×%d%s",
				r.Name, r.MaxDimensions, pos, m.Width, m.Height, whyNotResized(m))})
	}
	return out
}

func (r Rules) checkAspect(pos int, m Media) []Violation {
	if m.Width <= 0 || m.Height <= 0 {
		return nil
	}
	aspect := float64(m.Width) / float64(m.Height)
	if r.MinAspect > 0 && aspect < r.MinAspect-0.005 || r.MaxAspect > 0 && aspect > r.MaxAspect+0.005 {
		return []Violation{{Code: "media_aspect_ratio", Media: pos,
			Message: fmt.Sprintf("%s takes images from %s to %s (width:height); image %d is %d×%d",
				r.Name, ratio(r.MinAspect), ratio(r.MaxAspect), pos, m.Width, m.Height)}}
	}
	return nil
}

// whyNotResized says why Araldo could not shrink an image itself.
func whyNotResized(m Media) string {
	switch {
	case m.Type == media.GIF:
		return " (GIFs are not resized, to keep their animation)"
	case m.Transparent:
		return " (it has transparent pixels, which a resized JPEG cannot keep)"
	case int64(m.Width)*int64(m.Height) > media.MaxPixels:
		return " (it has more than 50 megapixels, too many to resize)"
	}
	return ""
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
