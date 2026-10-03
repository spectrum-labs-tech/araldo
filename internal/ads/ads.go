// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ads reads ad networks' results (ADR 0023). One adapter per
// network; this phase reads accounts and their campaigns' daily results,
// and spends nothing. Errors are platform.Error, classified the same way
// as posting's.
package ads

import (
	"context"
	"sort"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Network names an ad network.
type Network string

// Sandbox is the test-mode network, whose results are invented.
const Sandbox Network = "sandbox"

// Account is an ad account as the network describes it.
type Account struct {
	ExternalID string
	Name       string
	// Currency is the ISO 4217 code amounts are in.
	Currency string
	// Timezone is the IANA zone the network's days are in.
	Timezone string
}

// Result is one campaign's results on one day of the account's time zone.
type Result struct {
	CampaignID   string
	CampaignName string
	// Day is the date, as midnight UTC.
	Day time.Time
	// Spend is in the minor unit of the account's currency.
	Spend       int64
	Impressions int64
	Clicks      int64
	// Results counts what the campaign's objective counts (signups,
	// purchases, link clicks), as the network reports it.
	Results int64
}

// Reporter reads an ad network.
type Reporter interface {
	Network() Network
	Name() string
	// Fields are what connecting an account takes.
	Fields() []platform.Field
	// Verify checks credentials and describes the account they reach.
	Verify(ctx context.Context, c platform.Credentials) (Account, error)
	// Report returns every campaign's results for each day from from to to,
	// inclusive. Campaigns made in the network's own tools are included.
	Report(ctx context.Context, c platform.Credentials, from, to time.Time) ([]Result, error)
}

// Registry holds the networks an install can read.
type Registry struct {
	networks map[Network]Reporter
}

// NewRegistry returns a registry of networks.
func NewRegistry(networks ...Reporter) *Registry {
	r := &Registry{networks: map[Network]Reporter{}}
	for _, n := range networks {
		r.networks[n.Network()] = n
	}
	return r
}

// Get returns a network's reporter.
func (r *Registry) Get(n Network) (Reporter, bool) {
	rep, ok := r.networks[n]
	return rep, ok
}

// Networks lists the networks, sorted.
func (r *Registry) Networks() []Network {
	out := make([]Network, 0, len(r.networks))
	for n := range r.networks {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Date is t's calendar day in loc, as midnight UTC.
func Date(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Totals adds up results.
type Totals struct {
	Spend       int64
	Impressions int64
	Clicks      int64
	Results     int64
}

// CostPerClick is spend divided by clicks, rounded, or 0 without clicks.
func (t Totals) CostPerClick() int64 {
	if t.Clicks == 0 {
		return 0
	}
	return (t.Spend + t.Clicks/2) / t.Clicks
}
