// SPDX-License-Identifier: AGPL-3.0-or-later

package utm

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestTag(t *testing.T) {
	t.Parallel()
	p := Params{Source: "bluesky", Medium: "social", Campaign: "brand-spotlight", Content: "post_123"}
	const tags = "utm_campaign=brand-spotlight&utm_content=post_123&utm_medium=social&utm_source=bluesky"
	domains := []string{"araldo.dev"}
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "tags a listed domain", text: "See https://araldo.dev/brands/10 now",
			want: "See https://araldo.dev/brands/10?" + tags + " now"},
		{name: "keeps an existing query and fragment", text: "https://araldo.dev/b?ref=x#top",
			want: "https://araldo.dev/b?ref=x&" + tags + "#top"},
		{name: "subdomains count", text: "https://staging.araldo.dev/b.",
			want: "https://staging.araldo.dev/b?" + tags + "."},
		{name: "other domains are left alone", text: "https://example.com/araldo.dev",
			want: "https://example.com/araldo.dev"},
		{name: "a lookalike is not a subdomain", text: "https://notaraldo.dev/b",
			want: "https://notaraldo.dev/b"},
		{name: "links already tagged are left alone", text: "https://araldo.dev/b?UTM_source=newsletter",
			want: "https://araldo.dev/b?UTM_source=newsletter"},
		{name: "every link", text: "https://araldo.dev/a and https://araldo.dev/b",
			want: "https://araldo.dev/a?" + tags + " and https://araldo.dev/b?" + tags},
		{name: "no links", text: "just words", want: "just words"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Tag(tt.text, domains, p); got != tt.want {
				t.Errorf("Tag(%q)\n got %q\nwant %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestTagSkipsEmptyValuesAndLists(t *testing.T) {
	t.Parallel()
	if got := Tag("https://araldo.dev/b", nil, Params{Source: "x"}); got != "https://araldo.dev/b" {
		t.Errorf("no domains: %q", got)
	}
	if got := Tag("https://araldo.dev/b", []string{"araldo.dev"}, Params{Source: "x", Medium: "social"}); got != "https://araldo.dev/b?utm_medium=social&utm_source=x" {
		t.Errorf("empty campaign and content: %q", got)
	}
}

func TestNormalizeDomains(t *testing.T) {
	t.Parallel()
	got, err := NormalizeDomains([]string{" ARALDO.dev ", "https://www.araldo.dev/brands", "shop.example.com:443", "", "araldo.dev."})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"araldo.dev", "shop.example.com"}; !slices.Equal(got, want) {
		t.Errorf("NormalizeDomains = %q, want %q", got, want)
	}
	for _, bad := range []string{"localhost", "not a domain", "-bad.com", "a..b", "exa_mple.com"} {
		_, err := NormalizeDomains([]string{bad})
		var de *DomainError
		if !errors.Is(err, ErrDomain) || !errors.As(err, &de) {
			t.Errorf("NormalizeDomains(%q) err = %v, want a DomainError", bad, err)
		}
	}
	if got, err := NormalizeDomains(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("NormalizeDomains(nil) = %#v, %v; want an empty, non-nil list", got, err)
	}
}

func TestToken(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Alpha Launch": "alpha-launch", "r/LocalLLaMA": "r-localllama", "v1.2_final": "v1.2_final", "  --  ": "", "Café": "caf",
		strings.Repeat("ab ", 40): strings.TrimRight(strings.Repeat("ab-", 22)[:64], "-"),
	} {
		if got := Token(in); got != want {
			t.Errorf("Token(%q) = %q, want %q", in, got, want)
		}
	}
}
