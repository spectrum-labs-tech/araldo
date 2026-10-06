// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/smtpmail"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// The monthly report by email (ADR 0026 phase 3): early each month, each
// brand's report for the month before goes to the addresses its admins
// chose, members or not, as a share link and a short summary. Live mode
// only.

const (
	// maxReportRecipients caps a brand's list.
	maxReportRecipients = 20
	// reportMailDays is how far into a month its predecessor's report is
	// still sent: a list made later waits for the next month.
	reportMailDays = 7
	// reportEmailBatch is how many report emails one run claims.
	reportEmailBatch = 50
)

// ReportRecipients lists who gets a brand's monthly report. Admins and
// owners, who share reports, manage it.
func (s *Service) ReportRecipients(ctx context.Context, a Actor, brandID uuid.UUID) ([]model.ReportRecipient, error) {
	if err := a.require(PermMembersWrite); err != nil {
		return nil, err
	}
	if _, err := s.Brand(ctx, a, brandID); err != nil {
		return nil, err
	}
	return s.store.ReportRecipients(ctx, a.OrgID, brandID)
}

// AddReportRecipient adds an address to a brand's monthly report.
func (s *Service) AddReportRecipient(ctx context.Context, a Actor, brandID uuid.UUID, email string) error {
	if err := a.require(PermMembersWrite); err != nil {
		return err
	}
	b, err := s.Brand(ctx, a, brandID)
	if err != nil {
		return err
	}
	norm, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	have, err := s.store.ReportRecipients(ctx, a.OrgID, b.ID)
	if err != nil {
		return err
	}
	if len(have) >= maxReportRecipients {
		return apperr.Invalid("recipients_limit", "email", "A brand's report goes to at most %d addresses.", maxReportRecipients)
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.AddReportRecipient(ctx, a.OrgID, b.ID, norm, a.UserID); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Conflict("recipient_exists", "%s already gets this report.", norm)
			}
			return err
		}
		return s.audit(ctx, tx, a, "report.recipient_add", id.Format(id.Brand, b.ID), map[string]any{"email": norm})
	})
}

// RemoveReportRecipient takes an address off a brand's monthly report.
func (s *Service) RemoveReportRecipient(ctx context.Context, a Actor, brandID uuid.UUID, email string) error {
	if err := a.require(PermMembersWrite); err != nil {
		return err
	}
	norm := strings.ToLower(strings.TrimSpace(email))
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.RemoveReportRecipient(ctx, a.OrgID, brandID, norm); err != nil {
			return notFound(err, "recipient")
		}
		return s.audit(ctx, tx, a, "report.recipient_remove", id.Format(id.Brand, brandID), map[string]any{"email": norm})
	})
}

// reportMonthDue is the month whose report a brand sends now, in its time
// zone: the one before, during the first days of a month.
func reportMonthDue(now time.Time, tz string) (string, bool) {
	local := now.In(location(tz))
	if local.Day() > reportMailDays {
		return "", false
	}
	first := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, local.Location())
	return first.AddDate(0, -1, 0).Format("2006-01"), true
}

// reportActor acts for the mailing in an org: an owner's view of its live
// mode, recorded as the reports.mail task.
func reportActor(orgID uuid.UUID) Actor {
	return Actor{OrgID: orgID, Livemode: true, Role: model.RoleOwner, RequestID: "reports.mail", OrgStatus: model.OrgActive}
}

func shareTokenAAD(shareID uuid.UUID) string {
	return keyring.AAD("report_mailings", "share_token", shareID)
}

// MailReports plans this month's mailings and sends the report emails due.
func (s *Service) MailReports(ctx context.Context) (int, error) {
	return s.mailReports(ctx, nil)
}

// mailReports is MailReports for one org, or (orgID nil) every org.
func (s *Service) mailReports(ctx context.Context, orgID *uuid.UUID) (int, error) {
	if err := s.planReportMailings(ctx, orgID); err != nil {
		return 0, err
	}
	return s.sendReportEmails(ctx, orgID)
}

// planReportMailings makes, once per brand and month, the share link and
// the emails to send.
func (s *Service) planReportMailings(ctx context.Context, orgID *uuid.UUID) error {
	now := s.Now()
	brands, err := s.store.ReportBrands(ctx, orgID)
	if err != nil {
		return err
	}
	for _, b := range brands {
		month, due := reportMonthDue(now, b.Timezone)
		if !due {
			continue
		}
		if done, err := s.store.HasReportMailing(ctx, b.OrgID, b.BrandID, month); err != nil || done {
			if err != nil {
				return err
			}
			continue
		}
		a := reportActor(b.OrgID)
		token, sh, err := s.CreateReportShare(ctx, a, b.BrandID, month)
		if err != nil {
			s.log.WarnContext(ctx, "sharing a monthly report", "brand", id.Format(id.Brand, b.BrandID), "month", month, "err", err)
			continue
		}
		sealed, err := s.keys.Encrypt(ctx, b.OrgID, shareTokenAAD(sh.ID), []byte(token))
		if err != nil {
			return err
		}
		created, err := s.store.CreateReportMailing(ctx, b.OrgID, b.BrandID, month, sh.ID, sealed, now)
		if err != nil {
			return err
		}
		if !created { // another worker mailed it meanwhile
			if err := s.RevokeReportShare(ctx, a, sh.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// sendReportEmails sends the report emails due, retrying failures as
// notification emails are.
func (s *Service) sendReportEmails(ctx context.Context, orgID *uuid.UUID) (int, error) {
	now := s.Now()
	due, err := s.store.ClaimReportEmails(ctx, orgID, now, now.Add(notificationEmailLease), reportEmailBatch)
	if err != nil {
		return 0, err
	}
	reports := map[string]*Report{}
	n := 0
	for _, d := range due {
		status, reason, next, err := s.deliverReportEmail(ctx, now, d, reports)
		if err != nil {
			return n, err
		}
		if status == "sent" {
			n++
		}
		if err := s.store.FinishReportEmail(ctx, d.ID, status, reason, s.Now(), next); err != nil {
			return n, err
		}
	}
	return n, nil
}

// deliverReportEmail sends one claimed report email and says what became
// of it. Only storage and key errors are returned.
func (s *Service) deliverReportEmail(ctx context.Context, now time.Time, d store.ReportEmailDue, reports map[string]*Report) (status, reason string,
	next *time.Time, err error) {
	if s.cfg.Mail == nil {
		return "suppressed", "the server sends no email", nil, nil
	}
	still, err := s.store.IsReportRecipient(ctx, d.OrgID, d.BrandID, d.To)
	if err != nil {
		return "", "", nil, err
	}
	if !still {
		return "suppressed", "no longer a recipient", nil, nil
	}
	key := d.BrandID.String() + d.Month
	r := reports[key]
	if r == nil {
		if r, err = s.BrandReport(ctx, reportActor(d.OrgID), ReportInput{BrandID: d.BrandID, Month: d.Month}); err != nil {
			s.log.WarnContext(ctx, "computing a monthly report", "brand", id.Format(id.Brand, d.BrandID), "err", err)
			return "queued", truncate(err.Error(), 500), ptr(now.Add(notificationEmailRetry[min(d.Attempts, len(notificationEmailRetry))-1])), nil
		}
		reports[key] = r
	}
	token, err := s.keys.Decrypt(ctx, d.OrgID, shareTokenAAD(d.ShareID), d.SealedToken)
	if err != nil {
		return "", "", nil, err
	}
	o, err := s.store.Org(ctx, d.OrgID)
	if err != nil {
		return "", "", nil, err
	}
	unsub, err := s.reportUnsubscribeURL(ctx, d.OrgID, d.BrandID, d.To)
	if err != nil {
		return "", "", nil, err
	}
	m, err := reportEmail(d, r, o.Name, s.ShareURL(string(token)), unsub).message()
	if err != nil {
		return "", "", nil, err
	}
	m.Headers = map[string]string{"List-Unsubscribe": "<" + unsub + ">", "List-Unsubscribe-Post": "List-Unsubscribe=One-Click"}
	if err := s.cfg.Mail.Send(ctx, m); err != nil {
		s.log.WarnContext(ctx, "sending a report email failed", "brand", id.Format(id.Brand, d.BrandID), "attempt", d.Attempts, "err", err)
		reason = truncate(err.Error(), 500)
		if smtpmail.IsPermanent(err) || d.Attempts >= notificationEmailAttempts {
			return "failed", reason, nil, nil
		}
		return "queued", reason, ptr(now.Add(notificationEmailRetry[min(d.Attempts, len(notificationEmailRetry))-1])), nil
	}
	return "sent", "", nil, nil
}

// reportEmail is a month's report email: what happened, against the month
// before, and the link to the whole report.
func reportEmail(d store.ReportEmailDue, r *Report, orgName, link, unsub string) mailContent {
	month := r.Since.Format("January 2006")
	lines := []string{fmt.Sprintf("Here is %s's report for %s, from %s.", r.Brand.Name, month, orgName)}
	if p := r.Publishing; p != nil {
		lines = append(lines, fmt.Sprintf("Posts published: %d (%d the month before).", p.Published.Now, p.Published.Before))
	}
	if e := r.Engagement; e != nil {
		i := e.Interactions()
		lines = append(lines, fmt.Sprintf("Likes, reposts, replies and quotes on them: %d (%d the month before).", i.Now, i.Before))
	}
	if w := r.Web; w != nil {
		lines = append(lines, fmt.Sprintf("Website visitors: %d, and %d signups (%d and %d the month before).",
			w.Visitors.Now, w.Signups.Now, w.Visitors.Before, w.Signups.Before))
	}
	return mailContent{To: d.To, Subject: r.Brand.Name + ": your report for " + month, Paragraphs: lines,
		ButtonLabel: "Open the report", ButtonURL: link,
		Footer: "The link works for 30 days. You get this because " + orgName + " sends " + r.Brand.Name +
			"'s monthly report to this address. Stop these emails: " + unsub}
}
