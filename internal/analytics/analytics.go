// SPDX-License-Identifier: AGPL-3.0-or-later

// Package analytics reads the visits and goal completions a brand's web
// analytics recorded for each UTM tag (ADR 0025). One adapter per tool.
// Adapters report counts, never visitors. Errors are platform.Error,
// classified as posting's are.
package analytics

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Provider names a web analytics tool.
type Provider string

// Sandbox is the test-mode provider, whose numbers are invented.
const Sandbox Provider = "sandbox"

// Site is a site as the provider describes it.
type Site struct {
	// ID is what the provider calls the site (a domain, a property ID).
	ID       string
	Name     string
	Timezone string
}

// UTM is a visit's campaign tags; "" means the visit had none.
type UTM struct {
	Source, Medium, Campaign, Content string
}

// Row is one day's counts for one combination of tags. With Goal empty it
// is traffic: Visitors (unique) and Visits. With Goal set it is that
// goal's conversions: Visitors who completed it, and Events (completions).
type Row struct {
	Day      time.Time // the date, as midnight UTC
	UTM      UTM
	Goal     string
	Visitors int64
	Visits   int64
	Events   int64
}

// Source reads one analytics tool.
type Source interface {
	Provider() Provider
	Name() string
	// Fields are what connecting a site takes.
	Fields() []platform.Field
	// Verify checks credentials and describes the site they reach.
	Verify(ctx context.Context, c platform.Credentials) (Site, error)
	// Report returns each day's traffic, and each goal's conversions, by
	// UTM tags, from from to to inclusive. Traffic without tags is a row
	// with an empty UTM.
	Report(ctx context.Context, c platform.Credentials, from, to time.Time, goals []string) ([]Row, error)
}

// Registry holds the providers an install can read.
type Registry struct {
	sources map[Provider]Source
}

// NewRegistry returns a registry of providers.
func NewRegistry(sources ...Source) *Registry {
	r := &Registry{sources: map[Provider]Source{}}
	for _, s := range sources {
		r.sources[s.Provider()] = s
	}
	return r
}

// Get returns a provider's source.
func (r *Registry) Get(p Provider) (Source, bool) {
	s, ok := r.sources[p]
	return s, ok
}

// Providers lists the providers, sorted.
func (r *Registry) Providers() []Provider {
	out := make([]Provider, 0, len(r.sources))
	for p := range r.sources {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// CleanTag is a tag value as stored: tools report a missing tag as "",
// "(none)" or "(not set)", all of which mean none.
func CleanTag(v string) string {
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case "(none)", "(not set)", "(direct)", "(not provided)":
		return ""
	}
	return v
}
