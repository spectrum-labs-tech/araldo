// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"html/template"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/newsletter"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Newsletters (ADR 0024).

type newslettersData struct {
	Brands    []*model.Brand
	BrandName map[string]string
	Brand     string
	Issues    []*model.Issue
	More      bool
	Accounts  []*model.MailAccount
	// CanWrite shows "New issue"; CanConnect the account forms.
	CanWrite   bool
	CanConnect bool
	Providers  []core.MailProviderInfo
	Provider   *core.MailProviderInfo
	Values     map[string]string
	// For the guide.
	HasDefaults bool
	HasAddress  bool
	HasSent     bool
}

func (s *Server) newslettersPage(c *reqCtx) error {
	d, err := s.newslettersData(c)
	if err != nil {
		return err
	}
	return s.page(c, "newsletters", "newsletters", "Newsletters", d)
}

func (s *Server) newslettersData(c *reqCtx) (*newslettersData, error) {
	q := c.r.URL.Query()
	d := &newslettersData{Brand: q.Get("brand"), BrandName: map[string]string{}, CanWrite: c.actor.Can(core.PermNewslettersWrite),
		CanConnect: c.actor.Can(core.PermChannelsWrite), Providers: s.svc.MailProviders(c.actor.Livemode), Values: map[string]string{}}
	for i := range d.Providers {
		if string(d.Providers[i].Provider) == q.Get("provider") || d.Provider == nil {
			d.Provider = &d.Providers[i]
		}
	}
	var err error
	if d.Brands, err = s.svc.Brands(c.ctx(), c.actor); err != nil {
		return nil, err
	}
	var f core.IssueFilter
	for _, b := range d.Brands {
		d.BrandName[id.Format(id.Brand, b.ID)] = b.Name
		if d.Brand == "" || d.Brand == id.Format(id.Brand, b.ID) || d.Brand == b.Slug {
			d.HasAddress = d.HasAddress || b.EmailTheme.PostalAddress != ""
		}
	}
	if d.Brand != "" {
		b, err := s.svc.ResolveBrand(c.ctx(), c.actor, d.Brand)
		if err != nil {
			return nil, err
		}
		f.BrandID = &b.ID
	}
	if d.Issues, d.More, err = s.svc.Issues(c.ctx(), c.actor, f, store.Page{Limit: 50}); err != nil {
		return nil, err
	}
	for _, is := range d.Issues {
		d.HasSent = d.HasSent || is.Status != model.IssueDraft
	}
	if d.Accounts, err = s.svc.MailAccounts(c.ctx(), c.actor, f.BrandID); err != nil {
		return nil, err
	}
	for _, a := range d.Accounts {
		d.HasDefaults = d.HasDefaults || len(a.DefaultAudiences) > 0
	}
	return d, nil
}

func (s *Server) connectMailAccount(c *reqCtx) error {
	f := c.r.PostForm
	d, err := s.newslettersData(c)
	if err != nil {
		return err
	}
	in := core.MailAccountInput{Provider: email.Provider(f.Get("provider")), FromName: f.Get("from_name"), FromEmail: f.Get("from_email"),
		ReplyTo: f.Get("reply_to"), Fields: map[string]string{}}
	d.Values["from_name"], d.Values["from_email"], d.Values["reply_to"] = in.FromName, in.FromEmail, in.ReplyTo
	for k, v := range f {
		if name, ok := strings.CutPrefix(k, "field_"); ok && len(v) > 0 {
			in.Fields[name] = v[0]
			d.Values[name] = v[0]
		}
	}
	for i := range d.Providers {
		if d.Providers[i].Provider == in.Provider {
			d.Provider = &d.Providers[i]
		}
	}
	b, err := s.svc.ResolveBrand(c.ctx(), c.actor, f.Get("brand"))
	if err != nil {
		return s.formErr(c, "newsletters", "newsletters", "Newsletters", d, err)
	}
	in.BrandID = b.ID
	ma, err := s.svc.ConnectMailAccount(c.ctx(), c.actor, in)
	if err != nil {
		return s.formErr(c, "newsletters", "newsletters", "Newsletters", d, err)
	}
	return redirect(c, "/mail-accounts/"+id.Format(id.MailAccount, ma.ID), "Connected "+ma.Name+". Now choose the audiences new issues go to.")
}

type mailAccountData struct {
	Account   *model.MailAccount
	BrandName string
	Audiences []email.Audience
	// AudienceErr says why the provider's audiences could not be read.
	AudienceErr string
	CanWrite    bool
	Form        core.MailAccountUpdate
}

func (s *Server) mailAccountData(c *reqCtx) (*mailAccountData, error) {
	aid, err := pathUUID(c, id.MailAccount, "mail account")
	if err != nil {
		return nil, err
	}
	ma, err := s.svc.MailAccount(c.ctx(), c.actor, aid)
	if err != nil {
		return nil, err
	}
	d := &mailAccountData{Account: ma, CanWrite: c.actor.Can(core.PermChannelsWrite),
		Form: core.MailAccountUpdate{FromName: ma.FromName, FromEmail: ma.FromEmail, ReplyTo: ma.ReplyTo, DefaultAudiences: ma.DefaultAudiences}}
	if b, err := s.svc.Brand(c.ctx(), c.actor, ma.BrandID); err == nil {
		d.BrandName = b.Name
	}
	if d.Audiences, err = s.svc.MailAudiences(c.ctx(), c.actor, aid); err != nil {
		d.AudienceErr = apperr.As(err).Message
	}
	return d, nil
}

func (s *Server) mailAccountPage(c *reqCtx) error {
	d, err := s.mailAccountData(c)
	if err != nil {
		return err
	}
	return s.page(c, "newsletter_account", "newsletters", d.Account.Name, d)
}

func (s *Server) saveMailAccount(c *reqCtx) error {
	d, err := s.mailAccountData(c)
	if err != nil {
		return err
	}
	f := c.r.PostForm
	d.Form = core.MailAccountUpdate{FromName: f.Get("from_name"), FromEmail: f.Get("from_email"), ReplyTo: f.Get("reply_to"),
		DefaultAudiences: f["audience"]}
	if _, err := s.svc.UpdateMailAccount(c.ctx(), c.actor, d.Account.ID, d.Form); err != nil {
		return s.formErr(c, "newsletter_account", "newsletters", d.Account.Name, d, err)
	}
	return redirect(c, "/newsletters", "Saved "+d.Account.Name+".")
}

func (s *Server) deleteMailAccount(c *reqCtx) error {
	aid, err := pathUUID(c, id.MailAccount, "mail account")
	if err != nil {
		return err
	}
	if err := s.svc.DeleteMailAccount(c.ctx(), c.actor, aid); err != nil {
		return err
	}
	return redirect(c, "/newsletters", "Disconnected.")
}

// issueData is the editor: an issue (nil for a new one), the form, and
// the brand's accounts with their audiences to choose from.
type issueData struct {
	Issue    *model.Issue
	Brand    *model.Brand
	Brands   []*model.Brand
	Subject  string
	Preview  string
	Body     string
	Accounts []accountChoice
	// Rendered is the issue as sent, for the plain-text view.
	Rendered *newsletter.Rendered
	// CanWrite edits and schedules; CanReview approves.
	CanWrite  bool
	CanReview bool
	SendAt    string
	// TestTo is the test form's addresses, kept across a failed send.
	TestTo string
}

// accountChoice is a mail account in the editor: whether the issue goes
// through it, and which audiences.
type accountChoice struct {
	Account     *model.MailAccount
	Use         bool
	Audiences   []email.Audience
	Chosen      []string
	AudienceErr string
}

// Editable reports whether the form can change the issue.
func (d *issueData) Editable() bool {
	return d.CanWrite && (d.Issue == nil || d.Issue.Status == model.IssueDraft)
}

// Field is a form field's name, for the accessibility of repeated inputs.
func (a accountChoice) Field() string { return "audience_" + id.Format(id.MailAccount, a.Account.ID) }

func (s *Server) issueData(c *reqCtx, is *model.Issue, brandRef string) (*issueData, error) {
	d := &issueData{Issue: is, CanWrite: c.actor.Can(core.PermNewslettersWrite), CanReview: c.actor.Can(core.PermPostsApprove)}
	var err error
	if d.Brands, err = s.svc.Brands(c.ctx(), c.actor); err != nil {
		return nil, err
	}
	switch {
	case is != nil:
		d.Brand, err = s.svc.Brand(c.ctx(), c.actor, is.BrandID)
		d.Subject, d.Preview, d.Body = is.Subject, is.PreviewText, is.Body
		if is.SendAt != nil {
			d.SendAt = is.SendAt.In(brandLoc(d.Brand)).Format("2006-01-02T15:04")
		}
	case brandRef != "":
		d.Brand, err = s.svc.ResolveBrand(c.ctx(), c.actor, brandRef)
	case len(d.Brands) > 0:
		d.Brand = d.Brands[0]
	}
	if err != nil {
		return nil, err
	}
	if d.Brand == nil {
		return d, nil
	}
	accts, err := s.svc.MailAccounts(c.ctx(), c.actor, &d.Brand.ID)
	if err != nil {
		return nil, err
	}
	for _, ma := range accts {
		ac := accountChoice{Account: ma}
		if is == nil {
			ac.Use, ac.Chosen = ma.Status == model.AdAccountActive && len(ma.DefaultAudiences) > 0, ma.DefaultAudiences
		}
		for _, dl := range deliveriesOf(is) {
			if dl.MailAccountID != nil && *dl.MailAccountID == ma.ID {
				ac.Use = true
				for _, a := range dl.Audiences {
					ac.Chosen = append(ac.Chosen, a.ID)
				}
			}
		}
		if d.Editable() {
			if ac.Audiences, err = s.svc.MailAudiences(c.ctx(), c.actor, ma.ID); err != nil {
				ac.AudienceErr = apperr.As(err).Message
			}
		}
		d.Accounts = append(d.Accounts, ac)
	}
	if is != nil {
		if r, err := s.svc.IssuePreview(c.ctx(), c.actor, is.ID); err == nil {
			d.Rendered = &r
		}
	}
	return d, nil
}

func deliveriesOf(is *model.Issue) []model.IssueDelivery {
	if is == nil {
		return nil
	}
	return is.Deliveries
}

// formInput reads the editor's form into d and an IssueInput.
func (d *issueData) formInput(c *reqCtx) core.IssueInput {
	f := c.r.PostForm
	d.Subject, d.Preview, d.Body = f.Get("subject"), f.Get("preview_text"), f.Get("body")
	in := core.IssueInput{Subject: d.Subject, PreviewText: d.Preview, Body: d.Body, Deliveries: []core.DeliveryInput{}}
	if d.Brand != nil {
		in.BrandID = d.Brand.ID
	}
	for i := range d.Accounts {
		ac := &d.Accounts[i]
		ac.Use = slices.Contains(f["account"], id.Format(id.MailAccount, ac.Account.ID))
		ac.Chosen = f[ac.Field()]
		if ac.Use {
			in.Deliveries = append(in.Deliveries, core.DeliveryInput{MailAccountID: ac.Account.ID, Audiences: ac.Chosen})
		}
	}
	return in
}

func (s *Server) newIssue(c *reqCtx) error {
	d, err := s.issueData(c, nil, c.r.URL.Query().Get("brand"))
	if err != nil {
		return err
	}
	return s.page(c, "newsletter_edit", "newsletters", "New issue", d)
}

func (s *Server) createIssue(c *reqCtx) error {
	d, err := s.issueData(c, nil, c.r.PostFormValue("brand"))
	if err != nil {
		return err
	}
	in := d.formInput(c)
	is, err := s.svc.CreateIssue(c.ctx(), c.actor, in)
	if err != nil {
		return s.formErr(c, "newsletter_edit", "newsletters", "New issue", d, err)
	}
	return redirect(c, "/newsletters/"+id.Format(id.Issue, is.ID), "Saved the draft.")
}

func (s *Server) loadIssue(c *reqCtx) (*model.Issue, error) {
	iid, err := pathUUID(c, id.Issue, "newsletter issue")
	if err != nil {
		return nil, err
	}
	return s.svc.Issue(c.ctx(), c.actor, iid)
}

func (s *Server) issuePage(c *reqCtx) error {
	is, err := s.loadIssue(c)
	if err != nil {
		return err
	}
	d, err := s.issueData(c, is, "")
	if err != nil {
		return err
	}
	return s.page(c, "newsletter_edit", "newsletters", is.Subject, d)
}

func (s *Server) saveIssue(c *reqCtx) error {
	is, err := s.loadIssue(c)
	if err != nil {
		return err
	}
	d, err := s.issueData(c, is, "")
	if err != nil {
		return err
	}
	in := d.formInput(c)
	if _, err := s.svc.UpdateIssue(c.ctx(), c.actor, is.ID, in); err != nil {
		return s.formErr(c, "newsletter_edit", "newsletters", is.Subject, d, err)
	}
	return redirect(c, "/newsletters/"+c.r.PathValue("id"), "Saved the draft.")
}

// issueAction runs one of the editor's buttons and comes back to it.
func (s *Server) issueAction(c *reqCtx) error {
	is, err := s.loadIssue(c)
	if err != nil {
		return err
	}
	f := c.r.PostForm
	var notice string
	switch f.Get("action") {
	case "schedule":
		var at string
		b, berr := s.svc.Brand(c.ctx(), c.actor, is.BrandID)
		if berr != nil {
			return berr
		}
		if at, err = brandTime(b, f.Get("send_at"), "send_at"); err == nil {
			t, _ := time.Parse(time.RFC3339, at)
			was := is.Status
			if is, err = s.svc.ScheduleIssue(c.ctx(), c.actor, is.ID, t); err == nil {
				notice = "Scheduled."
				switch {
				case was != model.IssueDraft:
					notice = "Moved."
				case is.Status == model.IssuePendingApproval:
					notice = "Sent for approval."
				}
			}
		}
	case "unschedule":
		if _, err = s.svc.UnscheduleIssue(c.ctx(), c.actor, is.ID); err == nil {
			notice = "Back to draft; it needs scheduling again."
		}
	case "cancel":
		if _, err = s.svc.CancelIssue(c.ctx(), c.actor, is.ID); err == nil {
			notice = "Canceled."
		}
	case "approve", "reject":
		approve := f.Get("action") == "approve"
		if _, err = s.svc.ReviewIssue(c.ctx(), c.actor, is.ID, approve, f.Get("note")); err == nil {
			notice = "Approved."
			if !approve {
				notice = "Rejected: it is a draft again."
			}
		}
	case "test":
		var acct uuid.UUID
		if acct, err = id.Parse(id.MailAccount, f.Get("test_account")); err != nil {
			err = apperr.Invalid("mail_account_required", "test_account", "Choose a mail account to send the test through.")
		} else {
			to := strings.FieldsFunc(f.Get("test_to"), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' })
			if err = s.svc.SendTestIssue(c.ctx(), c.actor, is.ID, core.TestIssueInput{MailAccountID: acct, To: to}); err == nil {
				notice = "Test sent."
			}
		}
	default:
		err = apperr.Invalid("action_unknown", "action", "Unknown action.")
	}
	if err != nil {
		cur, lerr := s.loadIssue(c)
		if lerr != nil {
			return lerr
		}
		d, derr := s.issueData(c, cur, "")
		if derr != nil {
			return derr
		}
		if f.Get("send_at") != "" {
			d.SendAt = f.Get("send_at")
		}
		d.TestTo = f.Get("test_to")
		return s.formErr(c, "newsletter_edit", "newsletters", cur.Subject, d, err)
	}
	return redirect(c, "/newsletters/"+c.r.PathValue("id"), notice)
}

// previewCSP lets a preview frame show an email as a mail client would:
// its inline styles and remote images, but no scripts, forms or plugins,
// in a sandbox of its own origin, framed only by the dashboard.
const previewCSP = "default-src 'none'; img-src https: data: 'self'; style-src 'unsafe-inline'; frame-ancestors 'self'; sandbox"

func (s *Server) writePreview(c *reqCtx, html string) error {
	h := c.w.Header()
	h.Set("Content-Security-Policy", previewCSP)
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	_, err := c.w.Write([]byte(html))
	return err
}

// issuePreview is a stored issue's HTML, for the editor's frames.
func (s *Server) issuePreview(c *reqCtx) error {
	iid, err := pathUUID(c, id.Issue, "newsletter issue")
	if err != nil {
		return err
	}
	r, err := s.svc.IssuePreview(c.ctx(), c.actor, iid)
	if err != nil {
		return err
	}
	return s.writePreview(c, r.HTML)
}

// renderPreview renders the editor's unsaved form into its preview frame.
func (s *Server) renderPreview(c *reqCtx) error {
	f := c.r.PostForm
	in := core.IssueInput{Subject: f.Get("subject"), PreviewText: f.Get("preview_text"), Body: f.Get("body")}
	b, err := s.svc.ResolveBrand(c.ctx(), c.actor, f.Get("brand"))
	if err == nil {
		in.BrandID = b.ID
		for _, ref := range f["account"] {
			if aid, perr := id.Parse(id.MailAccount, ref); perr == nil {
				in.Deliveries = append(in.Deliveries, core.DeliveryInput{MailAccountID: aid})
			}
		}
		var r newsletter.Rendered
		if r, err = s.svc.PreviewIssue(c.ctx(), c.actor, in); err == nil {
			return s.writePreview(c, r.HTML)
		}
	}
	c.w.WriteHeader(http.StatusUnprocessableEntity)
	return s.writePreview(c, `<!DOCTYPE html><html lang="en"><body style="font-family:sans-serif;padding:16px;color:#b91c1c">`+
		template.HTMLEscapeString(apperr.As(err).Message)+problems(err)+`</body></html>`)
}

// problems lists an error's problems as HTML.
func problems(err error) string {
	var b strings.Builder
	for _, p := range apperr.As(err).Problems {
		b.WriteString("<p>" + template.HTMLEscapeString(p.Message) + "</p>")
	}
	return b.String()
}

// themeForm is the brand page's email theme form.
type themeForm struct {
	Logo, Accent, PostalAddress, Footer string
	Images                              []*model.Media
}

func (s *Server) saveEmailTheme(c *reqCtx) error {
	bid, err := pathUUID(c, id.Brand, "brand")
	if err != nil {
		return err
	}
	f := c.r.PostForm
	in := core.EmailThemeInput{Accent: f.Get("accent"), PostalAddress: f.Get("postal_address"), Footer: f.Get("footer")}
	if ref := f.Get("logo"); ref != "" {
		mid, err := id.Parse(id.Media, ref)
		if err != nil {
			return apperr.Invalid("logo_unknown", "logo", "Choose one of the brand's images.")
		}
		in.LogoMediaID = &mid
	}
	if in.Accent == newsletter.DefaultAccent {
		in.Accent = ""
	}
	if _, err := s.svc.SetEmailTheme(c.ctx(), c.actor, bid, in); err != nil {
		b, berr := s.svc.Brand(c.ctx(), c.actor, bid)
		if berr != nil {
			return berr
		}
		bf, ferr := s.brandForm(c, b)
		if ferr != nil {
			return ferr
		}
		bf.Theme = themeForm{Logo: f.Get("logo"), Accent: f.Get("accent"), PostalAddress: in.PostalAddress, Footer: in.Footer,
			Images: bf.Theme.Images}
		return s.formErr(c, "brand_edit", "brands", b.Name, bf, err)
	}
	return redirect(c, "/brands/"+c.r.PathValue("id"), "Saved the email theme.")
}
