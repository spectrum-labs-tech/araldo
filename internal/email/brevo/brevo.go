// SPDX-License-Identifier: AGPL-3.0-or-later

// Package brevo hands newsletters to Brevo (https://developers.brevo.com)
// as email campaigns (ADR 0024). Audiences are Brevo's contact lists and
// segments; Brevo sends, and owns the subscribers, unsubscribes, bounces
// and complaints. Test sends go through Brevo's transactional API, which
// reaches any address without adding it to a list.
package brevo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// DefaultAPI is Brevo's API.
const DefaultAPI = "https://api.brevo.com/v3"

// Brevo pages lists and segments 50 at a time; campaigns are searched
// newest first, a few pages deep.
const (
	audiencePage  = 50
	campaignPage  = 100
	campaignPages = 5
)

// Mailer sends through Brevo.
type Mailer struct {
	Client *http.Client
	// API is Brevo's; tests replace it.
	API string
}

// New returns the Brevo mailer.
func New(client *http.Client) *Mailer { return &Mailer{Client: client, API: DefaultAPI} }

func (m *Mailer) Provider() email.Provider { return "brevo" }
func (m *Mailer) Name() string             { return "Brevo" }

// Unsubscribe is Brevo's placeholder for each recipient's unsubscribe link.
func (m *Mailer) Unsubscribe() string { return "{{ unsubscribe }}" }

func (m *Mailer) Fields() []platform.Field {
	return []platform.Field{
		{Name: "api_key", Label: "API key", Secret: true, Help: "Brevo → SMTP & API → API keys → Generate a new API key"},
	}
}

func (m *Mailer) call(ctx context.Context, c platform.Credentials, method, path string, in, out any) error {
	return platform.JSON(ctx, m.Client, method, m.API+path, map[string]string{"api-key": c["api_key"]}, in, out)
}

// notFound reports Brevo's answer for a campaign that does not exist.
func notFound(err error) bool {
	var pe *platform.Error
	return errors.As(err, &pe) && strings.HasPrefix(pe.Msg, "HTTP 404")
}

func (m *Mailer) Verify(ctx context.Context, c platform.Credentials, from email.Address) (email.Account, error) {
	if c["api_key"] == "" {
		return email.Account{}, &platform.Error{Kind: platform.Rejected, Code: "fields_missing", Msg: "an API key is required"}
	}
	var acct struct {
		Email       string `json:"email"`
		CompanyName string `json:"companyName"`
	}
	if err := m.call(ctx, c, http.MethodGet, "/account", nil, &acct); err != nil {
		return email.Account{}, err
	}
	ok, err := m.canSendAs(ctx, c, from.Email)
	if err != nil {
		return email.Account{}, err
	}
	if !ok {
		return email.Account{}, &platform.Error{Kind: platform.Rejected, Code: "sender_unverified",
			Msg: from.Email + " is not an active Brevo sender, nor on an authenticated domain: add it in Brevo → Senders, Domains & Dedicated IPs"}
	}
	name := acct.CompanyName
	if name == "" {
		name = acct.Email
	}
	return email.Account{ExternalID: acct.Email, Name: name}, nil
}

// canSendAs reports whether addr is an active sender, or on a domain
// authenticated in Brevo.
func (m *Mailer) canSendAs(ctx context.Context, c platform.Credentials, addr string) (bool, error) {
	var senders struct {
		Senders []struct {
			Email  string `json:"email"`
			Active bool   `json:"active"`
		} `json:"senders"`
	}
	if err := m.call(ctx, c, http.MethodGet, "/senders", nil, &senders); err != nil {
		return false, err
	}
	for _, s := range senders.Senders {
		if s.Active && strings.EqualFold(s.Email, addr) {
			return true, nil
		}
	}
	var domains struct {
		Domains []struct {
			Name          string `json:"domain_name"`
			Authenticated bool   `json:"authenticated"`
			Verified      bool   `json:"verified"`
		} `json:"domains"`
	}
	if err := m.call(ctx, c, http.MethodGet, "/senders/domains", nil, &domains); err != nil {
		return false, err
	}
	_, domain, _ := strings.Cut(addr, "@")
	for _, d := range domains.Domains {
		if d.Authenticated && d.Verified && strings.EqualFold(d.Name, domain) {
			return true, nil
		}
	}
	return false, nil
}

// Audience IDs are "list:<id>" or "segment:<id>".
func (m *Mailer) Audiences(ctx context.Context, c platform.Credentials) ([]email.Audience, error) {
	var out []email.Audience
	for offset := 0; ; offset += audiencePage {
		var page struct {
			Lists []struct {
				ID                int64  `json:"id"`
				Name              string `json:"name"`
				UniqueSubscribers int64  `json:"uniqueSubscribers"`
			} `json:"lists"`
			Count int `json:"count"`
		}
		if err := m.call(ctx, c, http.MethodGet, fmt.Sprintf("/contacts/lists?limit=%d&offset=%d", audiencePage, offset), nil, &page); err != nil {
			return nil, err
		}
		for _, l := range page.Lists {
			out = append(out, email.Audience{ID: "list:" + strconv.FormatInt(l.ID, 10), Name: l.Name, Kind: "list", Size: l.UniqueSubscribers})
		}
		if len(page.Lists) < audiencePage || offset+len(page.Lists) >= page.Count {
			break
		}
	}
	for offset := 0; ; offset += audiencePage {
		var page struct {
			Segments []struct {
				ID   int64  `json:"id"`
				Name string `json:"segmentName"`
			} `json:"segments"`
			Count int `json:"count"`
		}
		if err := m.call(ctx, c, http.MethodGet, fmt.Sprintf("/contacts/segments?limit=%d&offset=%d", audiencePage, offset), nil, &page); err != nil {
			return nil, err
		}
		for _, s := range page.Segments {
			out = append(out, email.Audience{ID: "segment:" + strconv.FormatInt(s.ID, 10), Name: s.Name, Kind: "segment", Size: -1})
		}
		if len(page.Segments) < audiencePage || offset+len(page.Segments) >= page.Count {
			break
		}
	}
	return out, nil
}

type recipients struct {
	ListIDs    []int64 `json:"listIds,omitempty"`
	SegmentIDs []int64 `json:"segmentIds,omitempty"`
}

func toRecipients(audiences []string) (recipients, error) {
	var r recipients
	for _, a := range audiences {
		kind, raw, _ := strings.Cut(a, ":")
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return r, &platform.Error{Kind: platform.Rejected, Code: "audience_invalid", Msg: fmt.Sprintf("%q is not a Brevo list or segment", a)}
		}
		switch kind {
		case "list":
			r.ListIDs = append(r.ListIDs, n)
		case "segment":
			r.SegmentIDs = append(r.SegmentIDs, n)
		default:
			return r, &platform.Error{Kind: platform.Rejected, Code: "audience_invalid", Msg: fmt.Sprintf("%q is not a Brevo list or segment", a)}
		}
	}
	if len(r.ListIDs)+len(r.SegmentIDs) == 0 {
		return r, &platform.Error{Kind: platform.Rejected, Code: "audience_missing", Msg: "a campaign needs a list or a segment"}
	}
	return r, nil
}

type address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

type campaign struct {
	Name        string     `json:"name"`
	Subject     string     `json:"subject"`
	PreviewText string     `json:"previewText,omitempty"`
	Sender      address    `json:"sender"`
	ReplyTo     string     `json:"replyTo,omitempty"`
	HTMLContent string     `json:"htmlContent"`
	Tag         string     `json:"tag"`
	Recipients  recipients `json:"recipients"`
	ScheduledAt string     `json:"scheduledAt"`
}

// Schedule creates the campaign with its send time in one request, so it
// is never left created but unscheduled.
func (m *Mailer) Schedule(ctx context.Context, c platform.Credentials, msg email.Message, audiences []string, at time.Time) (string, error) {
	r, err := toRecipients(audiences)
	if err != nil {
		return "", err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	err = m.call(ctx, c, http.MethodPost, "/emailCampaigns", campaign{Name: msg.Name, Subject: msg.Subject, PreviewText: msg.PreviewText,
		Sender: address{msg.From.Name, msg.From.Email}, ReplyTo: msg.ReplyTo, HTMLContent: msg.HTML, Tag: msg.Tag, Recipients: r,
		ScheduledAt: at.UTC().Format(time.RFC3339)}, &out)
	if err != nil {
		return "", err
	}
	if out.ID == 0 {
		// Created, but which one is unknown: find it by its tag.
		return "", &platform.Error{Kind: platform.Uncertain, Code: "id_missing", Msg: "Brevo answered without the campaign's ID"}
	}
	return strconv.FormatInt(out.ID, 10), nil
}

func (m *Mailer) Find(ctx context.Context, c platform.Credentials, tag string) (string, error) {
	for page := 0; page < campaignPages; page++ {
		var out struct {
			Campaigns []struct {
				ID  int64  `json:"id"`
				Tag string `json:"tag"`
			} `json:"campaigns"`
		}
		q := url.Values{"type": {"classic"}, "limit": {strconv.Itoa(campaignPage)}, "offset": {strconv.Itoa(page * campaignPage)},
			"sort": {"desc"}, "excludeHtmlContent": {"true"}}
		if err := m.call(ctx, c, http.MethodGet, "/emailCampaigns?"+q.Encode(), nil, &out); err != nil {
			return "", err
		}
		for _, cp := range out.Campaigns {
			if cp.Tag == tag {
				return strconv.FormatInt(cp.ID, 10), nil
			}
		}
		if len(out.Campaigns) < campaignPage {
			break
		}
	}
	return "", nil
}

func (m *Mailer) Reschedule(ctx context.Context, c platform.Credentials, campaignID string, at time.Time) error {
	return m.call(ctx, c, http.MethodPut, "/emailCampaigns/"+url.PathEscape(campaignID),
		map[string]string{"scheduledAt": at.UTC().Format(time.RFC3339)}, nil)
}

// Cancel deletes the campaign; one already gone is canceled.
func (m *Mailer) Cancel(ctx context.Context, c platform.Credentials, campaignID string) error {
	err := m.call(ctx, c, http.MethodDelete, "/emailCampaigns/"+url.PathEscape(campaignID), nil, nil)
	if notFound(err) {
		return nil
	}
	return err
}

type stats struct {
	Sent            int64 `json:"sent"`
	Delivered       int64 `json:"delivered"`
	UniqueViews     int64 `json:"uniqueViews"`
	UniqueClicks    int64 `json:"uniqueClicks"`
	Unsubscriptions int64 `json:"unsubscriptions"`
	HardBounces     int64 `json:"hardBounces"`
	SoftBounces     int64 `json:"softBounces"`
	Complaints      int64 `json:"complaints"`
}

func (s stats) results() email.Results {
	return email.Results{Recipients: s.Sent, Delivered: s.Delivered, Opens: s.UniqueViews, Clicks: s.UniqueClicks,
		Unsubscribes: s.Unsubscriptions, Bounces: s.HardBounces + s.SoftBounces, Complaints: s.Complaints}
}

// Campaign reads a campaign. Brevo reports totals in globalStats, and
// sometimes only per list in campaignStats, which are then added up.
func (m *Mailer) Campaign(ctx context.Context, c platform.Credentials, campaignID string) (email.Campaign, error) {
	var out struct {
		Status     string `json:"status"`
		SentDate   string `json:"sentDate"`
		Statistics struct {
			GlobalStats   stats   `json:"globalStats"`
			CampaignStats []stats `json:"campaignStats"`
		} `json:"statistics"`
	}
	err := m.call(ctx, c, http.MethodGet, "/emailCampaigns/"+url.PathEscape(campaignID)+"?statistics=globalStats&excludeHtmlContent=true", nil, &out)
	if notFound(err) {
		return email.Campaign{ID: campaignID, Status: email.CampaignStopped}, nil
	}
	if err != nil {
		return email.Campaign{}, err
	}
	cp := email.Campaign{ID: campaignID}
	switch out.Status {
	case "queued":
		cp.Status = email.CampaignScheduled
	case "inProcess", "in_process":
		cp.Status = email.CampaignSending
	case "sent", "archive":
		cp.Status = email.CampaignSent
	default: // draft, suspended
		cp.Status = email.CampaignStopped
	}
	if t, err := time.Parse(time.RFC3339, out.SentDate); err == nil {
		cp.SentAt = &t
	}
	total := out.Statistics.GlobalStats
	if total == (stats{}) {
		for _, s := range out.Statistics.CampaignStats {
			total.Sent += s.Sent
			total.Delivered += s.Delivered
			total.UniqueViews += s.UniqueViews
			total.UniqueClicks += s.UniqueClicks
			total.Unsubscriptions += s.Unsubscriptions
			total.HardBounces += s.HardBounces
			total.SoftBounces += s.SoftBounces
			total.Complaints += s.Complaints
		}
	}
	cp.Results = total.results()
	return cp, nil
}

func (m *Mailer) SendTest(ctx context.Context, c platform.Credentials, msg email.Message, to []string) error {
	body := map[string]any{"sender": address{msg.From.Name, msg.From.Email}, "subject": msg.Subject, "htmlContent": msg.HTML,
		"textContent": msg.Text, "tags": []string{"araldo-test"}}
	rcpt := make([]address, len(to))
	for i, addr := range to {
		rcpt[i] = address{Email: addr}
	}
	body["to"] = rcpt
	if msg.ReplyTo != "" {
		body["replyTo"] = address{Email: msg.ReplyTo}
	}
	return m.call(ctx, c, http.MethodPost, "/smtp/email", body, nil)
}
