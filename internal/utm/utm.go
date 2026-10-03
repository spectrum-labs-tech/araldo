// SPDX-License-Identifier: AGPL-3.0-or-later

// Package utm adds UTM parameters to the links in a post, so the brand's web
// analytics can tell which post and network each visit came from.
package utm

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Params are the values added to each link. Empty values are left out.
type Params struct {
	Source   string // the network, e.g. "bluesky"
	Medium   string // "social"
	Campaign string // the template's key
	Content  string // the post's ID
}

// Tag adds the params to every link in text whose host is one of domains or
// a subdomain of one. A link that already has any utm_ parameter is left as
// it is: whoever wrote it chose its tags.
func Tag(text string, domains []string, p Params) string {
	if len(domains) == 0 {
		return text
	}
	return platform.LinkRE.ReplaceAllStringFunc(text, func(raw string) string { return TagURL(raw, domains, p) })
}

// TagURL adds the params to one link, by the same rules as Tag.
func TagURL(raw string, domains []string, p Params) string {
	u, err := url.Parse(raw)
	if err != nil || !matches(u.Hostname(), domains) || hasUTM(u.RawQuery) {
		return raw
	}
	add := url.Values{}
	for k, v := range map[string]string{"utm_source": p.Source, "utm_medium": p.Medium, "utm_campaign": p.Campaign, "utm_content": p.Content} {
		if v != "" {
			add.Set(k, v)
		}
	}
	if len(add) == 0 {
		return raw
	}
	if u.RawQuery != "" {
		u.RawQuery += "&"
	}
	u.RawQuery += add.Encode()
	return u.String()
}

func matches(host string, domains []string) bool {
	host = strings.ToLower(host)
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func hasUTM(rawQuery string) bool {
	for _, kv := range strings.Split(rawQuery, "&") {
		if strings.HasPrefix(strings.ToLower(kv), "utm_") {
			return true
		}
	}
	return false
}

// MaxDomains bounds a brand's list.
const MaxDomains = 20

// ErrDomain reports a domain that is not a host name.
var ErrDomain = errors.New("not a domain")

// NormalizeDomains trims, lowercases and de-duplicates a list of domains.
// People paste URLs, so a scheme, path or port is dropped; anything else
// that is not a host name is an error naming the entry.
func NormalizeDomains(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if i := strings.Index(d, "://"); i >= 0 {
			d = d[i+3:]
		}
		if i := strings.IndexAny(d, "/?#"); i >= 0 {
			d = d[:i]
		}
		if h, _, err := net.SplitHostPort(d); err == nil {
			d = h
		}
		d = strings.TrimPrefix(strings.TrimSuffix(d, "."), "www.")
		if !validHost(d) {
			return nil, &DomainError{Domain: d}
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out, nil
}

// DomainError names the entry that is not a domain.
type DomainError struct{ Domain string }

func (e *DomainError) Error() string { return "utm: " + e.Domain + " is not a domain" }

// Unwrap lets errors.Is match ErrDomain.
func (e *DomainError) Unwrap() error { return ErrDomain }

func validHost(h string) bool {
	if len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

// Token makes a value a short lowercase token ("Alpha launch!" becomes
// "alpha-launch"): what the link builder writes in utm_campaign and
// utm_content for ads, and what reading analytics matches an ad campaign's
// name against (ADR 0025), so the two cannot drift apart.
func Token(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 64 {
		out = strings.TrimRight(out[:64], "-")
	}
	return out
}

// Mediums Araldo writes besides "social" for posts.
const (
	// Paid is utm_medium on ad links.
	Paid = "paid"
	// Email is utm_medium on newsletter links (ADR 0024 decision 10).
	Email = "email"
)
