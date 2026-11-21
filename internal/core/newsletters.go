// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/newsletter"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
	"github.com/spectrum-labs-tech/araldo/internal/tmpl"
	"github.com/spectrum-labs-tech/araldo/internal/utm"
)

// Newsletters (ADR 0024): Araldo designs, approves and schedules an issue;
// each mail account's provider owns its list and sends.
const (
	// HandoffAhead is how long before its send time an issue is handed to
	// the providers, which then send it without Araldo.
	HandoffAhead = 24 * time.Hour
	// MinSendDelay keeps a send time far enough ahead for the hand-off.
	MinSendDelay = 2 * time.Minute
	// MaxTestRecipients bounds a test send.
	MaxTestRecipients = 5
	maxSubject        = 200
	maxPreviewText    = 200
	maxIssueBody      = 100_000
	maxThemeAddress   = 300
	maxThemeFooter    = 300
	handoffLease      = 2 * time.Minute
	handoffBatch      = 20
	handoffGiveUp     = 6 * time.Hour
	readLease         = 10 * time.Minute
	notSentGiveUp     = 24 * time.Hour
	firstLookAfter    = 10 * time.Minute
	lookAgain         = 15 * time.Minute
)

// ResultReads are when a sent delivery's results are read, after it was
// sent (ADR 0024 decision 15).
var ResultReads = []time.Duration{time.Hour, 24 * time.Hour, 3 * 24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour}

func mailCredentialsAAD(accountID uuid.UUID) string {
	return keyring.AAD("mail_accounts", "credentials", accountID)
}

// MailProviderInfo describes an email provider for connect forms.
type MailProviderInfo struct {
	Provider email.Provider
	Name     string
	Fields   []platform.Field
}

// MailProviders lists the providers accounts can connect to in a mode: the
// sandbox in test mode, the install's real providers in live mode.
func (s *Service) MailProviders(livemode bool) []MailProviderInfo {
	var out []MailProviderInfo
	for _, p := range s.mailers.Providers() {
		if (p == email.Sandbox) == livemode {
			continue
		}
		m, _ := s.mailers.Get(p)
		out = append(out, MailProviderInfo{Provider: p, Name: m.Name(), Fields: m.Fields()})
	}
	return out
}

// MailAccountInput connects a mail account.
type MailAccountInput struct {
	BrandID          uuid.UUID
	Provider         email.Provider
	FromName         string
	FromEmail        string
	ReplyTo          string
	DefaultAudiences []string
	Fields           map[string]string
}

// checkSender validates a sender and reply-to address.
func checkSender(fromName, fromEmail, replyTo string, ps *apperr.Problems) (string, string, string) {
	fromName, fromEmail, replyTo = strings.TrimSpace(fromName), strings.TrimSpace(fromEmail), strings.TrimSpace(replyTo)
	if fromName == "" || utf8.RuneCountInString(fromName) > 100 {
		ps.Add("from_name_invalid", "from_name", "A sender needs a name of 1 to 100 characters.")
	}
	if !plainAddress(fromEmail) {
		ps.Add("from_email_invalid", "from_email", "The sender's address is not an email address.")
	}
	if replyTo != "" && !plainAddress(replyTo) {
		ps.Add("reply_to_invalid", "reply_to", "The reply-to address is not an email address.")
	}
	return fromName, strings.ToLower(fromEmail), replyTo
}

// plainAddress reports whether s is a bare address, without a name.
func plainAddress(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && a.Name == ""
}

// ConnectMailAccount checks credentials and the sender with the provider,
// and stores the account.
func (s *Service) ConnectMailAccount(ctx context.Context, a Actor, in MailAccountInput) (*model.MailAccount, error) {
	if err := a.require(PermChannelsWrite); err != nil {
		return nil, err
	}
	b, err := s.Brand(ctx, a, in.BrandID)
	if err != nil {
		return nil, err
	}
	switch {
	case !a.Livemode && in.Provider != email.Sandbox:
		return nil, apperr.Invalid("livemode_required", "provider", "Test mode can only connect sandbox mail accounts.")
	case a.Livemode && in.Provider == email.Sandbox:
		return nil, apperr.Invalid("testmode_required", "provider", "Sandbox mail accounts exist only in test mode.")
	}
	m, ok := s.mailers.Get(in.Provider)
	if !ok {
		return nil, apperr.Invalid("provider_unsupported", "provider", "This install cannot send newsletters through %q.", in.Provider)
	}
	var ps apperr.Problems
	fromName, fromEmail, replyTo := checkSender(in.FromName, in.FromEmail, in.ReplyTo, &ps)
	settings, secrets, err := splitFields(m.Name()+" accounts", m.Fields(), in.Fields)
	if err != nil {
		return nil, err
	}
	if err := ps.Err("The mail account is not valid."); err != nil {
		return nil, err
	}
	creds := mergeCredentials(settings, secrets)
	acct, err := callProvider(ctx, func(ctx context.Context) (email.Account, error) {
		return m.Verify(ctx, creds, email.Address{Name: fromName, Email: fromEmail})
	})
	if err != nil {
		return nil, connectError(platform.Provider(m.Name()), err)
	}
	defaults, err := s.checkAudiences(ctx, m, creds, in.DefaultAudiences, "default_audiences")
	if err != nil {
		return nil, err
	}
	ma := &model.MailAccount{ID: id.New(), OrgID: a.OrgID, BrandID: b.ID, Livemode: a.Livemode, Provider: string(in.Provider),
		ExternalID: acct.ExternalID, Name: acct.Name, FromName: fromName, FromEmail: fromEmail, ReplyTo: replyTo,
		DefaultAudiences: audienceIDs(defaults), Settings: settings, Status: model.ConnectionActive}
	if ma.Credentials, err = sealSecrets(ctx, s.keys, a.OrgID, mailCredentialsAAD(ma.ID), secrets); err != nil {
		return nil, err
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateMailAccount(ctx, ma); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Conflict("mail_account_connected", "%s, sending as %s, is already connected to this brand.", acct.Name, fromEmail)
			}
			return err
		}
		return s.audit(ctx, tx, a, "mail_account.connect", id.Format(id.MailAccount, ma.ID), map[string]any{"provider": in.Provider})
	})
	return ma, err
}

// checkAudiences reads the provider's audiences and returns the ones
// named, in order, refusing any it does not have.
func (s *Service) checkAudiences(ctx context.Context, m email.Mailer, creds platform.Credentials, ids []string, field string) ([]model.Audience, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	all, err := m.Audiences(ctx, creds)
	if err != nil {
		return nil, connectError(platform.Provider(m.Name()), err)
	}
	byID := map[string]email.Audience{}
	for _, au := range all {
		byID[au.ID] = au
	}
	var out []model.Audience
	var ps apperr.Problems
	seen := map[string]bool{}
	for _, raw := range ids {
		aid := strings.TrimSpace(raw)
		au, ok := byID[aid]
		switch {
		case !ok:
			ps.Add("audience_unknown", field, "%s has no audience %q.", m.Name(), aid)
		case !seen[aid]:
			seen[aid] = true
			out = append(out, model.Audience{ID: au.ID, Name: au.Name, Kind: au.Kind, Size: au.Size})
		}
	}
	if err := ps.Err("Some audiences are not the provider's."); err != nil {
		return nil, err
	}
	if limit := email.MaxAudiences(m); limit > 0 && len(out) > limit {
		n := fmt.Sprintf("at most %d audiences", limit)
		if limit == 1 {
			n = "one audience"
		}
		return nil, apperr.Invalid("audience_too_many", field, "%s sends a campaign to %s; choose fewer, or make one in %s that covers them.", m.Name(), n, m.Name())
	}
	return out, nil
}

func audienceIDs(as []model.Audience) []string {
	out := make([]string, len(as))
	for i, au := range as {
		out[i] = au.ID
	}
	return out
}

// MailAccounts lists the actor's mail accounts, optionally for one brand.
func (s *Service) MailAccounts(ctx context.Context, a Actor, brandID *uuid.UUID) ([]*model.MailAccount, error) {
	if err := a.requireAny(PermChannelsRead, PermNewslettersRead); err != nil {
		return nil, err
	}
	if a.BrandID != nil {
		if brandID != nil && *brandID != *a.BrandID {
			return nil, apperr.NotFound("brand")
		}
		brandID = a.BrandID
	}
	return s.store.MailAccounts(ctx, a.OrgID, a.Livemode, brandID)
}

// MailAccount returns one of the actor's mail accounts.
func (s *Service) MailAccount(ctx context.Context, a Actor, accountID uuid.UUID) (*model.MailAccount, error) {
	if err := a.requireAny(PermChannelsRead, PermNewslettersRead); err != nil {
		return nil, err
	}
	ma, err := s.store.MailAccount(ctx, a.OrgID, accountID)
	if err != nil {
		return nil, notFound(err, "mail account")
	}
	if ma.Livemode != a.Livemode || a.brandAllowed(ma.BrandID) != nil {
		return nil, apperr.NotFound("mail account")
	}
	return ma, nil
}

// MailAccountUpdate changes an account's sender and default audiences.
type MailAccountUpdate struct {
	FromName         string
	FromEmail        string
	ReplyTo          string
	DefaultAudiences []string
}

// UpdateMailAccount changes an account's sender, checked with the provider
// when it changes, and its default audiences.
func (s *Service) UpdateMailAccount(ctx context.Context, a Actor, accountID uuid.UUID, in MailAccountUpdate) (*model.MailAccount, error) {
	if err := a.require(PermChannelsWrite); err != nil {
		return nil, err
	}
	ma, err := s.MailAccount(ctx, a, accountID)
	if err != nil {
		return nil, err
	}
	var ps apperr.Problems
	fromName, fromEmail, replyTo := checkSender(in.FromName, in.FromEmail, in.ReplyTo, &ps)
	if err := ps.Err("The mail account is not valid."); err != nil {
		return nil, err
	}
	m, creds, err := s.mailerFor(ctx, ma)
	if err != nil {
		return nil, err
	}
	if fromEmail != ma.FromEmail {
		if _, err := callProvider(ctx, func(ctx context.Context) (email.Account, error) {
			return m.Verify(ctx, creds, email.Address{Name: fromName, Email: fromEmail})
		}); err != nil {
			return nil, connectError(platform.Provider(m.Name()), err)
		}
	}
	defaults, err := s.checkAudiences(ctx, m, creds, in.DefaultAudiences, "default_audiences")
	if err != nil {
		return nil, err
	}
	ma.FromName, ma.FromEmail, ma.ReplyTo, ma.DefaultAudiences = fromName, fromEmail, replyTo, audienceIDs(defaults)
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.UpdateMailAccount(ctx, ma); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Conflict("mail_account_connected", "This brand already sends as %s through that account.", fromEmail)
			}
			return err
		}
		return s.audit(ctx, tx, a, "mail_account.update", id.Format(id.MailAccount, ma.ID), nil)
	})
	return ma, err
}

// DeleteMailAccount forgets a mail account. One with issues still to send
// is refused: cancel them, or send them elsewhere, first.
func (s *Service) DeleteMailAccount(ctx context.Context, a Actor, accountID uuid.UUID) error {
	if err := a.require(PermChannelsWrite); err != nil {
		return err
	}
	ma, err := s.MailAccount(ctx, a, accountID)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		n, err := tx.OpenNewsletterDeliveries(ctx, a.OrgID, ma.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			return apperr.Conflict("mail_account_in_use", "%d scheduled newsletter issues go through this account; cancel them first.", n)
		}
		if err := tx.DeleteMailAccount(ctx, a.OrgID, ma.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "mail_account.delete", id.Format(id.MailAccount, ma.ID), map[string]any{"provider": ma.Provider})
	})
}

// mailerFor returns an account's provider and credentials.
func (s *Service) mailerFor(ctx context.Context, ma *model.MailAccount) (email.Mailer, platform.Credentials, error) {
	m, ok := s.mailers.Get(email.Provider(ma.Provider))
	if !ok {
		return nil, nil, apperr.Conflict("provider_unsupported", "This install cannot send through %s.", ma.Provider)
	}
	creds, err := openCredentials(ctx, s.keys, ma.OrgID, mailCredentialsAAD(ma.ID), ma.Settings, ma.Credentials)
	if err != nil {
		return nil, nil, err
	}
	return m, creds, nil
}

// MailAudiences lists what an account's provider can send to.
func (s *Service) MailAudiences(ctx context.Context, a Actor, accountID uuid.UUID) ([]email.Audience, error) {
	ma, err := s.MailAccount(ctx, a, accountID)
	if err != nil {
		return nil, err
	}
	m, creds, err := s.mailerFor(ctx, ma)
	if err != nil {
		return nil, err
	}
	out, err := m.Audiences(ctx, creds)
	if err != nil {
		s.flagRevoked(ctx, ma, err)
		return nil, connectError(platform.Provider(m.Name()), err)
	}
	return out, nil
}

// flagRevoked marks an account whose provider refused its credentials.
func (s *Service) flagRevoked(ctx context.Context, ma *model.MailAccount, err error) {
	if platform.KindOf(err) != platform.AuthRevoked {
		return
	}
	note := truncate("The provider refused the key: connect the account again. "+err.Error(), 500)
	if serr := s.store.SetMailAccountStatus(ctx, ma.OrgID, ma.ID, model.ConnectionNeedsReauth, note); serr != nil {
		s.log.WarnContext(ctx, "flagging a mail account failed", "mail_account", id.Format(id.MailAccount, ma.ID), "err", serr)
	}
}

// EmailThemeInput sets a brand's look in newsletters.
type EmailThemeInput struct {
	LogoMediaID   *uuid.UUID
	Accent        string
	PostalAddress string
	Footer        string
}

// SetEmailTheme sets a brand's email theme. The accent must read as link
// text on white; the logo must be one of the brand's images.
func (s *Service) SetEmailTheme(ctx context.Context, a Actor, brandID uuid.UUID, in EmailThemeInput) (*model.Brand, error) {
	if err := a.require(PermBrandsWrite); err != nil {
		return nil, err
	}
	b, err := s.Brand(ctx, a, brandID)
	if err != nil {
		return nil, err
	}
	t := model.EmailTheme{LogoMediaID: in.LogoMediaID, Accent: strings.ToLower(strings.TrimSpace(in.Accent)),
		PostalAddress: strings.TrimSpace(strings.ReplaceAll(in.PostalAddress, "\r\n", "\n")), Footer: strings.TrimSpace(in.Footer)}
	var ps apperr.Problems
	if t.Accent != "" && !newsletter.ValidAccent(t.Accent) {
		ps.Add("accent_invalid", "accent", `The accent is a color like "#1d4ed8" dark enough to read on white (4.5:1).`)
	}
	if utf8.RuneCountInString(t.PostalAddress) > maxThemeAddress {
		ps.Add("postal_address_too_long", "postal_address", "The postal address is at most %d characters.", maxThemeAddress)
	}
	if utf8.RuneCountInString(t.Footer) > maxThemeFooter || strings.Contains(t.Footer, "\n") {
		ps.Add("footer_invalid", "footer", "The footer is one line of at most %d characters.", maxThemeFooter)
	}
	if t.LogoMediaID != nil {
		m, err := s.store.Media(ctx, a.OrgID, *t.LogoMediaID)
		switch {
		case errors.Is(err, store.ErrNotFound) || err == nil && m.BrandID != b.ID:
			ps.Add("logo_unknown", "logo", "The logo must be one of the brand's images.")
		case err != nil:
			return nil, err
		}
	}
	if err := ps.Err("The email theme is not valid."); err != nil {
		return nil, err
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.UpdateBrandEmailTheme(ctx, a.OrgID, b.ID, t); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "brand.email_theme", id.Format(id.Brand, b.ID), nil)
	})
	if err != nil {
		return nil, err
	}
	b.EmailTheme = t
	return b, nil
}

// IssueInput creates or edits an issue.
type IssueInput struct {
	BrandID     uuid.UUID
	Subject     string
	PreviewText string
	// Body is the Markdown subset internal/newsletter renders.
	Body string
	// Template is a newsletter template ("key", "key@3" or its ID) that
	// renders the subject, preview text and body from Data instead; those
	// three are then left empty (ADR 0024 phase 2).
	Template string
	Data     json.RawMessage
	// Deliveries are the mail accounts and their audiences it goes to. Nil
	// means, for a new issue, each of the brand's active accounts with
	// default audiences, and for an edit, the ones it has.
	Deliveries []DeliveryInput
}

// DeliveryInput is one mail account an issue goes to, and its audiences
// there (empty: the account's defaults).
type DeliveryInput struct {
	MailAccountID uuid.UUID
	Audiences     []string
}

// issuePlan is a checked issue, ready to store.
type issuePlan struct {
	brand      *model.Brand
	deliveries []model.IssueDelivery
	media      []uuid.UUID
	// template and version rendered the text, from data; nil when it was
	// written by hand.
	template *model.Template
	version  *model.TemplateVersion
	data     json.RawMessage
}

// issueFromTemplate renders one of brand b's newsletter templates with
// data into an issue's subject, preview text and body.
func (s *Service) issueFromTemplate(ctx context.Context, a Actor, b *model.Brand, ref string, data json.RawMessage) (*model.Template,
	*model.TemplateVersion, tmpl.Issue, error) {
	t, v, err := s.TemplateByRef(ctx, a, b.ID, ref)
	if err != nil {
		return nil, nil, tmpl.Issue{}, err
	}
	if t.Kind != model.TemplateNewsletter {
		return nil, nil, tmpl.Issue{}, apperr.Invalid("template_kind", "template", "%s is a post template; issues use newsletter templates.", t.Key)
	}
	c, err := tmpl.Compile(sourceOf(v))
	if err != nil {
		return nil, nil, tmpl.Issue{}, err
	}
	out, err := renderTemplateIssue(c, data, location(b.Timezone))
	return t, v, out, err
}

// fillFromTemplate renders in.Template into in's subject, preview text and
// body, refusing text given beside it.
func (s *Service) fillFromTemplate(ctx context.Context, a Actor, b *model.Brand, in *IssueInput) (*model.Template, *model.TemplateVersion, error) {
	if in.Subject != "" || in.PreviewText != "" || strings.TrimSpace(in.Body) != "" {
		return nil, nil, apperr.Invalid("content_conflict", "template", "Send either a template with data, or a subject and body, not both.")
	}
	t, v, out, err := s.issueFromTemplate(ctx, a, b, in.Template, in.Data)
	if err != nil {
		return nil, nil, err
	}
	in.Subject, in.PreviewText, in.Body = out.Subject, out.PreviewText, out.Body
	return t, v, nil
}

// prepareIssue checks an issue and resolves its deliveries' audiences.
func (s *Service) prepareIssue(ctx context.Context, a Actor, in *IssueInput) (*issuePlan, error) {
	if err := a.require(PermNewslettersWrite); err != nil {
		return nil, err
	}
	b, err := s.Brand(ctx, a, in.BrandID)
	if err != nil {
		return nil, err
	}
	pl := &issuePlan{brand: b}
	if in.Template != "" {
		if pl.template, pl.version, err = s.fillFromTemplate(ctx, a, b, in); err != nil {
			return nil, err
		}
		if d := bytes.TrimSpace(in.Data); len(d) > 0 && string(d) != "null" {
			pl.data = d
		}
	}
	in.Subject, in.PreviewText = strings.TrimSpace(in.Subject), strings.TrimSpace(in.PreviewText)
	in.Body = strings.TrimSpace(strings.ReplaceAll(in.Body, "\r\n", "\n"))
	var ps apperr.Problems
	if n := utf8.RuneCountInString(in.Subject); n == 0 || n > maxSubject {
		ps.Add("subject_invalid", "subject", "A subject is 1 to %d characters.", maxSubject)
	}
	if utf8.RuneCountInString(in.PreviewText) > maxPreviewText {
		ps.Add("preview_text_too_long", "preview_text", "Preview text is at most %d characters.", maxPreviewText)
	}
	if n := len(in.Body); n == 0 || n > maxIssueBody {
		ps.Add("body_invalid", "body", "A body is 1 to %d bytes.", maxIssueBody)
	}
	if len(ps) == 0 {
		images := s.issueImages(ctx, a.OrgID, b.ID, a.Livemode)
		if _, err := newsletter.Render(newsletter.Issue{Subject: in.Subject, PreviewText: in.PreviewText, Body: in.Body},
			newsletter.Options{Image: images.resolve}); err != nil {
			var re *newsletter.Error
			if errors.As(err, &re) {
				ps.Add("body_invalid", "body", "Line %d: %s.", re.Line, re.Msg)
			} else {
				return nil, err
			}
		}
		pl.media = images.used
	}
	if err := ps.Err("The issue is not valid."); err != nil {
		return nil, err
	}
	if pl.deliveries, err = s.issueDeliveries(ctx, a, b, in.Deliveries); err != nil {
		return nil, err
	}
	return pl, nil
}

// issueDeliveries resolves the accounts and audiences an issue goes to.
func (s *Service) issueDeliveries(ctx context.Context, a Actor, b *model.Brand, in []DeliveryInput) ([]model.IssueDelivery, error) {
	if in == nil {
		accts, err := s.store.MailAccounts(ctx, a.OrgID, a.Livemode, &b.ID)
		if err != nil {
			return nil, err
		}
		for _, ma := range accts {
			if ma.Status == model.ConnectionActive && len(ma.DefaultAudiences) > 0 {
				in = append(in, DeliveryInput{MailAccountID: ma.ID})
			}
		}
	}
	var out []model.IssueDelivery
	seen := map[uuid.UUID]bool{}
	for i, d := range in {
		field := fmt.Sprintf("deliveries[%d]", i)
		if seen[d.MailAccountID] {
			return nil, apperr.Invalid("delivery_duplicate", field, "Each mail account appears once; list all its audiences together.")
		}
		seen[d.MailAccountID] = true
		ma, err := s.store.MailAccount(ctx, a.OrgID, d.MailAccountID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err != nil || ma.Livemode != a.Livemode || ma.BrandID != b.ID {
			return nil, apperr.Invalid("mail_account_unknown", field+".mail_account", "The mail account is not one of brand %s's.", b.Name)
		}
		if ma.Status != model.ConnectionActive {
			return nil, apperr.Invalid("mail_account_needs_reauth", field+".mail_account", "%s needs connecting again.", ma.Name)
		}
		ids := d.Audiences
		if len(ids) == 0 {
			ids = ma.DefaultAudiences
		}
		if len(ids) == 0 {
			return nil, apperr.Invalid("audience_missing", field+".audiences", "Choose at least one audience at %s.", ma.Name)
		}
		m, creds, err := s.mailerFor(ctx, ma)
		if err != nil {
			return nil, err
		}
		audiences, err := s.checkAudiences(ctx, m, creds, ids, field+".audiences")
		if err != nil {
			s.flagRevoked(ctx, ma, err)
			return nil, err
		}
		out = append(out, model.IssueDelivery{ID: id.New(), OrgID: a.OrgID, MailAccountID: &ma.ID, Livemode: a.Livemode, Provider: ma.Provider,
			AccountName: ma.Name + " (" + ma.FromEmail + ")", Audiences: audiences, Status: model.DeliveryDraft})
	}
	return out, nil
}

// issueImages resolves an issue's images: the brand's library images, in
// the issue's mode, by their permanent email links, or https URLs.
type issueImages struct {
	s        *Service
	ctx      context.Context
	orgID    uuid.UUID
	brandID  uuid.UUID
	livemode bool
	used     []uuid.UUID
}

func (s *Service) issueImages(ctx context.Context, orgID, brandID uuid.UUID, livemode bool) *issueImages {
	return &issueImages{s: s, ctx: ctx, orgID: orgID, brandID: brandID, livemode: livemode}
}

func (im *issueImages) resolve(src string) (newsletter.Image, error) {
	if strings.HasPrefix(src, "https://") {
		return newsletter.Image{URL: src}, nil
	}
	mid, err := id.Parse(id.Media, src)
	if err != nil {
		return newsletter.Image{}, errors.New("an image is a media ID from the library or an https:// URL")
	}
	m, err := im.s.store.Media(im.ctx, im.orgID, mid)
	if err != nil || m.BrandID != im.brandID || m.Livemode != im.livemode {
		return newsletter.Image{}, fmt.Errorf("%s is not one of the brand's images", src)
	}
	link := im.s.EmailMediaLink(im.ctx, m)
	if link == "" {
		return newsletter.Image{}, errors.New("library images need the install's public URL and master keys; use an https:// URL")
	}
	if !slices.Contains(im.used, mid) {
		im.used = append(im.used, mid)
	}
	return newsletter.Image{URL: link, Alt: m.Alt, Width: m.Width, Height: m.Height}, nil
}

// CreateIssue stores a draft issue.
func (s *Service) CreateIssue(ctx context.Context, a Actor, in IssueInput) (*model.Issue, error) {
	pl, err := s.prepareIssue(ctx, a, &in)
	if err != nil {
		return nil, err
	}
	is := &model.Issue{ID: id.New(), OrgID: a.OrgID, BrandID: pl.brand.ID, Livemode: a.Livemode, Subject: in.Subject,
		PreviewText: in.PreviewText, Body: in.Body, Status: model.IssueDraft, CreatedByUser: a.UserID, CreatedByKey: a.KeyID,
		Deliveries: pl.deliveries, Media: pl.media}
	pl.setTemplate(is)
	var out *model.Issue
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateIssue(ctx, is); err != nil {
			return err
		}
		if out, err = tx.Issue(ctx, a.OrgID, is.ID); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, a, "newsletter.create", id.Format(id.Issue, is.ID), nil); err != nil {
			return err
		}
		return s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, "newsletter.created", ViewIssue(out))
	})
	return out, err
}

// UpdateIssue edits a draft.
func (s *Service) UpdateIssue(ctx context.Context, a Actor, issueID uuid.UUID, in IssueInput) (*model.Issue, error) {
	cur, err := s.Issue(ctx, a, issueID)
	if err != nil {
		return nil, err
	}
	in.BrandID = cur.BrandID
	if in.Deliveries == nil {
		// Keep the accounts and audiences it has.
		in.Deliveries = []DeliveryInput{}
		for _, d := range cur.Deliveries {
			if d.MailAccountID != nil {
				in.Deliveries = append(in.Deliveries, DeliveryInput{MailAccountID: *d.MailAccountID, Audiences: audienceIDs(d.Audiences)})
			}
		}
	}
	pl, err := s.prepareIssue(ctx, a, &in)
	if err != nil {
		return nil, err
	}
	var out *model.Issue
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		is, err := s.lockIssue(ctx, tx, a, issueID)
		if err != nil {
			return err
		}
		if is.Status != model.IssueDraft {
			return apperr.Conflict("issue_not_draft", "Only a draft can be edited; unschedule the issue first.")
		}
		asRendered := is.Subject == in.Subject && is.PreviewText == in.PreviewText && is.Body == in.Body
		is.Subject, is.PreviewText, is.Body, is.Deliveries, is.Media = in.Subject, in.PreviewText, in.Body, pl.deliveries, pl.media
		// The template stays the issue's while its text is as rendered:
		// text edited by hand is the editor's, and no template's approval
		// rule covers it (ADR 0004).
		if pl.template != nil || !asRendered {
			pl.setTemplate(is)
		}
		if err := tx.UpdateIssue(ctx, is); err != nil {
			return err
		}
		if out, err = tx.Issue(ctx, a.OrgID, is.ID); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, a, "newsletter.update", id.Format(id.Issue, is.ID), nil); err != nil {
			return err
		}
		return s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, "newsletter.updated", ViewIssue(out))
	})
	return out, err
}

// setTemplate records on is the template that rendered its text, or
// clears it.
func (pl *issuePlan) setTemplate(is *model.Issue) {
	is.TemplateID, is.TemplateVersion, is.Data = nil, nil, nil
	if pl.template != nil {
		is.TemplateID, is.TemplateVersion, is.Data = &pl.template.ID, &pl.version.Version, pl.data
	}
}

// Issue returns one of the actor's issues.
func (s *Service) Issue(ctx context.Context, a Actor, issueID uuid.UUID) (*model.Issue, error) {
	if err := a.require(PermNewslettersRead); err != nil {
		return nil, err
	}
	is, err := s.store.Issue(ctx, a.OrgID, issueID)
	if err != nil {
		return nil, notFound(err, "newsletter issue")
	}
	if is.Livemode != a.Livemode || a.brandAllowed(is.BrandID) != nil {
		return nil, apperr.NotFound("newsletter issue")
	}
	return is, nil
}

// IssueFilter narrows a listing.
type IssueFilter = store.IssueFilter

// Issues lists issues newest first.
func (s *Service) Issues(ctx context.Context, a Actor, f IssueFilter, page store.Page) ([]*model.Issue, bool, error) {
	if err := a.require(PermNewslettersRead); err != nil {
		return nil, false, err
	}
	if a.BrandID != nil {
		f.BrandID = a.BrandID
	}
	return s.store.Issues(ctx, a.OrgID, a.Livemode, f, page)
}

func (s *Service) lockIssue(ctx context.Context, tx *store.Store, a Actor, issueID uuid.UUID) (*model.Issue, error) {
	is, err := tx.LockIssue(ctx, a.OrgID, issueID)
	if err != nil {
		return nil, notFound(err, "newsletter issue")
	}
	if is.Livemode != a.Livemode || a.brandAllowed(is.BrandID) != nil {
		return nil, apperr.NotFound("newsletter issue")
	}
	return is, nil
}

// PreviewIssue renders an issue as its first delivery's provider would
// send it, with its links tagged; nothing is stored.
func (s *Service) PreviewIssue(ctx context.Context, a Actor, in IssueInput) (newsletter.Rendered, error) {
	if err := a.require(PermNewslettersRead); err != nil {
		return newsletter.Rendered{}, err
	}
	b, err := s.Brand(ctx, a, in.BrandID)
	if err != nil {
		return newsletter.Rendered{}, err
	}
	provider := string(email.Sandbox)
	if a.Livemode {
		provider = "email"
	}
	if len(in.Deliveries) > 0 {
		if ma, err := s.MailAccount(ctx, a, in.Deliveries[0].MailAccountID); err == nil {
			provider = ma.Provider
		}
	}
	if in.Template != "" {
		if _, _, err := s.fillFromTemplate(ctx, a, b, &in); err != nil {
			return newsletter.Rendered{}, err
		}
	}
	r, err := s.renderIssue(ctx, b, a.Livemode, uuid.Nil, newsletter.Issue{Subject: strings.TrimSpace(in.Subject),
		PreviewText: strings.TrimSpace(in.PreviewText), Body: in.Body}, provider, "#unsubscribe")
	var re *newsletter.Error
	if errors.As(err, &re) {
		return newsletter.Rendered{}, apperr.Invalid("body_invalid", "body", "Line %d: %s.", re.Line, re.Msg)
	}
	return r, err
}

// IssuePreview renders a stored issue as its first delivery would be sent.
func (s *Service) IssuePreview(ctx context.Context, a Actor, issueID uuid.UUID) (newsletter.Rendered, error) {
	is, err := s.Issue(ctx, a, issueID)
	if err != nil {
		return newsletter.Rendered{}, err
	}
	b, err := s.store.Brand(ctx, a.OrgID, is.BrandID)
	if err != nil {
		return newsletter.Rendered{}, err
	}
	provider := string(email.Sandbox)
	if len(is.Deliveries) > 0 {
		provider = is.Deliveries[0].Provider
	}
	return s.renderIssue(ctx, b, is.Livemode, is.ID, newsletter.Issue{Subject: is.Subject, PreviewText: is.PreviewText, Body: is.Body},
		provider, "#unsubscribe")
}

// renderIssue renders an issue for one provider: the brand's theme, its
// images, links to the brand's sites tagged utm_medium=email with the
// provider as source, the issue as campaign and each link's place as
// content (ADR 0024 decision 10), and the provider's unsubscribe link.
func (s *Service) renderIssue(ctx context.Context, b *model.Brand, livemode bool, issueID uuid.UUID, is newsletter.Issue,
	provider, unsubscribe string) (newsletter.Rendered, error) {
	theme := newsletter.Theme{BrandName: b.Name, Accent: b.EmailTheme.Accent, PostalAddress: b.EmailTheme.PostalAddress,
		Footer: b.EmailTheme.Footer}
	if b.EmailTheme.LogoMediaID != nil {
		if m, err := s.store.Media(ctx, b.OrgID, *b.EmailTheme.LogoMediaID); err == nil {
			theme.LogoURL = s.EmailMediaLink(ctx, m)
		}
	}
	campaign := "preview"
	if issueID != uuid.Nil {
		campaign = id.Format(id.Issue, issueID)
	}
	images := s.issueImages(ctx, b.OrgID, b.ID, livemode)
	return newsletter.Render(is, newsletter.Options{Theme: theme, Image: images.resolve, Unsubscribe: unsubscribe,
		Link: func(u string, n int) string {
			return utm.TagURL(u, b.UTMDomains, utm.Params{Source: provider, Medium: utm.Email, Campaign: campaign, Content: fmt.Sprintf("link-%d", n)})
		}})
}

// ScheduleIssue sets an issue to go out at sendAt. A draft is checked and
// scheduled, or sent for approval under the brand's policy; a scheduled
// issue is moved, at the providers too once it is handed off (ADR 0024
// decisions 12 and 13).
func (s *Service) ScheduleIssue(ctx context.Context, a Actor, issueID uuid.UUID, sendAt time.Time) (*model.Issue, error) {
	if err := a.require(PermNewslettersWrite); err != nil {
		return nil, err
	}
	now := s.Now()
	switch {
	case sendAt.Before(now.Add(MinSendDelay - time.Second)):
		return nil, apperr.Invalid("send_at_invalid", "send_at", "Schedule an issue at least %d minutes ahead.", int(MinSendDelay.Minutes()))
	case sendAt.After(now.Add(maxScheduleAhead)):
		return nil, apperr.Invalid("send_at_invalid", "send_at", "Issues can be scheduled at most a year ahead.")
	}
	sendAt = sendAt.UTC().Truncate(time.Second)
	cur, err := s.Issue(ctx, a, issueID)
	if err != nil {
		return nil, err
	}
	if cur.Status == model.IssuePendingApproval || cur.Status == model.IssueScheduled || cur.Status == model.IssueSending {
		return s.moveIssue(ctx, a, cur, sendAt)
	}
	if cur.Status != model.IssueDraft {
		return nil, apperr.Conflict("issue_not_schedulable", "A %s issue cannot be scheduled.", strings.ReplaceAll(string(cur.Status), "_", " "))
	}
	b, err := s.store.Brand(ctx, a.OrgID, cur.BrandID)
	if err != nil {
		return nil, err
	}
	var ps apperr.Problems
	if b.EmailTheme.PostalAddress == "" {
		ps.Add("postal_address_missing", "brand", "Brand %s has no postal address in its email theme; anti-spam laws require one.", b.Name)
	}
	if len(cur.Deliveries) == 0 {
		ps.Add("deliveries_missing", "deliveries", "Choose at least one mail account and audience.")
	}
	for i, d := range cur.Deliveries {
		if d.MailAccountID == nil {
			ps.Add("mail_account_unknown", fmt.Sprintf("deliveries[%d]", i), "%s was disconnected; edit the issue.", d.AccountName)
			continue
		}
		ma, err := s.store.MailAccount(ctx, a.OrgID, *d.MailAccountID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err != nil || ma.Status != model.ConnectionActive {
			ps.Add("mail_account_needs_reauth", fmt.Sprintf("deliveries[%d]", i), "%s needs connecting again.", d.AccountName)
		}
	}
	if _, err := s.renderIssue(ctx, b, cur.Livemode, cur.ID, newsletter.Issue{Subject: cur.Subject, PreviewText: cur.PreviewText, Body: cur.Body},
		"check", "#"); err != nil {
		ps.Add("body_invalid", "body", "%s.", err.Error())
	}
	if err := ps.Err("The issue cannot be scheduled yet."); err != nil {
		return nil, err
	}
	// An issue still as its template rendered it follows the template's
	// approval rule, as a post does (ADR 0004).
	var t *model.Template
	if cur.TemplateID != nil {
		if t, err = s.store.Template(ctx, a.OrgID, *cur.TemplateID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	needs := approvalNeeded(b.ApprovalPolicy, t, a)
	var out *model.Issue
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		is, err := s.lockIssue(ctx, tx, a, issueID)
		if err != nil {
			return err
		}
		if is.Status != model.IssueDraft {
			return apperr.Conflict("issue_not_draft", "The issue changed meanwhile; reload it.")
		}
		status, deliveries, event := model.IssueScheduled, model.DeliveryQueued, "newsletter.scheduled"
		var handoff *time.Time
		if needs {
			status, deliveries, event = model.IssuePendingApproval, model.DeliveryHeld, "newsletter.approval_requested"
		} else {
			handoff = ptr(sendAt.Add(-HandoffAhead))
		}
		if err := tx.SetIssueSchedule(ctx, a.OrgID, is.ID, status, &sendAt, needs); err != nil {
			return err
		}
		if _, err := tx.SetDeliveries(ctx, a.OrgID, is.ID, []model.DeliveryStatus{model.DeliveryDraft}, deliveries, handoff); err != nil {
			return err
		}
		if out, err = tx.Issue(ctx, a.OrgID, is.ID); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, a, "newsletter.schedule", id.Format(id.Issue, is.ID), map[string]any{"send_at": sendAt}); err != nil {
			return err
		}
		if needs {
			if err := s.notifyApproval(ctx, tx, a, NotifyNewsletterApproval, "A newsletter waits for your approval: "+out.Subject,
				"/newsletters/"+id.Format(id.Issue, out.ID)); err != nil {
				return err
			}
		}
		return s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, event, ViewIssue(out))
	})
	return out, err
}

// moveIssue changes a scheduled issue's send time: queued deliveries are
// handed off on the new schedule, and handed-off ones are moved at their
// providers first. If a provider refuses, the ones already moved are moved
// back and nothing changes; one that cannot be moved back needs attention,
// since its campaign no longer sends when the issue says.
func (s *Service) moveIssue(ctx context.Context, a Actor, cur *model.Issue, sendAt time.Time) (*model.Issue, error) {
	if busy, err := s.store.HandoffInFlight(ctx, a.OrgID, cur.ID, s.Now()); err != nil || busy {
		if err != nil {
			return nil, err
		}
		return nil, errHandoffInFlight
	}
	var moved []model.IssueDelivery
	for _, d := range cur.Deliveries {
		if d.Status != model.DeliveryHandedOff {
			continue
		}
		if err := s.atProvider(ctx, a.OrgID, d, func(ctx context.Context, m email.Mailer, c platform.Credentials) error {
			return m.Reschedule(ctx, c, d.CampaignID, sendAt)
		}); err != nil {
			s.moveBack(ctx, a, cur, moved, sendAt)
			return nil, err
		}
		moved = append(moved, d)
	}
	var out *model.Issue
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		is, err := s.lockIssue(ctx, tx, a, cur.ID)
		if err != nil {
			return err
		}
		if _, err := tx.SetDeliveries(ctx, a.OrgID, is.ID, []model.DeliveryStatus{model.DeliveryQueued}, model.DeliveryQueued,
			ptr(sendAt.Add(-HandoffAhead))); err != nil {
			return err
		}
		if err := tx.SetIssueSchedule(ctx, a.OrgID, is.ID, is.Status, &sendAt, is.ApprovalNeeded); err != nil {
			return err
		}
		if out, err = tx.Issue(ctx, a.OrgID, is.ID); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, a, "newsletter.move", id.Format(id.Issue, is.ID), map[string]any{"send_at": sendAt}); err != nil {
			return err
		}
		return s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, "newsletter.rescheduled", ViewIssue(out))
	})
	return out, err
}

// moveBack returns moved deliveries' campaigns to the issue's send time
// after a move failed part way. One that will not go back is flagged, and
// logged: its campaign sends at sendAt, not when the issue says.
func (s *Service) moveBack(ctx context.Context, a Actor, cur *model.Issue, moved []model.IssueDelivery, sendAt time.Time) {
	var stuck []model.IssueDelivery
	for _, back := range moved {
		err := s.atProvider(ctx, a.OrgID, back, func(ctx context.Context, m email.Mailer, c platform.Credentials) error {
			return m.Reschedule(ctx, c, back.CampaignID, *cur.SendAt)
		})
		if err != nil {
			s.log.ErrorContext(ctx, "moving a newsletter's campaign back failed; it needs attention", "issue", id.Format(id.Issue, cur.ID),
				"delivery", id.Format(id.IssueDelivery, back.ID), "err", err)
			stuck = append(stuck, back)
		}
	}
	if len(stuck) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx) // the flags must land even if the caller left
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		is, err := s.lockIssue(ctx, tx, a, cur.ID)
		if err != nil {
			return err
		}
		for _, d := range stuck {
			why := fmt.Sprintf("Moving the issue failed part way: this campaign is set to send at %s and could not be moved back to %s; check it at %s.",
				sendAt.UTC().Format(time.RFC3339), cur.SendAt.UTC().Format(time.RFC3339), d.AccountName)
			if err := tx.FlagDelivery(ctx, a.OrgID, d.ID, why); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		return s.refreshIssue(ctx, tx, is, a.RequestID, "")
	})
	if err != nil {
		s.log.ErrorContext(ctx, "flagging a newsletter's stuck campaigns failed", "issue", id.Format(id.Issue, cur.ID), "err", err)
	}
}

var errHandoffInFlight = apperr.Conflict("handoff_in_progress", "The issue is being handed to its providers right now; try again in a minute.")

// atProvider runs fn with a delivery's account's provider. A refused key
// flags the account.
func (s *Service) atProvider(ctx context.Context, orgID uuid.UUID, d model.IssueDelivery,
	fn func(context.Context, email.Mailer, platform.Credentials) error) error {
	if d.MailAccountID == nil {
		return apperr.Conflict("mail_account_unknown", "%s was disconnected.", d.AccountName)
	}
	ma, err := s.store.MailAccount(ctx, orgID, *d.MailAccountID)
	if err != nil {
		return notFound(err, "mail account")
	}
	m, creds, err := s.mailerFor(ctx, ma)
	if err != nil {
		return err
	}
	if _, err := callProvider(ctx, func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx, m, creds) }); err != nil {
		s.flagRevoked(ctx, ma, err)
		return connectError(platform.Provider(m.Name()), err)
	}
	return nil
}

// UnscheduleIssue returns a scheduled issue to draft, to be edited. Its
// campaigns are removed from the providers.
func (s *Service) UnscheduleIssue(ctx context.Context, a Actor, issueID uuid.UUID) (*model.Issue, error) {
	return s.stopIssue(ctx, a, issueID, false)
}

// CancelIssue stops an issue for good; deliveries already sent stay sent.
func (s *Service) CancelIssue(ctx context.Context, a Actor, issueID uuid.UUID) (*model.Issue, error) {
	return s.stopIssue(ctx, a, issueID, true)
}

func (s *Service) stopIssue(ctx context.Context, a Actor, issueID uuid.UUID, cancel bool) (*model.Issue, error) {
	if err := a.require(PermNewslettersWrite); err != nil {
		return nil, err
	}
	cur, err := s.Issue(ctx, a, issueID)
	if err != nil {
		return nil, err
	}
	switch cur.Status {
	case model.IssuePendingApproval, model.IssueScheduled, model.IssueSending:
	case model.IssueDraft:
		if !cancel {
			return nil, apperr.Conflict("issue_not_scheduled", "The issue is not scheduled.")
		}
	default:
		return nil, apperr.Conflict("issue_not_cancelable", "A %s issue cannot be stopped.", strings.ReplaceAll(string(cur.Status), "_", " "))
	}
	if !cancel && slices.ContainsFunc(cur.Deliveries, func(d model.IssueDelivery) bool { return d.Status == model.DeliverySent }) {
		return nil, apperr.Conflict("issue_partly_sent", "Some copies have been sent; cancel the rest instead.")
	}
	if busy, err := s.store.HandoffInFlight(ctx, a.OrgID, cur.ID, s.Now()); err != nil || busy {
		if err != nil {
			return nil, err
		}
		return nil, errHandoffInFlight
	}
	for _, d := range cur.Deliveries {
		if d.Status != model.DeliveryHandedOff {
			continue
		}
		if err := s.atProvider(ctx, a.OrgID, d, func(ctx context.Context, m email.Mailer, c platform.Credentials) error {
			return m.Cancel(ctx, c, d.CampaignID)
		}); err != nil {
			return nil, err
		}
		// Recorded at once, in case a later one fails: this campaign is gone,
		// so the delivery is canceled, or queued to be handed off again.
		to, handoff := model.DeliveryCanceled, (*time.Time)(nil)
		if !cancel && cur.SendAt != nil {
			to, handoff = model.DeliveryQueued, ptr(cur.SendAt.Add(-HandoffAhead))
		}
		if err := s.store.StopCampaign(context.WithoutCancel(ctx), a.OrgID, d.ID, to, handoff); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	var out *model.Issue
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		is, err := s.lockIssue(ctx, tx, a, issueID)
		if err != nil {
			return err
		}
		open := []model.DeliveryStatus{model.DeliveryDraft, model.DeliveryHeld, model.DeliveryQueued, model.DeliveryHandedOff}
		action, event := "newsletter.cancel", "newsletter.canceled"
		if cancel {
			if _, err := tx.SetDeliveries(ctx, a.OrgID, is.ID, open, model.DeliveryCanceled, nil); err != nil {
				return err
			}
			if err := s.refreshIssue(ctx, tx, is, a.RequestID, model.IssueCanceled); err != nil {
				return err
			}
		} else {
			action, event = "newsletter.unschedule", "newsletter.unscheduled"
			if err := tx.ResetDeliveries(ctx, a.OrgID, is.ID); err != nil {
				return err
			}
			if err := tx.SetIssueSchedule(ctx, a.OrgID, is.ID, model.IssueDraft, is.SendAt, false); err != nil {
				return err
			}
		}
		if out, err = tx.Issue(ctx, a.OrgID, is.ID); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, a, action, id.Format(id.Issue, is.ID), nil); err != nil {
			return err
		}
		return s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, event, ViewIssue(out))
	})
	return out, err
}

// ReviewIssue approves or rejects an issue waiting for approval. Approving
// queues it, sending soon if its time has passed; rejecting returns it to
// draft with the note.
func (s *Service) ReviewIssue(ctx context.Context, a Actor, issueID uuid.UUID, approve bool, note string) (*model.Issue, error) {
	if err := a.require(PermPostsApprove); err != nil {
		return nil, err
	}
	var out *model.Issue
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		is, err := s.lockIssue(ctx, tx, a, issueID)
		if err != nil {
			return err
		}
		if is.Status != model.IssuePendingApproval {
			return apperr.Conflict("issue_not_pending", "This issue is not waiting for approval.")
		}
		if a.IsKey() && is.CreatedByKey != nil && *is.CreatedByKey == *a.KeyID {
			return apperr.Forbidden("A key cannot review an issue it created (ADR 0019).")
		}
		now := s.Now()
		status, action := model.IssueScheduled, "newsletter.approved"
		if approve {
			sendAt := *is.SendAt
			if earliest := now.Add(MinSendDelay); sendAt.Before(earliest) {
				sendAt = earliest.UTC().Truncate(time.Second)
			}
			if err := tx.SetIssueSchedule(ctx, a.OrgID, is.ID, model.IssuePendingApproval, &sendAt, true); err != nil {
				return err
			}
			if _, err := tx.SetDeliveries(ctx, a.OrgID, is.ID, []model.DeliveryStatus{model.DeliveryHeld}, model.DeliveryQueued,
				ptr(sendAt.Add(-HandoffAhead))); err != nil {
				return err
			}
		} else {
			status, action = model.IssueDraft, "newsletter.rejected"
			if _, err := tx.SetDeliveries(ctx, a.OrgID, is.ID, []model.DeliveryStatus{model.DeliveryHeld}, model.DeliveryDraft, nil); err != nil {
				return err
			}
		}
		if err := tx.ReviewIssue(ctx, a.OrgID, is.ID, a.UserID, a.KeyID, status, strings.TrimSpace(note), now); err != nil {
			return err
		}
		if out, err = tx.Issue(ctx, a.OrgID, is.ID); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, a, action, id.Format(id.Issue, is.ID), nil); err != nil {
			return err
		}
		return s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, action, ViewIssue(out))
	})
	return out, err
}

// TestIssueInput sends an issue to a few addresses through one of its
// brand's mail accounts. The addresses are never stored.
type TestIssueInput struct {
	MailAccountID uuid.UUID
	To            []string
}

// SendTestIssue sends an issue, as that account's provider would send it,
// to up to MaxTestRecipients addresses, its subject marked as a test.
func (s *Service) SendTestIssue(ctx context.Context, a Actor, issueID uuid.UUID, in TestIssueInput) error {
	if err := a.require(PermNewslettersWrite); err != nil {
		return err
	}
	is, err := s.Issue(ctx, a, issueID)
	if err != nil {
		return err
	}
	var to []string
	for _, addr := range in.To {
		if addr = strings.TrimSpace(addr); addr != "" && !slices.Contains(to, addr) {
			to = append(to, addr)
		}
	}
	var ps apperr.Problems
	if len(to) == 0 || len(to) > MaxTestRecipients {
		ps.Add("to_invalid", "to", "Send a test to 1 to %d addresses.", MaxTestRecipients)
	}
	for _, addr := range to {
		if !plainAddress(addr) {
			ps.Add("to_invalid", "to", "%q is not an email address.", addr)
		}
	}
	if err := ps.Err("The test cannot be sent."); err != nil {
		return err
	}
	ma, err := s.MailAccount(ctx, a, in.MailAccountID)
	if err != nil && apperr.As(err).Kind != apperr.KindNotFound {
		return err
	}
	if err != nil || ma.BrandID != is.BrandID {
		return apperr.Invalid("mail_account_unknown", "mail_account", "The mail account is not one of the issue's brand's.")
	}
	b, err := s.store.Brand(ctx, a.OrgID, is.BrandID)
	if err != nil {
		return err
	}
	m, creds, err := s.mailerFor(ctx, ma)
	if err != nil {
		return err
	}
	r, err := s.renderIssue(ctx, b, is.Livemode, is.ID, newsletter.Issue{Subject: is.Subject, PreviewText: is.PreviewText, Body: is.Body},
		ma.Provider, "#unsubscribe-in-test")
	if err != nil {
		return apperr.Invalid("body_invalid", "body", "%s.", err.Error())
	}
	msg := email.Message{Subject: "[Test] " + is.Subject, PreviewText: is.PreviewText, HTML: r.HTML, Text: r.Text,
		From: email.Address{Name: ma.FromName, Email: ma.FromEmail}, ReplyTo: ma.ReplyTo}
	if _, err := callProvider(ctx, func(ctx context.Context) (struct{}, error) { return struct{}{}, m.SendTest(ctx, creds, msg, to) }); err != nil {
		s.flagRevoked(ctx, ma, err)
		return connectError(platform.Provider(m.Name()), err)
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		return s.audit(ctx, tx, a, "newsletter.test_send", id.Format(id.Issue, is.ID), map[string]any{"recipients": len(to),
			"mail_account": id.Format(id.MailAccount, ma.ID)})
	})
}

// deriveIssueStatus is an issue's status from its deliveries, once it is
// past draft and approval (ADR 0024 decision 6).
func deriveIssueStatus(is *model.Issue, now time.Time) model.IssueStatus {
	if is.Status == model.IssueDraft || is.Status == model.IssuePendingApproval || len(is.Deliveries) == 0 {
		return is.Status
	}
	var open, handedOff, sent, canceled int
	for _, d := range is.Deliveries {
		switch {
		case d.Status == model.DeliveryHandedOff:
			open++
			handedOff++
		case d.Status.Open():
			open++
		case d.Status == model.DeliverySent:
			sent++
		case d.Status == model.DeliveryCanceled:
			canceled++
		}
	}
	total := len(is.Deliveries)
	switch {
	case canceled == total:
		return model.IssueCanceled
	case open > 0 && (sent > 0 || handedOff > 0 && is.SendAt != nil && !now.Before(*is.SendAt)):
		return model.IssueSending
	case open > 0:
		return model.IssueScheduled
	case sent > 0 && sent+canceled == total:
		return model.IssueSent
	case sent > 0:
		return model.IssuePartiallySent
	}
	return model.IssueFailed
}

// refreshIssue re-derives a locked issue's status from its deliveries (or
// sets force), and announces a send finishing.
func (s *Service) refreshIssue(ctx context.Context, tx *store.Store, is *model.Issue, requestID string, force model.IssueStatus) error {
	fresh, err := tx.LockIssue(ctx, is.OrgID, is.ID)
	if err != nil {
		return err
	}
	status := force
	if status == "" {
		status = deriveIssueStatus(fresh, s.Now())
	}
	if status == fresh.Status {
		return nil
	}
	if err := tx.SetIssueStatus(ctx, is.OrgID, is.ID, status); err != nil {
		return err
	}
	fresh.Status = status
	switch status {
	case model.IssueSent, model.IssuePartiallySent:
		return s.emit(ctx, tx, is.OrgID, is.Livemode, requestID, "newsletter.sent", ViewIssue(fresh))
	case model.IssueFailed:
		return s.emit(ctx, tx, is.OrgID, is.Livemode, requestID, "newsletter.failed", ViewIssue(fresh))
	}
	return nil
}

// HandOffNewsletters hands due deliveries to their providers.
func (s *Service) HandOffNewsletters(ctx context.Context) (int, error) {
	return s.handOffNewsletters(ctx, nil)
}

func (s *Service) handOffNewsletters(ctx context.Context, orgID *uuid.UUID) (int, error) {
	owner := id.New().String()
	n := 0
	// One at a time, so each lease starts when its work does; a batch
	// claimed at once would run out behind slow providers.
	for range handoffBatch {
		now := s.Now()
		due, err := s.store.ClaimDueHandoffs(ctx, orgID, owner, s.mailProviders(), now, handoffLease, 1)
		if err != nil || len(due) == 0 {
			return n, err
		}
		d := due[0]
		stop := s.keepHandoffLease(ctx, d, owner)
		err = s.handOff(ctx, d, owner, now)
		stop()
		if err != nil {
			if errors.Is(err, store.ErrNotFound) { // its lease ran out and another worker has it
				s.log.WarnContext(ctx, "a newsletter hand-off outlived its lease", "delivery", id.Format(id.IssueDelivery, d.ID))
				continue
			}
			return n, err
		}
		n++
	}
	return n, nil
}

// keepHandoffLease renews a delivery's lease while its worker hands it
// off, so a slow provider never lets a second worker in beside it.
func (s *Service) keepHandoffLease(ctx context.Context, d *model.IssueDelivery, owner string) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(handoffLease / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.store.ExtendHandoffLease(context.WithoutCancel(ctx), d.OrgID, d.ID, owner, s.Now().Add(handoffLease)); err != nil {
					s.log.WarnContext(ctx, "renewing a newsletter hand-off lease failed", "delivery", id.Format(id.IssueDelivery, d.ID), "err", err)
				}
			}
		}
	}()
	return func() { close(done) }
}

func (s *Service) mailProviders() []string {
	var out []string
	for _, p := range s.mailers.Providers() {
		out = append(out, string(p))
	}
	return out
}

// handOff creates one delivery's campaign at its provider, scheduled for
// the issue's send time. Once is all (ADR 0024 decision 14): a retry
// first looks for the campaign by its tag, and a create whose result was
// unknown, with no campaign to be found, waits for a person.
func (s *Service) handOff(ctx context.Context, d *model.IssueDelivery, owner string, now time.Time) error {
	end := func(status model.DeliveryStatus, why string) error {
		return s.store.InTx(ctx, func(tx *store.Store) error {
			if err := tx.EndDelivery(ctx, d.OrgID, d.ID, owner, status, truncate(why, 500)); err != nil {
				return err
			}
			return s.refreshIssue(ctx, tx, &model.Issue{ID: d.IssueID, OrgID: d.OrgID, Livemode: d.Livemode}, "", "")
		})
	}
	is, err := s.store.Issue(ctx, d.OrgID, d.IssueID)
	if err != nil {
		return err
	}
	if d.MailAccountID == nil {
		return end(model.DeliveryFailed, "The mail account was disconnected.")
	}
	ma, err := s.store.MailAccount(ctx, d.OrgID, *d.MailAccountID)
	if errors.Is(err, store.ErrNotFound) {
		return end(model.DeliveryFailed, "The mail account was disconnected.")
	}
	if err != nil { // the database, for now: try again
		return s.retryHandoff(ctx, d, is, owner, now, err)
	}
	if ma.Status != model.ConnectionActive {
		return end(model.DeliveryFailed, "The mail account needs connecting again: "+ma.StatusNote)
	}
	m, creds, err := s.mailerFor(ctx, ma)
	if err != nil {
		if apperr.As(err).Kind == apperr.KindUnavailable { // the master keys, for now: try again
			return s.retryHandoff(ctx, d, is, owner, now, err)
		}
		return end(model.DeliveryFailed, err.Error())
	}
	b, err := s.store.Brand(ctx, d.OrgID, is.BrandID)
	if err != nil {
		return err
	}
	tag := id.Format(id.IssueDelivery, d.ID)
	campaignID := ""
	if d.Attempts > 1 {
		if campaignID, err = m.Find(ctx, creds, tag); err != nil {
			return s.retryHandoff(ctx, d, is, owner, now, err)
		}
		if campaignID == "" && strings.HasPrefix(d.LastError, uncertainPrefix) {
			return end(model.DeliveryNeedsAttention, "Whether "+m.Name()+" has the campaign is unknown, and none tagged "+tag+
				" was found. Check "+m.Name()+": send it there, or cancel the issue and schedule it again.")
		}
	}
	if campaignID == "" {
		r, err := s.renderIssue(ctx, b, is.Livemode, is.ID, newsletter.Issue{Subject: is.Subject, PreviewText: is.PreviewText, Body: is.Body},
			ma.Provider, m.Unsubscribe())
		if err != nil {
			return end(model.DeliveryFailed, "The issue no longer renders: "+err.Error())
		}
		at := *is.SendAt
		if earliest := now.Add(MinSendDelay); at.Before(earliest) {
			at = earliest
		}
		msg := email.Message{Name: is.Subject + " · " + id.Format(id.Issue, is.ID), Subject: is.Subject, PreviewText: is.PreviewText,
			HTML: r.HTML, Text: r.Text, From: email.Address{Name: ma.FromName, Email: ma.FromEmail}, ReplyTo: ma.ReplyTo, Tag: tag}
		if campaignID, err = m.Schedule(ctx, creds, msg, audienceIDs(d.Audiences), at); err != nil {
			return s.retryHandoff(ctx, d, is, owner, now, err)
		}
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeliveryHandedOff(ctx, d.OrgID, d.ID, owner, campaignID, is.SendAt.Add(firstLookAfter), *is.SendAt); err != nil {
			return err
		}
		return s.refreshIssue(ctx, tx, is, "", "")
	})
	if errors.Is(err, store.ErrNotFound) {
		// Canceled, or moved, while it was being handed off: take the
		// campaign back; a moved issue's delivery is handed off again on
		// its new schedule. Unless another worker took the hand-off over
		// and found this campaign, or is finding it now: it is theirs.
		taken, terr := s.store.HandoffTaken(context.WithoutCancel(ctx), d.OrgID, d.ID, owner, campaignID)
		if terr != nil || taken {
			s.log.WarnContext(ctx, "a newsletter hand-off lost its lease; leaving the campaign to the worker that has it", "delivery", tag, "err", terr)
			return nil
		}
		if cerr := m.Cancel(ctx, creds, campaignID); cerr != nil {
			s.log.WarnContext(ctx, "removing a campaign canceled during hand-off failed", "delivery", tag, "err", cerr)
		}
		return nil
	}
	return err
}

// uncertainPrefix marks a hand-off whose result was unknown.
const uncertainPrefix = "unknown: "

// retryHandoff records a failed hand-off: a refused key or request ends
// the delivery; anything else is tried again, backing off, until well
// past the send time.
func (s *Service) retryHandoff(ctx context.Context, d *model.IssueDelivery, is *model.Issue, owner string, now time.Time, err error) error {
	kind := platform.KindOf(err)
	end := func(status model.DeliveryStatus, why string) error {
		return s.store.InTx(ctx, func(tx *store.Store) error {
			if err := tx.EndDelivery(ctx, d.OrgID, d.ID, owner, status, truncate(why, 500)); err != nil {
				return err
			}
			return s.refreshIssue(ctx, tx, is, "", "")
		})
	}
	switch kind {
	case platform.AuthRevoked:
		if ma, merr := s.store.MailAccount(ctx, d.OrgID, *d.MailAccountID); merr == nil {
			s.flagRevoked(ctx, ma, err)
		}
		return end(model.DeliveryFailed, "The provider refused the key: connect the account again. "+err.Error())
	case platform.Rejected:
		return end(model.DeliveryFailed, "The provider refused the campaign: "+err.Error())
	}
	if now.After(is.SendAt.Add(handoffGiveUp)) {
		return end(model.DeliveryFailed, "Could not hand the issue off in time: "+err.Error())
	}
	why := err.Error()
	if kind == platform.Uncertain {
		why = uncertainPrefix + why
	}
	backoff := min(time.Duration(1<<min(d.Attempts, 5))*time.Minute, 30*time.Minute)
	if pe := (*platform.Error)(nil); errors.As(err, &pe) && pe.RetryAfter > backoff {
		backoff = pe.RetryAfter
	}
	return s.store.RetryHandoff(ctx, d.OrgID, d.ID, owner, truncate(why, 500), now.Add(backoff))
}

// ReadNewsletterResults checks handed-off deliveries and reads sent ones'
// results on their schedule.
func (s *Service) ReadNewsletterResults(ctx context.Context) (int, error) {
	return s.readNewsletterResults(ctx, nil)
}

func (s *Service) readNewsletterResults(ctx context.Context, orgID *uuid.UUID) (int, error) {
	now := s.Now()
	due, err := s.store.ClaimDueDeliveryReads(ctx, orgID, s.mailProviders(), now, readLease, 50)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range due {
		if err := s.readDelivery(ctx, d, now); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// readDelivery asks the provider how a delivery stands. A failure is
// retried later, never returned.
func (s *Service) readDelivery(ctx context.Context, d *model.IssueDelivery, now time.Time) error {
	is, err := s.store.Issue(ctx, d.OrgID, d.IssueID)
	if err != nil {
		return err
	}
	save := func() error {
		return s.store.InTx(ctx, func(tx *store.Store) error {
			if err := tx.SaveDeliveryRead(ctx, d, now); err != nil {
				return err
			}
			return s.refreshIssue(ctx, tx, is, "", "")
		})
	}
	var cp email.Campaign
	if d.MailAccountID == nil {
		err = errors.New("the mail account was disconnected")
	} else {
		err = s.atProvider(ctx, d.OrgID, *d, func(ctx context.Context, m email.Mailer, c platform.Credentials) error {
			var rerr error
			cp, rerr = m.Campaign(ctx, c, d.CampaignID)
			return rerr
		})
	}
	if err != nil {
		d.LastError, d.NextReadAt = truncate("Reading failed; retrying: "+err.Error(), 500), ptr(now.Add(lookAgain))
		if d.Status == model.DeliveryHandedOff && now.After(is.SendAt.Add(notSentGiveUp)) {
			d.Status, d.NextReadAt = model.DeliveryNeedsAttention, nil
			d.LastError = truncate("Could not confirm the send: "+err.Error(), 500)
		}
		return save()
	}
	d.LastError = ""
	switch cp.Status {
	case email.CampaignSent:
		if d.Status != model.DeliverySent {
			d.SentAt = cp.SentAt
			if d.SentAt == nil {
				d.SentAt = is.SendAt
			}
		}
		d.Status = model.DeliverySent
		d.Results = model.MailResults(cp.Results)
		d.NextReadAt = nil
		for _, after := range ResultReads {
			if at := d.SentAt.Add(after); at.After(now) {
				d.NextReadAt = &at
				break
			}
		}
	case email.CampaignStopped:
		d.Status, d.NextReadAt = model.DeliveryCanceled, nil
		d.LastError = "Stopped in the provider's own tools."
	default: // scheduled, sending
		d.NextReadAt = ptr(now.Add(lookAgain))
		if now.After(is.SendAt.Add(notSentGiveUp)) {
			d.Status, d.NextReadAt = model.DeliveryNeedsAttention, nil
			d.LastError = "The provider has not sent it a day after its send time; check it there."
		}
	}
	return save()
}
