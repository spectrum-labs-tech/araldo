// SPDX-License-Identifier: AGPL-3.0-or-later

// Package email hands newsletters to email providers (ADR 0024). Araldo
// designs and schedules an issue; the provider owns the subscribers and
// sends. One adapter per provider. Adapters see audiences' IDs, names and
// sizes, never a subscriber. Errors are platform.Error, classified as
// posting's are.
package email

import (
	"context"
	"sort"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Provider names an email provider.
type Provider string

// Sandbox is the test-mode provider: it sends nothing and invents numbers.
const Sandbox Provider = "sandbox"

// Account is the provider account a key reaches.
type Account struct {
	ExternalID string
	Name       string
}

// Address is a sender or reply-to address.
type Address struct {
	Name  string
	Email string
}

// Audience is what the provider groups subscribers by: a list, a segment,
// a tag. Size is -1 when the provider does not say.
type Audience struct {
	ID   string
	Name string
	Kind string
	Size int64
}

// Message is one delivery's issue, rendered for its provider.
type Message struct {
	// Name is the campaign's name in the provider's own tools.
	Name        string
	Subject     string
	PreviewText string
	HTML        string
	Text        string
	From        Address
	ReplyTo     string
	// Tag marks the campaign with the delivery's ID, to find it again when
	// a create's result is unknown (ADR 0024 decision 14).
	Tag string
}

// CampaignStatus is where a campaign stands at the provider.
type CampaignStatus string

// Campaign statuses.
const (
	CampaignScheduled CampaignStatus = "scheduled"
	CampaignSending   CampaignStatus = "sending"
	CampaignSent      CampaignStatus = "sent"
	// CampaignStopped is a campaign someone suspended, unscheduled or
	// deleted in the provider's own tools.
	CampaignStopped CampaignStatus = "stopped"
)

// Results are a sent campaign's counts. Opens are inflated by mail clients
// that load images on their own; clicks are the better signal.
type Results struct {
	Recipients   int64
	Delivered    int64
	Opens        int64 // unique
	Clicks       int64 // unique
	Unsubscribes int64
	Bounces      int64
	Complaints   int64
}

// Campaign is a campaign as the provider reports it.
type Campaign struct {
	ID      string
	Status  CampaignStatus
	SentAt  *time.Time
	Results Results
}

// Mailer is one email provider.
type Mailer interface {
	Provider() Provider
	Name() string
	// Fields are what connecting an account takes besides the sender.
	Fields() []platform.Field
	// Unsubscribe is what the provider replaces with each recipient's own
	// unsubscribe link, in an href.
	Unsubscribe() string
	// Verify checks credentials and that from is a sender the provider
	// will send as.
	Verify(ctx context.Context, c platform.Credentials, from Address) (Account, error)
	// Audiences lists what an issue can be sent to.
	Audiences(ctx context.Context, c platform.Credentials) ([]Audience, error)
	// Schedule creates a campaign for m to the audiences, to be sent by the
	// provider at at, and returns its ID.
	Schedule(ctx context.Context, c platform.Credentials, m Message, audiences []string, at time.Time) (string, error)
	// Find returns the ID of the campaign tagged tag, or "".
	Find(ctx context.Context, c platform.Credentials, tag string) (string, error)
	// Reschedule moves a campaign that has not been sent.
	Reschedule(ctx context.Context, c platform.Credentials, campaignID string, at time.Time) error
	// Cancel removes a campaign that has not been sent.
	Cancel(ctx context.Context, c platform.Credentials, campaignID string) error
	// Campaign reads a campaign's status and results.
	Campaign(ctx context.Context, c platform.Credentials, campaignID string) (Campaign, error)
	// SendTest sends m to a few addresses, outside any audience.
	SendTest(ctx context.Context, c platform.Credentials, m Message, to []string) error
}

// Registry holds the providers an install can send through.
type Registry struct {
	mailers map[Provider]Mailer
}

// NewRegistry returns a registry of providers.
func NewRegistry(mailers ...Mailer) *Registry {
	r := &Registry{mailers: map[Provider]Mailer{}}
	for _, m := range mailers {
		r.mailers[m.Provider()] = m
	}
	return r
}

// Get returns a provider's mailer.
func (r *Registry) Get(p Provider) (Mailer, bool) {
	m, ok := r.mailers[p]
	return m, ok
}

// Providers lists the providers, sorted.
func (r *Registry) Providers() []Provider {
	out := make([]Provider, 0, len(r.mailers))
	for p := range r.mailers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
