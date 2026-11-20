// SPDX-License-Identifier: AGPL-3.0-or-later

// Package resend hands newsletters to Resend (https://resend.com/docs) as
// broadcasts (ADR 0024). Audiences are Resend's segments; Resend sends,
// and owns the contacts, unsubscribes, bounces and complaints. Test sends
// go through Resend's email API, which reaches any address without adding
// it to a segment.
//
// Where Resend differs from Brevo:
//   - A broadcast goes to one segment, so a delivery takes exactly one
//     audience.
//   - Segments carry no size, and counting one means listing its contacts'
//     addresses, which Araldo never reads: sizes are unknown (-1).
//   - Broadcasts take no tag. The delivery's tag ends the broadcast's name,
//     and Find looks for it there, so renaming a broadcast in Resend's
//     dashboard before a reconciliation hides it (ADR 0024 decision 14).
//   - A scheduled broadcast cannot be moved in place: it is canceled back
//     to a draft and sent again with the new time.
//   - Results come from the email metrics API; Resend does not report a
//     recipient count separately, so Recipients is the number sent.
package resend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// DefaultAPI is Resend's API.
const DefaultAPI = "https://api.resend.com"

// Resend's list endpoints take at most 100 a page
// (https://resend.com/docs/api-reference/pagination). Broadcasts are not
// documented to come in any order, so Find reads up to findPages of them.
const (
	pageSize  = 100
	findPages = 20
)

// Mailer sends through Resend.
type Mailer struct {
	Client *http.Client
	// API is Resend's; tests replace it.
	API string
}

// New returns the Resend mailer.
func New(client *http.Client) *Mailer { return &Mailer{Client: client, API: DefaultAPI} }

func (m *Mailer) Provider() email.Provider { return "resend" }
func (m *Mailer) Name() string             { return "Resend" }

// Unsubscribe is the placeholder Resend replaces with each contact's
// unsubscribe link in a broadcast
// (https://resend.com/docs/api-reference/broadcasts/create-broadcast).
func (m *Mailer) Unsubscribe() string { return "{{{RESEND_UNSUBSCRIBE_URL}}}" }

// Fields: broadcasts, segments and domains need a full-access key; a
// sending-access key "can only send emails"
// (https://resend.com/docs/dashboard/api-keys/introduction).
func (m *Mailer) Fields() []platform.Field {
	return []platform.Field{
		{Name: "api_key", Label: "API key", Secret: true,
			Help: "Resend → API Keys → Create API key, with Full access: a sending-access key cannot create broadcasts or read segments"},
	}
}

func (m *Mailer) call(ctx context.Context, c platform.Credentials, method, path string, in, out any) error {
	err := platform.JSON(ctx, m.Client, method, m.API+path, map[string]string{"Authorization": "Bearer " + c["api_key"]}, in, out)
	return classify(err)
}

// apiError is Resend's error body
// (https://resend.com/docs/api-reference/errors).
type apiError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// classify reads Resend's own error names over the HTTP status. A key that
// does not exist is a 400 validation_error on every endpoint but sending's,
// and a sending-access key is refused with restricted_api_key: both mean
// the account needs a new key.
func classify(err error) error {
	var pe *platform.Error
	if !errors.As(err, &pe) || len(pe.Body) == 0 {
		return err
	}
	var body apiError
	if json.Unmarshal(pe.Body, &body) != nil {
		return err
	}
	switch {
	case body.Name == "restricted_api_key" && pe.Status == http.StatusUnauthorized:
		pe.Kind, pe.Code, pe.Msg = platform.AuthRevoked, "key_restricted", "the API key has sending access only; create one with full access"
	case body.Name == "restricted_api_key", body.Name == "suspended_api_key", body.Name == "invalid_permission", body.Name == "missing_api_key",
		body.Name == "validation_error" && strings.Contains(strings.ToLower(body.Message), "api key"):
		pe.Kind, pe.Code, pe.Msg = platform.AuthRevoked, body.Name, "Resend refused the API key: "+body.Message
	}
	return pe
}

// notFound reports Resend's answer for a broadcast that does not exist.
func notFound(err error) bool {
	return platform.HTTPStatus(err) == http.StatusNotFound
}

// list is Resend's list envelope.
type list[T any] struct {
	HasMore bool `json:"has_more"`
	Data    []T  `json:"data"`
}

// each reads every page of a list endpoint, up to pages of them, calling
// fn for each item until it returns false. id gives an item's ID, for the
// next page's cursor.
func each[T any](ctx context.Context, m *Mailer, c platform.Credentials, path string, pages int, id func(T) string, fn func(T) bool) error {
	after := ""
	for range pages {
		q := url.Values{"limit": {strconv.Itoa(pageSize)}}
		if after != "" {
			q.Set("after", after)
		}
		var page list[T]
		if err := m.call(ctx, c, http.MethodGet, path+"?"+q.Encode(), nil, &page); err != nil {
			return err
		}
		for _, item := range page.Data {
			if !fn(item) {
				return nil
			}
		}
		if !page.HasMore || len(page.Data) == 0 {
			return nil
		}
		after = id(page.Data[len(page.Data)-1])
	}
	return nil
}

type domain struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Verify checks the key by listing its domains (which a sending-access key
// may not do), and that from is on one Resend shows as verified
// (https://resend.com/docs/api-reference/domains/list-domains). Resend has
// no account endpoint, so the account is the sending domain.
func (m *Mailer) Verify(ctx context.Context, c platform.Credentials, from email.Address) (email.Account, error) {
	if c["api_key"] == "" {
		return email.Account{}, &platform.Error{Kind: platform.Rejected, Code: "fields_missing", Msg: "an API key is required"}
	}
	_, want, _ := strings.Cut(from.Email, "@")
	var found *domain
	err := each(ctx, m, c, "/domains", 50, func(d domain) string { return d.ID }, func(d domain) bool {
		if strings.EqualFold(d.Name, want) {
			found = &d
			return false
		}
		return true
	})
	if err != nil {
		return email.Account{}, err
	}
	if found == nil || found.Status != "verified" {
		state := "not in this Resend account"
		if found != nil {
			state = "not verified in Resend yet (" + strings.ReplaceAll(found.Status, "_", " ") + ")"
		}
		return email.Account{}, &platform.Error{Kind: platform.Rejected, Code: "sender_unverified",
			Msg: fmt.Sprintf("%s is %s: add the domain in Resend → Domains and publish its records", want, state)}
	}
	return email.Account{ExternalID: found.ID, Name: found.Name}, nil
}

type segment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Audiences are Resend's segments, which replaced audiences
// (https://resend.com/docs/api-reference/segments/list-segments). Their
// IDs are Resend's own.
func (m *Mailer) Audiences(ctx context.Context, c platform.Credentials) ([]email.Audience, error) {
	var out []email.Audience
	err := each(ctx, m, c, "/segments", 50, func(s segment) string { return s.ID }, func(s segment) bool {
		out = append(out, email.Audience{ID: s.ID, Name: s.Name, Kind: "segment", Size: -1})
		return true
	})
	return out, err
}

// sender formats an address as Resend's from takes it: "Name <email>".
func sender(a email.Address) string {
	if a.Name == "" {
		return a.Email
	}
	return (&mail.Address{Name: a.Name, Address: a.Email}).String()
}

// name is a broadcast's name: the message's, ending with its tag for Find.
func name(msg email.Message) string {
	if msg.Name == "" {
		return msg.Tag
	}
	return msg.Name + " · " + msg.Tag
}

type broadcast struct {
	SegmentID   string `json:"segment_id"`
	From        string `json:"from"`
	Subject     string `json:"subject"`
	ReplyTo     string `json:"reply_to,omitempty"`
	HTML        string `json:"html"`
	Text        string `json:"text,omitempty"`
	Name        string `json:"name"`
	Send        bool   `json:"send"`
	ScheduledAt string `json:"scheduled_at"`
}

// Schedule creates the broadcast and schedules it in one request
// (https://resend.com/docs/api-reference/broadcasts/create-broadcast), so
// it is never left created but unscheduled. The preview text is already
// in the HTML: Resend's create takes none of its own.
func (m *Mailer) Schedule(ctx context.Context, c platform.Credentials, msg email.Message, audiences []string, at time.Time) (string, error) {
	switch {
	case len(audiences) == 0:
		return "", &platform.Error{Kind: platform.Rejected, Code: "audience_missing", Msg: "a broadcast needs a segment"}
	case len(audiences) > 1:
		return "", &platform.Error{Kind: platform.Rejected, Code: "audience_multiple",
			Msg: "a Resend broadcast goes to one segment; choose one, or make a segment in Resend that covers them"}
	}
	var out struct {
		ID string `json:"id"`
	}
	err := m.call(ctx, c, http.MethodPost, "/broadcasts", broadcast{SegmentID: audiences[0], From: sender(msg.From), Subject: msg.Subject,
		ReplyTo: msg.ReplyTo, HTML: msg.HTML, Text: msg.Text, Name: name(msg), Send: true, ScheduledAt: at.UTC().Format(time.RFC3339)}, &out)
	if err != nil {
		return "", err
	}
	if out.ID == "" {
		// Created, but which one is unknown: find it by its name.
		return "", &platform.Error{Kind: platform.Uncertain, Code: "id_missing", Msg: "Resend answered without the broadcast's ID"}
	}
	return out.ID, nil
}

type listed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Find looks for the broadcast whose name ends with tag
// (https://resend.com/docs/api-reference/broadcasts/list-broadcasts).
func (m *Mailer) Find(ctx context.Context, c platform.Credentials, tag string) (string, error) {
	found := ""
	err := each(ctx, m, c, "/broadcasts", findPages, func(b listed) string { return b.ID }, func(b listed) bool {
		if strings.HasSuffix(b.Name, tag) {
			found = b.ID
			return false
		}
		return true
	})
	return found, err
}

type detail struct {
	Status      string `json:"status"`
	ScheduledAt string `json:"scheduled_at"`
	SentAt      string `json:"sent_at"`
	CreatedAt   string `json:"created_at"`
}

func (m *Mailer) get(ctx context.Context, c platform.Credentials, id string) (detail, error) {
	var d detail
	err := m.call(ctx, c, http.MethodGet, "/broadcasts/"+url.PathEscape(id), nil, &d)
	return d, err
}

// Reschedule moves a broadcast that has not been sent. Resend cannot
// change a scheduled broadcast's time, so it is canceled, which returns it
// to a draft (https://resend.com/docs/api-reference/broadcasts/cancel-broadcast),
// then sent again with the new one
// (https://resend.com/docs/api-reference/broadcasts/send-broadcast). A
// draft, left by a move that failed half way, is just sent.
func (m *Mailer) Reschedule(ctx context.Context, c platform.Credentials, campaignID string, at time.Time) error {
	d, err := m.get(ctx, c, campaignID)
	if err != nil {
		return err
	}
	path := "/broadcasts/" + url.PathEscape(campaignID)
	switch d.Status {
	case "scheduled":
		if err := m.call(ctx, c, http.MethodPost, path+"/cancel", nil, nil); err != nil {
			return err
		}
	case "draft":
	default:
		return &platform.Error{Kind: platform.Rejected, Code: "campaign_not_movable",
			Msg: fmt.Sprintf("the Resend broadcast is %s and can no longer be moved", d.Status)}
	}
	err = m.call(ctx, c, http.MethodPost, path+"/send", map[string]string{"scheduled_at": at.UTC().Format(time.RFC3339)}, nil)
	if err != nil {
		var pe *platform.Error
		if errors.As(err, &pe) {
			pe.Msg = "the broadcast was unscheduled but not scheduled again, so it is a draft that will not send: " + pe.Msg
		}
	}
	return err
}

// Cancel deletes the broadcast, which cancels a scheduled one
// (https://resend.com/docs/api-reference/broadcasts/delete-broadcast); one
// already gone is canceled.
func (m *Mailer) Cancel(ctx context.Context, c platform.Credentials, campaignID string) error {
	err := m.call(ctx, c, http.MethodDelete, "/broadcasts/"+url.PathEscape(campaignID), nil, nil)
	if notFound(err) {
		return nil
	}
	return err
}

// parseTime reads Resend's timestamps, which its examples show as
// "2026-12-02 19:32:22.980+00" rather than RFC 3339.
func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07", "2006-01-02 15:04:05.999999999-07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// metrics are the counts the email metrics API returns
// (https://resend.com/docs/api-reference/emails/get-metrics).
type metrics struct {
	Sent          int64 `json:"sent"`
	Delivered     int64 `json:"delivered"`
	UniqueOpened  int64 `json:"unique_opened"`
	UniqueClicked int64 `json:"unique_clicked"`
	Unsubscribed  int64 `json:"unsubscribed"`
	Bounced       int64 `json:"bounced"`
	Complained    int64 `json:"complained"`
}

// Campaign reads a broadcast's status
// (https://resend.com/docs/api-reference/broadcasts/get-broadcast) and, once
// sent, its totals from the email metrics API, from the day before it was
// sent (the API otherwise starts six days back) to now.
func (m *Mailer) Campaign(ctx context.Context, c platform.Credentials, campaignID string) (email.Campaign, error) {
	d, err := m.get(ctx, c, campaignID)
	if notFound(err) {
		return email.Campaign{ID: campaignID, Status: email.CampaignStopped}, nil
	}
	if err != nil {
		return email.Campaign{}, err
	}
	cp := email.Campaign{ID: campaignID}
	switch d.Status {
	case "scheduled":
		cp.Status = email.CampaignScheduled
	case "queued", "sending":
		cp.Status = email.CampaignSending
	case "sent":
		cp.Status = email.CampaignSent
	default: // draft, canceled
		cp.Status = email.CampaignStopped
	}
	if cp.Status != email.CampaignSent {
		return cp, nil
	}
	from := ""
	if t, ok := parseTime(d.SentAt); ok {
		cp.SentAt = &t
		from = t.UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	} else if t, ok := parseTime(d.CreatedAt); ok {
		from = t.UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	}
	q := url.Values{"broadcast_id": {campaignID},
		"metrics": {"sent,delivered,unique_opened,unique_clicked,unsubscribed,bounced,complained"}}
	if from != "" {
		q.Set("start_date", from)
	}
	var out struct {
		Totals metrics `json:"totals"`
	}
	if err := m.call(ctx, c, http.MethodGet, "/emails/metrics?"+q.Encode(), nil, &out); err != nil {
		return email.Campaign{}, err
	}
	t := out.Totals
	cp.Results = email.Results{Recipients: t.Sent, Delivered: t.Delivered, Opens: t.UniqueOpened, Clicks: t.UniqueClicked,
		Unsubscribes: t.Unsubscribed, Bounces: t.Bounced, Complaints: t.Complained}
	return cp, nil
}

type tag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SendTest sends one email to the addresses
// (https://resend.com/docs/api-reference/emails/send-email), which takes
// up to 50 of them.
func (m *Mailer) SendTest(ctx context.Context, c platform.Credentials, msg email.Message, to []string) error {
	if len(to) > 50 {
		return &platform.Error{Kind: platform.Rejected, Code: "too_many_recipients", Msg: "Resend sends one email to at most 50 addresses"}
	}
	body := map[string]any{"from": sender(msg.From), "to": to, "subject": msg.Subject, "html": msg.HTML,
		"tags": []tag{{Name: "araldo", Value: "test"}}}
	if msg.Text != "" {
		body["text"] = msg.Text
	}
	if msg.ReplyTo != "" {
		body["reply_to"] = msg.ReplyTo
	}
	return m.call(ctx, c, http.MethodPost, "/emails", body, nil)
}
