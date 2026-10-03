// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/newsletter"
)

// Newsletters (ADR 0024).

func (h *Handler) listMailProviders(w http.ResponseWriter, r *http.Request) error {
	providers := h.svc.MailProviders(actor(r).Livemode)
	out := make([]core.MailProviderView, 0, len(providers))
	for _, p := range providers {
		out = append(out, core.ViewMailProvider(p))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/mail_providers"})
	return nil
}

func (h *Handler) listMailAccounts(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var brandID *uuid.UUID
	if ref := r.URL.Query().Get("brand"); ref != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, ref)
		if err != nil {
			return err
		}
		brandID = &b.ID
	}
	accts, err := h.svc.MailAccounts(r.Context(), a, brandID)
	if err != nil {
		return err
	}
	out := make([]core.MailAccountView, 0, len(accts))
	for _, m := range accts {
		out = append(out, core.ViewMailAccount(m))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/mail_accounts"})
	return nil
}

func (h *Handler) createMailAccount(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var body struct {
		Brand            string            `json:"brand"`
		Provider         string            `json:"provider"`
		FromName         string            `json:"from_name"`
		FromEmail        string            `json:"from_email"`
		ReplyTo          string            `json:"reply_to"`
		DefaultAudiences []string          `json:"default_audiences"`
		Fields           map[string]string `json:"fields"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	b, err := h.svc.ResolveBrand(r.Context(), a, body.Brand)
	if err != nil {
		return err
	}
	m, err := h.svc.ConnectMailAccount(r.Context(), a, core.MailAccountInput{BrandID: b.ID, Provider: email.Provider(body.Provider),
		FromName: body.FromName, FromEmail: body.FromEmail, ReplyTo: body.ReplyTo, DefaultAudiences: body.DefaultAudiences, Fields: body.Fields})
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, core.ViewMailAccount(m))
	return nil
}

func (h *Handler) getMailAccount(w http.ResponseWriter, r *http.Request) error {
	mid, err := pathID(r, id.MailAccount, "mail account")
	if err != nil {
		return err
	}
	m, err := h.svc.MailAccount(r.Context(), actor(r), mid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewMailAccount(m))
	return nil
}

func (h *Handler) updateMailAccount(w http.ResponseWriter, r *http.Request) error {
	mid, err := pathID(r, id.MailAccount, "mail account")
	if err != nil {
		return err
	}
	var body struct {
		FromName         string   `json:"from_name"`
		FromEmail        string   `json:"from_email"`
		ReplyTo          string   `json:"reply_to"`
		DefaultAudiences []string `json:"default_audiences"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	m, err := h.svc.UpdateMailAccount(r.Context(), actor(r), mid, core.MailAccountUpdate{FromName: body.FromName, FromEmail: body.FromEmail,
		ReplyTo: body.ReplyTo, DefaultAudiences: body.DefaultAudiences})
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewMailAccount(m))
	return nil
}

func (h *Handler) deleteMailAccount(w http.ResponseWriter, r *http.Request) error {
	mid, err := pathID(r, id.MailAccount, "mail account")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteMailAccount(r.Context(), actor(r), mid); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: r.PathValue("id"), Object: "mail_account", Deleted: true})
	return nil
}

func (h *Handler) listMailAudiences(w http.ResponseWriter, r *http.Request) error {
	mid, err := pathID(r, id.MailAccount, "mail account")
	if err != nil {
		return err
	}
	as, err := h.svc.MailAudiences(r.Context(), actor(r), mid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, list{Object: "list", Data: core.ViewAudiences(as), URL: "/v1/mail_accounts/" + r.PathValue("id") + "/audiences"})
	return nil
}

func (h *Handler) setEmailTheme(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	b, err := h.svc.ResolveBrand(r.Context(), a, r.PathValue("id"))
	if err != nil {
		return err
	}
	var body struct {
		Logo          *string `json:"logo"`
		Accent        string  `json:"accent"`
		PostalAddress string  `json:"postal_address"`
		Footer        string  `json:"footer"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	in := core.EmailThemeInput{Accent: body.Accent, PostalAddress: body.PostalAddress, Footer: body.Footer}
	if body.Logo != nil && *body.Logo != "" {
		mid, err := id.Parse(id.Media, *body.Logo)
		if err != nil {
			return badRequest("parameter_invalid", "logo", "logo must be a media ID.")
		}
		in.LogoMediaID = &mid
	}
	b, err = h.svc.SetEmailTheme(r.Context(), a, b.ID, in)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewBrand(b))
	return nil
}

// issueBody is an issue's fields in a request.
type issueBody struct {
	Brand       string `json:"brand"`
	Subject     string `json:"subject"`
	PreviewText string `json:"preview_text"`
	Body        string `json:"body"`
	Deliveries  *[]struct {
		MailAccount string   `json:"mail_account"`
		Audiences   []string `json:"audiences"`
	} `json:"deliveries"`
}

// issueInput reads an issue; withBrand resolves its brand.
func (h *Handler) issueInput(r *http.Request, withBrand bool) (core.IssueInput, error) {
	var body issueBody
	if err := decode(r, &body); err != nil {
		return core.IssueInput{}, err
	}
	in := core.IssueInput{Subject: body.Subject, PreviewText: body.PreviewText, Body: body.Body}
	if withBrand {
		b, err := h.svc.ResolveBrand(r.Context(), actor(r), body.Brand)
		if err != nil {
			return in, err
		}
		in.BrandID = b.ID
	} else if body.Brand != "" {
		return in, badRequest("parameter_unknown", "brand", "An issue's brand cannot change.")
	}
	if body.Deliveries != nil {
		in.Deliveries = []core.DeliveryInput{}
		for _, d := range *body.Deliveries {
			mid, err := id.Parse(id.MailAccount, d.MailAccount)
			if err != nil {
				return in, badRequest("parameter_invalid", "deliveries", "%q is not a mail account ID.", d.MailAccount)
			}
			in.Deliveries = append(in.Deliveries, core.DeliveryInput{MailAccountID: mid, Audiences: d.Audiences})
		}
	}
	return in, nil
}

func (h *Handler) listIssues(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	pg, err := page(r, id.Issue)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := core.IssueFilter{Status: model.IssueStatus(q.Get("status"))}
	if ref := q.Get("brand"); ref != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, ref)
		if err != nil {
			return err
		}
		f.BrandID = &b.ID
	}
	issues, more, err := h.svc.Issues(r.Context(), a, f, pg)
	if err != nil {
		return err
	}
	out := make([]core.IssueView, 0, len(issues))
	for _, is := range issues {
		out = append(out, core.ViewIssue(is))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, HasMore: more, URL: "/v1/newsletters"})
	return nil
}

func (h *Handler) createIssue(w http.ResponseWriter, r *http.Request) error {
	in, err := h.issueInput(r, true)
	if err != nil {
		return err
	}
	is, err := h.svc.CreateIssue(r.Context(), actor(r), in)
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, core.ViewIssue(is))
	return nil
}

func previewView(rd newsletter.Rendered) map[string]any {
	return map[string]any{"object": "newsletter_preview", "html": rd.HTML, "text": rd.Text}
}

func (h *Handler) previewIssueInput(w http.ResponseWriter, r *http.Request) error {
	in, err := h.issueInput(r, true)
	if err != nil {
		return err
	}
	rd, err := h.svc.PreviewIssue(r.Context(), actor(r), in)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, previewView(rd))
	return nil
}

func (h *Handler) getIssue(w http.ResponseWriter, r *http.Request) error {
	iid, err := pathID(r, id.Issue, "newsletter issue")
	if err != nil {
		return err
	}
	is, err := h.svc.Issue(r.Context(), actor(r), iid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewIssue(is))
	return nil
}

func (h *Handler) updateIssue(w http.ResponseWriter, r *http.Request) error {
	iid, err := pathID(r, id.Issue, "newsletter issue")
	if err != nil {
		return err
	}
	in, err := h.issueInput(r, false)
	if err != nil {
		return err
	}
	is, err := h.svc.UpdateIssue(r.Context(), actor(r), iid, in)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewIssue(is))
	return nil
}

func (h *Handler) renderIssue(w http.ResponseWriter, r *http.Request) error {
	iid, err := pathID(r, id.Issue, "newsletter issue")
	if err != nil {
		return err
	}
	rd, err := h.svc.IssuePreview(r.Context(), actor(r), iid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, previewView(rd))
	return nil
}

func (h *Handler) scheduleIssue(w http.ResponseWriter, r *http.Request) error {
	iid, err := pathID(r, id.Issue, "newsletter issue")
	if err != nil {
		return err
	}
	var body struct {
		SendAt string `json:"send_at"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	at, err := time.Parse(time.RFC3339, body.SendAt)
	if err != nil {
		return badRequest("parameter_invalid", "send_at", "send_at must be an RFC 3339 time.")
	}
	is, err := h.svc.ScheduleIssue(r.Context(), actor(r), iid, at)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewIssue(is))
	return nil
}

// issueAction runs a body-less action on an issue.
func (h *Handler) issueAction(fn func(context.Context, core.Actor, uuid.UUID) (*model.Issue, error)) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		iid, err := pathID(r, id.Issue, "newsletter issue")
		if err != nil {
			return err
		}
		if err := decode(r, &struct{}{}); err != nil {
			return err
		}
		is, err := fn(r.Context(), actor(r), iid)
		if err != nil {
			return err
		}
		ok(w, http.StatusOK, core.ViewIssue(is))
		return nil
	}
}

func (h *Handler) reviewIssue(approve bool) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		iid, err := pathID(r, id.Issue, "newsletter issue")
		if err != nil {
			return err
		}
		var body struct {
			Note string `json:"note"`
		}
		if err := decode(r, &body); err != nil {
			return err
		}
		is, err := h.svc.ReviewIssue(r.Context(), actor(r), iid, approve, body.Note)
		if err != nil {
			return err
		}
		ok(w, http.StatusOK, core.ViewIssue(is))
		return nil
	}
}

func (h *Handler) testIssue(w http.ResponseWriter, r *http.Request) error {
	iid, err := pathID(r, id.Issue, "newsletter issue")
	if err != nil {
		return err
	}
	var body struct {
		MailAccount string   `json:"mail_account"`
		To          []string `json:"to"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	mid, err := id.Parse(id.MailAccount, body.MailAccount)
	if err != nil {
		return badRequest("parameter_invalid", "mail_account", "mail_account must be a mail account ID.")
	}
	if err := h.svc.SendTestIssue(r.Context(), actor(r), iid, core.TestIssueInput{MailAccountID: mid, To: body.To}); err != nil {
		return err
	}
	ok(w, http.StatusOK, map[string]any{"object": "newsletter_test", "sent": len(body.To)})
	return nil
}
