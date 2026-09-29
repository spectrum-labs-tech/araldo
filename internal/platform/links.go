// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"net/url"
	"regexp"
	"strings"
)

// LinkRE finds links in text. A trailing punctuation mark ends the
// sentence, not the link.
var LinkRE = regexp.MustCompile(`https?://[^\s<>"]+[^\s<>".,;:!?)\]]`)

// Link is a link in a Bluesky post: where its short form sits in the text
// (UTF-8 byte offsets, as the protocol requires) and the full URL it opens.
type Link struct {
	Start, End int
	URL        string
}

// ShortenLinks replaces each link with the short form the Bluesky app shows
// (ShortLink) and reports where the links are. The result is the text that
// is posted and measured; the full URLs travel in link facets, so long
// links, such as ones with UTM parameters, cost nothing against the limit.
func ShortenLinks(text string) (string, []Link) {
	var sb strings.Builder
	var links []Link
	last := 0
	for _, m := range LinkRE.FindAllStringIndex(text, -1) {
		sb.WriteString(text[last:m[0]])
		full := text[m[0]:m[1]]
		start := sb.Len()
		sb.WriteString(ShortLink(full))
		links = append(links, Link{Start: start, End: sb.Len(), URL: full})
		last = m[1]
	}
	sb.WriteString(text[last:])
	return sb.String(), links
}

// ShortLink is how the Bluesky app shows a link: the host, then the path,
// query and fragment cut after 13 characters. It mirrors toShortUrl in
// @atproto/api.
func ShortLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return raw
	}
	path := u.EscapedPath()
	if path == "/" {
		path = ""
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		path += "#" + u.EscapedFragment()
	}
	if len(path) > 15 {
		return u.Host + path[:13] + "..."
	}
	return u.Host + path
}
