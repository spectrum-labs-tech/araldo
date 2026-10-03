// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// MailProviderView is an email provider in the API.
type MailProviderView struct {
	Provider      string           `json:"provider"`
	Name          string           `json:"name"`
	ConnectFields []platform.Field `json:"connect_fields"`
}

// ViewMailProvider renders a provider.
func ViewMailProvider(p MailProviderInfo) MailProviderView {
	fields := p.Fields
	if fields == nil {
		fields = []platform.Field{}
	}
	return MailProviderView{Provider: string(p.Provider), Name: p.Name, ConnectFields: fields}
}

// MailAccountView is a mail account in the API. Credentials never appear.
type MailAccountView struct {
	ID               string    `json:"id"`
	Object           string    `json:"object"`
	Brand            string    `json:"brand"`
	Livemode         bool      `json:"livemode"`
	Provider         string    `json:"provider"`
	Name             string    `json:"name"`
	FromName         string    `json:"from_name"`
	FromEmail        string    `json:"from_email"`
	ReplyTo          string    `json:"reply_to,omitempty"`
	DefaultAudiences []string  `json:"default_audiences"`
	Status           string    `json:"status"`
	StatusNote       string    `json:"status_note,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// ViewMailAccount renders a mail account.
func ViewMailAccount(m *model.MailAccount) MailAccountView {
	return MailAccountView{ID: id.Format(id.MailAccount, m.ID), Object: "mail_account", Brand: id.Format(id.Brand, m.BrandID),
		Livemode: m.Livemode, Provider: m.Provider, Name: m.Name, FromName: m.FromName, FromEmail: m.FromEmail, ReplyTo: m.ReplyTo,
		DefaultAudiences: nonNilList(m.DefaultAudiences), Status: string(m.Status), StatusNote: m.StatusNote, CreatedAt: m.CreatedAt.UTC()}
}

// AudienceView is a provider's audience in the API: never its members.
type AudienceView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Size is null when the provider does not say.
	Size *int64 `json:"size"`
}

func viewAudience(id, name, kind string, size int64) AudienceView {
	v := AudienceView{ID: id, Name: name, Kind: kind}
	if size >= 0 {
		v.Size = &size
	}
	return v
}

// ViewAudiences renders a provider's audiences.
func ViewAudiences(as []email.Audience) []AudienceView {
	out := make([]AudienceView, len(as))
	for i, a := range as {
		out[i] = viewAudience(a.ID, a.Name, a.Kind, a.Size)
	}
	return out
}

// EmailThemeView is a brand's email theme in the API.
type EmailThemeView struct {
	Logo          *string `json:"logo"`
	Accent        string  `json:"accent"`
	PostalAddress string  `json:"postal_address"`
	Footer        string  `json:"footer"`
}

// ViewEmailTheme renders a brand's email theme.
func ViewEmailTheme(t model.EmailTheme) EmailThemeView {
	v := EmailThemeView{Accent: t.Accent, PostalAddress: t.PostalAddress, Footer: t.Footer}
	if t.LogoMediaID != nil {
		v.Logo = ptr(id.Format(id.Media, *t.LogoMediaID))
	}
	return v
}

// MailResultsView is a delivery's, or an issue's, counts.
type MailResultsView struct {
	Recipients   int64 `json:"recipients"`
	Delivered    int64 `json:"delivered"`
	Opens        int64 `json:"opens"`
	Clicks       int64 `json:"clicks"`
	Unsubscribes int64 `json:"unsubscribes"`
	Bounces      int64 `json:"bounces"`
	Complaints   int64 `json:"complaints"`
}

func viewMailResults(r model.MailResults) MailResultsView {
	return MailResultsView(r)
}

// IssueDeliveryView is one mail account's copy of an issue in the API.
type IssueDeliveryView struct {
	ID          string          `json:"id"`
	Object      string          `json:"object"`
	MailAccount *string         `json:"mail_account"`
	Provider    string          `json:"provider"`
	AccountName string          `json:"account_name"`
	Audiences   []AudienceView  `json:"audiences"`
	Status      string          `json:"status"`
	CampaignID  string          `json:"campaign_id,omitempty"`
	Error       string          `json:"error,omitempty"`
	SentAt      *time.Time      `json:"sent_at"`
	Results     MailResultsView `json:"results"`
	ReadAt      *time.Time      `json:"results_read_at"`
}

// IssueView is a newsletter issue in the API.
type IssueView struct {
	ID          string              `json:"id"`
	Object      string              `json:"object"`
	Brand       string              `json:"brand"`
	Livemode    bool                `json:"livemode"`
	Subject     string              `json:"subject"`
	PreviewText string              `json:"preview_text"`
	Body        string              `json:"body"`
	Status      string              `json:"status"`
	SendAt      *time.Time          `json:"send_at"`
	Approval    ApprovalView        `json:"approval"`
	Deliveries  []IssueDeliveryView `json:"deliveries"`
	Results     MailResultsView     `json:"results"`
	Media       []string            `json:"media"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

// ViewIssue renders an issue; its results add up its deliveries'.
func ViewIssue(is *model.Issue) IssueView {
	v := IssueView{ID: id.Format(id.Issue, is.ID), Object: "newsletter_issue", Brand: id.Format(id.Brand, is.BrandID), Livemode: is.Livemode,
		Subject: is.Subject, PreviewText: is.PreviewText, Body: is.Body, Status: string(is.Status), SendAt: utc(is.SendAt),
		Approval:   ApprovalView{Required: is.ApprovalNeeded, ReviewedAt: utc(is.ReviewedAt), Note: is.ReviewNote},
		Deliveries: make([]IssueDeliveryView, 0, len(is.Deliveries)), Media: make([]string, 0, len(is.Media)),
		CreatedAt: is.CreatedAt.UTC(), UpdatedAt: is.UpdatedAt.UTC()}
	if is.ReviewedByUser != nil {
		v.Approval.ReviewedBy = id.Format(id.User, *is.ReviewedByUser)
	}
	if is.ReviewedByKey != nil {
		v.Approval.ReviewedByKey = id.Format(id.APIKey, *is.ReviewedByKey)
	}
	var total model.MailResults
	for _, d := range is.Deliveries {
		dv := IssueDeliveryView{ID: id.Format(id.IssueDelivery, d.ID), Object: "newsletter_delivery", Provider: d.Provider,
			AccountName: d.AccountName, Audiences: make([]AudienceView, len(d.Audiences)), Status: string(d.Status), CampaignID: d.CampaignID,
			Error: d.LastError, SentAt: utc(d.SentAt), Results: viewMailResults(d.Results), ReadAt: utc(d.ResultsReadAt)}
		if d.MailAccountID != nil {
			dv.MailAccount = ptr(id.Format(id.MailAccount, *d.MailAccountID))
		}
		for i, a := range d.Audiences {
			dv.Audiences[i] = viewAudience(a.ID, a.Name, a.Kind, a.Size)
		}
		v.Deliveries = append(v.Deliveries, dv)
		r := d.Results
		total.Recipients += r.Recipients
		total.Delivered += r.Delivered
		total.Opens += r.Opens
		total.Clicks += r.Clicks
		total.Unsubscribes += r.Unsubscribes
		total.Bounces += r.Bounces
		total.Complaints += r.Complaints
	}
	v.Results = viewMailResults(total)
	for _, m := range is.Media {
		v.Media = append(v.Media, id.Format(id.Media, m))
	}
	return v
}
