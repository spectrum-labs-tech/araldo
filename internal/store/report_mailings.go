// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// The monthly report by email (ADR 0026 phase 3).

// ReportRecipients lists who gets a brand's monthly report, by address.
func (s *Store) ReportRecipients(ctx context.Context, orgID, brandID uuid.UUID) ([]model.ReportRecipient, error) {
	rows, err := s.q.Query(ctx, `SELECT email, created_at FROM report_recipients WHERE org_id = $1 AND brand_id = $2 ORDER BY email`, orgID, brandID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.ReportRecipient, error) {
		var rr model.ReportRecipient
		return rr, r.Scan(&rr.Email, &rr.CreatedAt)
	})
}

// AddReportRecipient adds an address; ErrConflict if it is there already.
func (s *Store) AddReportRecipient(ctx context.Context, orgID, brandID uuid.UUID, email string, by *uuid.UUID) error {
	_, err := s.q.Exec(ctx, `INSERT INTO report_recipients (org_id, brand_id, email, created_by) VALUES ($1, $2, $3, $4)`, orgID, brandID, email, by)
	return mapErr(err)
}

// RemoveReportRecipient removes an address.
func (s *Store) RemoveReportRecipient(ctx context.Context, orgID, brandID uuid.UUID, email string) error {
	return s.execOne(ctx, `DELETE FROM report_recipients WHERE org_id = $1 AND brand_id = $2 AND email = $3`, orgID, brandID, email)
}

// ReportBrand is a brand whose report goes to someone.
type ReportBrand struct {
	OrgID    uuid.UUID
	BrandID  uuid.UUID
	Timezone string
}

// ReportBrands lists the brands with recipients, in active orgs, in one org
// or (orgID nil) every org.
func (s *Store) ReportBrands(ctx context.Context, orgID *uuid.UUID) ([]ReportBrand, error) {
	rows, err := s.q.Query(ctx, `SELECT b.org_id, b.id, b.timezone FROM brands b JOIN orgs o ON o.id = b.org_id
		WHERE o.status = 'active' AND ($1::uuid IS NULL OR b.org_id = $1)
		AND EXISTS (SELECT 1 FROM report_recipients r WHERE r.org_id = b.org_id AND r.brand_id = b.id)`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ReportBrand, error) {
		var b ReportBrand
		return b, r.Scan(&b.OrgID, &b.BrandID, &b.Timezone)
	})
}

// HasReportMailing reports whether a brand's month was mailed already.
func (s *Store) HasReportMailing(ctx context.Context, orgID, brandID uuid.UUID, month string) (bool, error) {
	var ok bool
	err := s.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM report_mailings WHERE org_id = $1 AND brand_id = $2 AND month = $3)`,
		orgID, brandID, month).Scan(&ok)
	return ok, err
}

// CreateReportMailing records a brand's month, once, and queues an email
// to each of its recipients at now; it reports false when the month was
// mailed already.
func (s *Store) CreateReportMailing(ctx context.Context, orgID, brandID uuid.UUID, month string, shareID uuid.UUID, sealedToken []byte,
	now time.Time) (bool, error) {
	tag, err := s.q.Exec(ctx, `INSERT INTO report_mailings (org_id, brand_id, month, share_id, share_token, created_at) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT DO NOTHING`, orgID, brandID, month, shareID, sealedToken, now)
	if err != nil || tag.RowsAffected() == 0 {
		return false, mapErr(err)
	}
	_, err = s.q.Exec(ctx, `INSERT INTO report_emails (id, org_id, brand_id, month, email, status, next_attempt_at, created_at)
		SELECT gen_random_uuid(), org_id, brand_id, $3, email, 'queued', $4, $4 FROM report_recipients WHERE org_id = $1 AND brand_id = $2`,
		orgID, brandID, month, now)
	return true, mapErr(err)
}

// ReportEmailDue is a report email claimed for sending.
type ReportEmailDue struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	BrandID     uuid.UUID
	Month       string
	To          string
	Attempts    int
	ShareID     uuid.UUID
	SealedToken []byte
}

// ClaimReportEmails claims queued report emails due at now, in one org or
// (orgID nil) every org, each until lease; an address no longer a
// recipient is still claimed, for the caller to suppress.
func (s *Store) ClaimReportEmails(ctx context.Context, orgID *uuid.UUID, now, lease time.Time, limit int) ([]ReportEmailDue, error) {
	rows, err := s.q.Query(ctx, `UPDATE report_emails e SET attempts = e.attempts + 1, next_attempt_at = $2
		FROM report_mailings m
		WHERE m.org_id = e.org_id AND m.brand_id = e.brand_id AND m.month = e.month AND e.id IN (
			SELECT id FROM report_emails WHERE status = 'queued' AND next_attempt_at <= $1 AND ($4::uuid IS NULL OR org_id = $4)
			ORDER BY next_attempt_at LIMIT $3 FOR UPDATE SKIP LOCKED)
		RETURNING e.id, e.org_id, e.brand_id, e.month, e.email, e.attempts, m.share_id, m.share_token`, now, lease, limit, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ReportEmailDue, error) {
		var d ReportEmailDue
		return d, r.Scan(&d.ID, &d.OrgID, &d.BrandID, &d.Month, &d.To, &d.Attempts, &d.ShareID, &d.SealedToken)
	})
}

// IsReportRecipient reports whether an address still gets a brand's report.
func (s *Store) IsReportRecipient(ctx context.Context, orgID, brandID uuid.UUID, email string) (bool, error) {
	var ok bool
	err := s.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM report_recipients WHERE org_id = $1 AND brand_id = $2 AND email = $3)`,
		orgID, brandID, email).Scan(&ok)
	return ok, err
}

// FinishReportEmail records a report email's outcome, as
// FinishNotificationEmail does.
func (s *Store) FinishReportEmail(ctx context.Context, id uuid.UUID, status, reason string, at time.Time, next *time.Time) error {
	var sentAt *time.Time
	if status == "sent" {
		sentAt = &at
	}
	return s.execOne(ctx, `UPDATE report_emails SET status = $2, reason = $3, sent_at = $4, next_attempt_at = $5 WHERE id = $1`,
		id, status, reason, sentAt, next)
}

// ReportEmailStatus returns a recipient's email status for a month, and why.
func (s *Store) ReportEmailStatus(ctx context.Context, orgID, brandID uuid.UUID, month, email string) (status, reason string, err error) {
	err = s.q.QueryRow(ctx, `SELECT status, reason FROM report_emails WHERE org_id = $1 AND brand_id = $2 AND month = $3 AND email = $4`,
		orgID, brandID, month, email).Scan(&status, &reason)
	return status, reason, mapErr(err)
}
