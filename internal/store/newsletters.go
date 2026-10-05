// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Mail accounts, newsletter issues and their deliveries (ADR 0024).

const mailAccountCols = `id, org_id, brand_id, livemode, provider, external_id, name, from_name, from_email, reply_to,
	default_audiences, settings, credentials, status, status_note, created_at`

func scanMailAccount(r pgx.Row) (*model.MailAccount, error) {
	var m model.MailAccount
	err := r.Scan(&m.ID, &m.OrgID, &m.BrandID, &m.Livemode, &m.Provider, &m.ExternalID, &m.Name, &m.FromName, &m.FromEmail, &m.ReplyTo,
		&m.DefaultAudiences, &m.Settings, &m.Credentials, &m.Status, &m.StatusNote, &m.CreatedAt)
	return &m, mapErr(err)
}

// CreateMailAccount stores a new account. The same provider account and
// sender on the same brand and mode is ErrConflict.
func (s *Store) CreateMailAccount(ctx context.Context, m *model.MailAccount) error {
	if m.Settings == nil {
		m.Settings = map[string]string{}
	}
	return mapErr(s.q.QueryRow(ctx, `INSERT INTO mail_accounts (id, org_id, brand_id, livemode, provider, external_id, name, from_name,
		from_email, reply_to, default_audiences, settings, credentials, status) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING created_at`, m.ID, m.OrgID, m.BrandID, m.Livemode, m.Provider, m.ExternalID, m.Name, m.FromName, m.FromEmail, m.ReplyTo,
		nonNilStrings(m.DefaultAudiences), m.Settings, m.Credentials, m.Status).Scan(&m.CreatedAt))
}

// MailAccount returns one of an org's mail accounts.
func (s *Store) MailAccount(ctx context.Context, orgID, id uuid.UUID) (*model.MailAccount, error) {
	return scanMailAccount(s.q.QueryRow(ctx, `SELECT `+mailAccountCols+` FROM mail_accounts WHERE org_id = $1 AND id = $2`, orgID, id))
}

// MailAccounts lists an org's mail accounts in a mode, optionally for one
// brand.
func (s *Store) MailAccounts(ctx context.Context, orgID uuid.UUID, livemode bool, brandID *uuid.UUID) ([]*model.MailAccount, error) {
	rows, err := s.q.Query(ctx, `SELECT `+mailAccountCols+` FROM mail_accounts WHERE org_id = $1 AND livemode = $2
		AND ($3::uuid IS NULL OR brand_id = $3) ORDER BY brand_id, provider, name, id`, orgID, livemode, brandID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.MailAccount, error) { return scanMailAccount(r) })
}

// UpdateMailAccount changes an account's sender and default audiences.
func (s *Store) UpdateMailAccount(ctx context.Context, m *model.MailAccount) error {
	return s.execOne(ctx, `UPDATE mail_accounts SET from_name = $3, from_email = $4, reply_to = $5, default_audiences = $6, updated_at = now()
		WHERE org_id = $1 AND id = $2`, m.OrgID, m.ID, m.FromName, m.FromEmail, m.ReplyTo, nonNilStrings(m.DefaultAudiences))
}

// SetMailAccountStatus records whether an account can be used, and why not.
func (s *Store) SetMailAccountStatus(ctx context.Context, orgID, id uuid.UUID, status model.AdAccountStatus, note string) error {
	return s.execOne(ctx, `UPDATE mail_accounts SET status = $3, status_note = $4, updated_at = now() WHERE org_id = $1 AND id = $2`,
		orgID, id, status, note)
}

// OpenNewsletterDeliveries counts an account's deliveries that may still be sent.
func (s *Store) OpenNewsletterDeliveries(ctx context.Context, orgID, accountID uuid.UUID) (int, error) {
	var n int
	err := s.q.QueryRow(ctx, `SELECT count(*) FROM newsletter_deliveries WHERE org_id = $1 AND mail_account_id = $2
		AND status IN ('held', 'queued', 'handed_off')`, orgID, accountID).Scan(&n)
	return n, err
}

// DeleteMailAccount forgets an account. Its past deliveries keep their
// results, without the account.
func (s *Store) DeleteMailAccount(ctx context.Context, orgID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM mail_accounts WHERE org_id = $1 AND id = $2`, orgID, id)
}

// Issues.

const issueCols = `id, org_id, brand_id, livemode, subject, preview_text, body, status, send_at, approval_needed, reviewed_by_user,
	reviewed_by_key, reviewed_at, review_note, created_by_user, created_by_key, created_at, updated_at`

func scanIssue(r pgx.Row) (*model.Issue, error) {
	var is model.Issue
	err := r.Scan(&is.ID, &is.OrgID, &is.BrandID, &is.Livemode, &is.Subject, &is.PreviewText, &is.Body, &is.Status, &is.SendAt,
		&is.ApprovalNeeded, &is.ReviewedByUser, &is.ReviewedByKey, &is.ReviewedAt, &is.ReviewNote, &is.CreatedByUser, &is.CreatedByKey,
		&is.CreatedAt, &is.UpdatedAt)
	return &is, mapErr(err)
}

const deliveryCols = `id, org_id, issue_id, mail_account_id, livemode, provider, account_name, audiences, status, handoff_at, attempts,
	campaign_id, last_error, sent_at, recipients, delivered, opens, clicks, unsubscribes, bounces, complaints, results_read_at,
	next_read_at, updated_at`

func scanDelivery(r pgx.Row) (*model.IssueDelivery, error) {
	var d model.IssueDelivery
	res := &d.Results
	err := r.Scan(&d.ID, &d.OrgID, &d.IssueID, &d.MailAccountID, &d.Livemode, &d.Provider, &d.AccountName, &d.Audiences, &d.Status,
		&d.HandoffAt, &d.Attempts, &d.CampaignID, &d.LastError, &d.SentAt, &res.Recipients, &res.Delivered, &res.Opens, &res.Clicks,
		&res.Unsubscribes, &res.Bounces, &res.Complaints, &d.ResultsReadAt, &d.NextReadAt, &d.UpdatedAt)
	return &d, mapErr(err)
}

func collectDeliveries(rows pgx.Rows, err error) ([]*model.IssueDelivery, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.IssueDelivery, error) { return scanDelivery(r) })
}

// CreateIssue stores an issue with its deliveries and media.
func (s *Store) CreateIssue(ctx context.Context, is *model.Issue) error {
	if err := s.q.QueryRow(ctx, `INSERT INTO newsletter_issues (id, org_id, brand_id, livemode, subject, preview_text, body, status,
		send_at, approval_needed, created_by_user, created_by_key) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING created_at, updated_at`, is.ID, is.OrgID, is.BrandID, is.Livemode, is.Subject, is.PreviewText, is.Body, is.Status,
		is.SendAt, is.ApprovalNeeded, is.CreatedByUser, is.CreatedByKey).Scan(&is.CreatedAt, &is.UpdatedAt); err != nil {
		return mapErr(err)
	}
	return s.replaceIssueParts(ctx, is)
}

// replaceIssueParts rewrites an issue's deliveries and media.
func (s *Store) replaceIssueParts(ctx context.Context, is *model.Issue) error {
	if _, err := s.q.Exec(ctx, `DELETE FROM newsletter_deliveries WHERE org_id = $1 AND issue_id = $2`, is.OrgID, is.ID); err != nil {
		return err
	}
	for i := range is.Deliveries {
		d := &is.Deliveries[i]
		if d.Audiences == nil {
			d.Audiences = []model.Audience{}
		}
		if _, err := s.q.Exec(ctx, `INSERT INTO newsletter_deliveries (id, org_id, issue_id, mail_account_id, livemode, provider, account_name,
			audiences, status) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, d.ID, is.OrgID, is.ID, d.MailAccountID, is.Livemode, d.Provider,
			d.AccountName, d.Audiences, d.Status); err != nil {
			return mapErr(err)
		}
	}
	if _, err := s.q.Exec(ctx, `DELETE FROM newsletter_media WHERE org_id = $1 AND issue_id = $2`, is.OrgID, is.ID); err != nil {
		return err
	}
	for _, m := range is.Media {
		if _, err := s.q.Exec(ctx, `INSERT INTO newsletter_media (org_id, issue_id, media_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			is.OrgID, is.ID, m); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// UpdateIssue rewrites a draft's content, deliveries and media.
func (s *Store) UpdateIssue(ctx context.Context, is *model.Issue) error {
	if err := s.execOne(ctx, `UPDATE newsletter_issues SET subject = $3, preview_text = $4, body = $5, send_at = $6, updated_at = now()
		WHERE org_id = $1 AND id = $2`, is.OrgID, is.ID, is.Subject, is.PreviewText, is.Body, is.SendAt); err != nil {
		return err
	}
	return s.replaceIssueParts(ctx, is)
}

// Issue returns one of an org's issues with its deliveries and media.
func (s *Store) Issue(ctx context.Context, orgID, id uuid.UUID) (*model.Issue, error) {
	var out *model.Issue
	err := s.snapshot(ctx, func(tx *Store) error {
		is, err := scanIssue(tx.q.QueryRow(ctx, `SELECT `+issueCols+` FROM newsletter_issues WHERE org_id = $1 AND id = $2`, orgID, id))
		if err != nil {
			return err
		}
		out = is
		return tx.attachIssueParts(ctx, orgID, []*model.Issue{is})
	})
	return out, err
}

// LockIssue reads an issue, with its parts, locking its row for the rest
// of the transaction.
func (s *Store) LockIssue(ctx context.Context, orgID, id uuid.UUID) (*model.Issue, error) {
	is, err := scanIssue(s.q.QueryRow(ctx, `SELECT `+issueCols+` FROM newsletter_issues WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
	if err != nil {
		return nil, err
	}
	return is, s.attachIssueParts(ctx, orgID, []*model.Issue{is})
}

// IssueFilter narrows a listing.
type IssueFilter struct {
	BrandID *uuid.UUID
	Status  model.IssueStatus
}

// Issues lists issues newest first.
func (s *Store) Issues(ctx context.Context, orgID uuid.UUID, livemode bool, f IssueFilter, page Page) ([]*model.Issue, bool, error) {
	var out []*model.Issue
	var more bool
	err := s.snapshot(ctx, func(tx *Store) error {
		where, order, extra := pageClause(page, "id", 5)
		rows, err := tx.q.Query(ctx, `SELECT `+issueCols+` FROM newsletter_issues WHERE org_id = $1 AND livemode = $2
			AND ($3::uuid IS NULL OR brand_id = $3) AND ($4 = '' OR status = $4)`+where+order,
			append([]any{orgID, livemode, f.BrandID, string(f.Status)}, extra...)...)
		if err != nil {
			return err
		}
		issues, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Issue, error) { return scanIssue(r) })
		if err != nil {
			return err
		}
		out, more = trimPage(page, issues)
		return tx.attachIssueParts(ctx, orgID, out)
	})
	return out, more, err
}

func (s *Store) attachIssueParts(ctx context.Context, orgID uuid.UUID, issues []*model.Issue) error {
	if len(issues) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(issues))
	byID := map[uuid.UUID]*model.Issue{}
	for i, is := range issues {
		ids[i], byID[is.ID] = is.ID, is
		is.Deliveries, is.Media = nil, nil
	}
	ds, err := collectDeliveries(s.q.Query(ctx, `SELECT `+deliveryCols+` FROM newsletter_deliveries WHERE org_id = $1 AND issue_id = ANY($2)
		ORDER BY issue_id, account_name, id`, orgID, ids))
	if err != nil {
		return err
	}
	for _, d := range ds {
		is := byID[d.IssueID]
		is.Deliveries = append(is.Deliveries, *d)
	}
	rows, err := s.q.Query(ctx, `SELECT issue_id, media_id FROM newsletter_media WHERE org_id = $1 AND issue_id = ANY($2) ORDER BY issue_id, media_id`,
		orgID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var issueID, mediaID uuid.UUID
		if err := rows.Scan(&issueID, &mediaID); err != nil {
			return err
		}
		byID[issueID].Media = append(byID[issueID].Media, mediaID)
	}
	return rows.Err()
}

// SetIssueSchedule sets an issue's status, send time and whether it needs
// approval.
func (s *Store) SetIssueSchedule(ctx context.Context, orgID, id uuid.UUID, status model.IssueStatus, sendAt *time.Time, approvalNeeded bool) error {
	return s.execOne(ctx, `UPDATE newsletter_issues SET status = $3, send_at = $4, approval_needed = $5, updated_at = now()
		WHERE org_id = $1 AND id = $2`, orgID, id, status, sendAt, approvalNeeded)
}

// SetIssueStatus records an issue's status.
func (s *Store) SetIssueStatus(ctx context.Context, orgID, id uuid.UUID, status model.IssueStatus) error {
	return s.execOne(ctx, `UPDATE newsletter_issues SET status = $3, updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, status)
}

// ReviewIssue records who approved or rejected an issue, and sets its status.
func (s *Store) ReviewIssue(ctx context.Context, orgID, id uuid.UUID, userID, keyID *uuid.UUID, status model.IssueStatus, note string, at time.Time) error {
	return s.execOne(ctx, `UPDATE newsletter_issues SET status = $3, reviewed_by_user = $4, reviewed_by_key = $5, reviewed_at = $6, review_note = $7,
		updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, status, userID, keyID, at, note)
}

// SetDeliveries moves an issue's deliveries in one of from to status to,
// with a hand-off time (nil clears it), and returns how many moved.
func (s *Store) SetDeliveries(ctx context.Context, orgID, issueID uuid.UUID, from []model.DeliveryStatus, to model.DeliveryStatus, handoffAt *time.Time) (int64, error) {
	tag, err := s.q.Exec(ctx, `UPDATE newsletter_deliveries SET status = $4, handoff_at = $5, lease_until = NULL, updated_at = now()
		WHERE org_id = $1 AND issue_id = $2 AND status = ANY($3)`, orgID, issueID, deliveryStatuses(from), to, handoffAt)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func deliveryStatuses(ss []model.DeliveryStatus) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = string(s)
	}
	return out
}

// ResetDeliveries returns an issue's open deliveries to draft, forgetting
// their campaigns and attempts.
func (s *Store) ResetDeliveries(ctx context.Context, orgID, issueID uuid.UUID) error {
	_, err := s.q.Exec(ctx, `UPDATE newsletter_deliveries SET status = 'draft', handoff_at = NULL, lease_until = NULL, attempts = 0,
		campaign_id = '', last_error = '', next_read_at = NULL, updated_at = now()
		WHERE org_id = $1 AND issue_id = $2 AND status IN ('draft', 'held', 'queued', 'handed_off')`, orgID, issueID)
	return err
}

// HandoffInFlight reports whether one of an issue's deliveries is being
// handed to its provider right now.
func (s *Store) HandoffInFlight(ctx context.Context, orgID, issueID uuid.UUID, now time.Time) (bool, error) {
	var busy bool
	err := s.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM newsletter_deliveries WHERE org_id = $1 AND issue_id = $2 AND status = 'queued'
		AND lease_until > $3)`, orgID, issueID, now).Scan(&busy)
	return busy, err
}

// ClaimDueHandoffs takes queued deliveries of providers the caller can
// send through whose hand-off is due, in one org or (orgID nil) every
// org but suspended ones (ADR 0031): each is leased, and counts an attempt.
func (s *Store) ClaimDueHandoffs(ctx context.Context, orgID *uuid.UUID, providers []string, now time.Time, lease time.Duration, limit int) ([]*model.IssueDelivery, error) {
	return collectDeliveries(s.q.Query(ctx, `UPDATE newsletter_deliveries SET lease_until = $3, attempts = attempts + 1, updated_at = now()
		WHERE id IN (
			SELECT id FROM newsletter_deliveries WHERE status = 'queued' AND handoff_at <= $2 AND (lease_until IS NULL OR lease_until <= $2)
				AND ($1::uuid IS NULL OR org_id = $1) AND provider = ANY($5)
				AND NOT EXISTS (SELECT 1 FROM orgs WHERE orgs.id = newsletter_deliveries.org_id AND orgs.status = 'suspended')
			ORDER BY handoff_at LIMIT $4 FOR UPDATE SKIP LOCKED)
		RETURNING `+deliveryCols, orgID, now, now.Add(lease), limit, providers))
}

// DeliveryHandedOff records a delivery's campaign at its provider. It is
// ErrNotFound if the delivery stopped being queued meanwhile.
func (s *Store) DeliveryHandedOff(ctx context.Context, orgID, id uuid.UUID, campaignID string, nextRead time.Time) error {
	return s.execOne(ctx, `UPDATE newsletter_deliveries SET status = 'handed_off', campaign_id = $3, next_read_at = $4, lease_until = NULL,
		last_error = '', updated_at = now() WHERE org_id = $1 AND id = $2 AND status = 'queued'`, orgID, id, campaignID, nextRead)
}

// RetryHandoff keeps a delivery queued, to be tried again at next.
func (s *Store) RetryHandoff(ctx context.Context, orgID, id uuid.UUID, lastError string, next time.Time) error {
	return s.execOne(ctx, `UPDATE newsletter_deliveries SET lease_until = $3, last_error = $4, updated_at = now()
		WHERE org_id = $1 AND id = $2 AND status = 'queued'`, orgID, id, next, lastError)
}

// EndDelivery sets a delivery's final status and why.
func (s *Store) EndDelivery(ctx context.Context, orgID, id uuid.UUID, status model.DeliveryStatus, lastError string) error {
	return s.execOne(ctx, `UPDATE newsletter_deliveries SET status = $3, last_error = $4, lease_until = NULL, next_read_at = NULL,
		updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, status, lastError)
}

// ClaimDueDeliveryReads takes handed-off and sent deliveries of providers
// the caller can read that are due a look, pushing the next look back by
// lease.
func (s *Store) ClaimDueDeliveryReads(ctx context.Context, orgID *uuid.UUID, providers []string, now time.Time, lease time.Duration, limit int) ([]*model.IssueDelivery, error) {
	return collectDeliveries(s.q.Query(ctx, `UPDATE newsletter_deliveries SET next_read_at = $3 WHERE id IN (
			SELECT id FROM newsletter_deliveries WHERE status IN ('handed_off', 'sent') AND next_read_at <= $2
				AND ($1::uuid IS NULL OR org_id = $1) AND provider = ANY($5)
			ORDER BY next_read_at LIMIT $4 FOR UPDATE SKIP LOCKED)
		RETURNING `+deliveryCols, orgID, now, now.Add(lease), limit, providers))
}

// SaveDeliveryRead records what the provider said: the delivery's status,
// when it was sent, its results, and when to look next (nil: never).
func (s *Store) SaveDeliveryRead(ctx context.Context, d *model.IssueDelivery, readAt time.Time) error {
	r := d.Results
	return s.execOne(ctx, `UPDATE newsletter_deliveries SET status = $3, sent_at = $4, recipients = $5, delivered = $6, opens = $7, clicks = $8,
		unsubscribes = $9, bounces = $10, complaints = $11, results_read_at = $12, next_read_at = $13, last_error = $14, updated_at = now()
		WHERE org_id = $1 AND id = $2`, d.OrgID, d.ID, d.Status, d.SentAt, r.Recipients, r.Delivered, r.Opens, r.Clicks, r.Unsubscribes,
		r.Bounces, r.Complaints, readAt, d.NextReadAt, d.LastError)
}

// NewsletterDelivery returns one of an org's newsletter deliveries.
func (s *Store) NewsletterDelivery(ctx context.Context, orgID, id uuid.UUID) (*model.IssueDelivery, error) {
	return scanDelivery(s.q.QueryRow(ctx, `SELECT `+deliveryCols+` FROM newsletter_deliveries WHERE org_id = $1 AND id = $2`, orgID, id))
}
