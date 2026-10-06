// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Notifications (ADR 0034).

// CreateNotification stores a notification and, unless emailStatus is
// empty, its email with that status and reason. It reports false, storing
// nothing, when the person already has one with the same dedupe key.
func (s *Store) CreateNotification(ctx context.Context, n *model.Notification, emailStatus, reason string, at time.Time) (bool, error) {
	tag, err := s.q.Exec(ctx, `INSERT INTO notifications (id, user_id, org_id, type, subject, body, link, shown, dedupe_key, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), $10) ON CONFLICT (user_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING`,
		n.ID, n.UserID, n.OrgID, n.Type, n.Subject, n.Body, n.Link, n.Shown, n.DedupeKey, at)
	if err != nil || tag.RowsAffected() == 0 {
		return false, mapErr(err)
	}
	if emailStatus != "" {
		var next *time.Time
		if emailStatus == "queued" {
			next = &at
		}
		if _, err := s.q.Exec(ctx, `INSERT INTO notification_emails (notification_id, status, reason, next_attempt_at, created_at)
			VALUES ($1, $2, $3, $4, $5)`, n.ID, emailStatus, reason, next, at); err != nil {
			return false, mapErr(err)
		}
	}
	return true, nil
}

// NotificationPrefs returns the choices people made for a type in an org,
// by person; those who made none are missing.
func (s *Store) NotificationPrefs(ctx context.Context, orgID uuid.UUID, typ string, userIDs []uuid.UUID) (map[uuid.UUID]model.NotificationPref, error) {
	rows, err := s.q.Query(ctx, `SELECT user_id, type, in_app, email FROM notification_preferences
		WHERE org_id = $1 AND type = $2 AND user_id = ANY($3)`, orgID, typ, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]model.NotificationPref{}
	for rows.Next() {
		var u uuid.UUID
		var p model.NotificationPref
		if err := rows.Scan(&u, &p.Type, &p.InApp, &p.Email); err != nil {
			return nil, err
		}
		out[u] = p
	}
	return out, rows.Err()
}

// UserNotificationPrefs returns a person's choices in an org, by type.
func (s *Store) UserNotificationPrefs(ctx context.Context, userID, orgID uuid.UUID) (map[string]model.NotificationPref, error) {
	rows, err := s.q.Query(ctx, `SELECT type, in_app, email FROM notification_preferences WHERE user_id = $1 AND org_id = $2`, userID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.NotificationPref{}
	for rows.Next() {
		var p model.NotificationPref
		if err := rows.Scan(&p.Type, &p.InApp, &p.Email); err != nil {
			return nil, err
		}
		out[p.Type] = p
	}
	return out, rows.Err()
}

// SetNotificationPref records a person's choice for a type in an org.
func (s *Store) SetNotificationPref(ctx context.Context, userID, orgID uuid.UUID, p model.NotificationPref) error {
	_, err := s.q.Exec(ctx, `INSERT INTO notification_preferences (user_id, org_id, type, in_app, email) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, org_id, type) DO UPDATE SET in_app = $4, email = $5, updated_at = now()`, userID, orgID, p.Type, p.InApp, p.Email)
	return mapErr(err)
}

const notificationCols = `n.id, n.user_id, n.org_id, COALESCE(o.name, ''), n.type, n.subject, n.body, n.link, n.shown,
	COALESCE(n.dedupe_key, ''), n.read_at, n.created_at`

func scanNotification(r pgx.Row) (*model.Notification, error) {
	var n model.Notification
	err := r.Scan(&n.ID, &n.UserID, &n.OrgID, &n.OrgName, &n.Type, &n.Subject, &n.Body, &n.Link, &n.Shown, &n.DedupeKey, &n.ReadAt, &n.CreatedAt)
	return &n, mapErr(err)
}

// Notifications lists a person's notifications the dashboard shows, newest
// first, before a time (nil for the newest).
func (s *Store) Notifications(ctx context.Context, userID uuid.UUID, before *time.Time, limit int) ([]*model.Notification, error) {
	rows, err := s.q.Query(ctx, `SELECT `+notificationCols+` FROM notifications n LEFT JOIN orgs o ON o.id = n.org_id
		WHERE n.user_id = $1 AND n.shown AND ($2::timestamptz IS NULL OR n.created_at < $2) ORDER BY n.created_at DESC LIMIT $3`, userID, before, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Notification, error) { return scanNotification(r) })
}

// UnreadNotifications counts a person's unread notifications.
func (s *Store) UnreadNotifications(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := s.q.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND shown AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// ReadNotification marks one of a person's notifications read and returns
// it.
func (s *Store) ReadNotification(ctx context.Context, userID, id uuid.UUID, at time.Time) (*model.Notification, error) {
	if _, err := s.q.Exec(ctx, `UPDATE notifications SET read_at = $3 WHERE user_id = $1 AND id = $2 AND read_at IS NULL`, userID, id, at); err != nil {
		return nil, err
	}
	return scanNotification(s.q.QueryRow(ctx, `SELECT `+notificationCols+` FROM notifications n LEFT JOIN orgs o ON o.id = n.org_id
		WHERE n.user_id = $1 AND n.id = $2`, userID, id))
}

// ReadAllNotifications marks all of a person's notifications read.
func (s *Store) ReadAllNotifications(ctx context.Context, userID uuid.UUID, at time.Time) error {
	_, err := s.q.Exec(ctx, `UPDATE notifications SET read_at = $2 WHERE user_id = $1 AND read_at IS NULL`, userID, at)
	return err
}

// PruneNotifications deletes read notifications from before readCutoff and
// any from before cutoff.
func (s *Store) PruneNotifications(ctx context.Context, readCutoff, cutoff time.Time) (int, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM notifications WHERE (read_at IS NOT NULL AND created_at < $1) OR created_at < $2`, readCutoff, cutoff)
	return int(tag.RowsAffected()), err
}

// NotificationEmail returns a notification's email status and why.
func (s *Store) NotificationEmail(ctx context.Context, notificationID uuid.UUID) (status, reason string, err error) {
	err = s.q.QueryRow(ctx, `SELECT status, reason FROM notification_emails WHERE notification_id = $1`, notificationID).Scan(&status, &reason)
	return status, reason, mapErr(err)
}

// NotificationEmailDue is a notification email claimed for sending.
type NotificationEmailDue struct {
	model.Notification
	To       string
	Attempts int
}

// ClaimNotificationEmails claims queued emails due at now, for a person
// (userID) or everyone: each counts an attempt and is not due again until
// lease, so another worker leaves it while this one sends.
func (s *Store) ClaimNotificationEmails(ctx context.Context, userID *uuid.UUID, now, lease time.Time, limit int) ([]NotificationEmailDue, error) {
	rows, err := s.q.Query(ctx, `UPDATE notification_emails e SET attempts = e.attempts + 1, next_attempt_at = $2
		FROM notifications n JOIN users u ON u.id = n.user_id LEFT JOIN orgs o ON o.id = n.org_id
		WHERE e.notification_id = n.id AND e.notification_id IN (
			SELECT x.notification_id FROM notification_emails x JOIN notifications y ON y.id = x.notification_id
			WHERE x.status = 'queued' AND x.next_attempt_at <= $1 AND ($4::uuid IS NULL OR y.user_id = $4)
			ORDER BY x.next_attempt_at LIMIT $3 FOR UPDATE OF x SKIP LOCKED)
		RETURNING n.id, n.user_id, n.org_id, COALESCE(o.name, ''), n.type, n.subject, n.body, n.link, n.created_at, u.email, e.attempts`,
		now, lease, limit, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (NotificationEmailDue, error) {
		var d NotificationEmailDue
		err := r.Scan(&d.ID, &d.UserID, &d.OrgID, &d.OrgName, &d.Type, &d.Subject, &d.Body, &d.Link, &d.CreatedAt, &d.To, &d.Attempts)
		return d, err
	})
}

// FinishNotificationEmail records an email's outcome: sent (at), failed or
// suppressed (with why), or queued again for next.
func (s *Store) FinishNotificationEmail(ctx context.Context, notificationID uuid.UUID, status, reason string, at time.Time, next *time.Time) error {
	var sentAt *time.Time
	if status == "sent" {
		sentAt = &at
	}
	return s.execOne(ctx, `UPDATE notification_emails SET status = $2, reason = $3, sent_at = $4, next_attempt_at = $5 WHERE notification_id = $1`,
		notificationID, status, reason, sentAt, next)
}

// NotificationEmailsSent counts the notification emails sent to a person
// since a time.
func (s *Store) NotificationEmailsSent(ctx context.Context, userID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := s.q.QueryRow(ctx, `SELECT count(*) FROM notification_emails e JOIN notifications n ON n.id = e.notification_id
		WHERE n.user_id = $1 AND e.status = 'sent' AND e.sent_at >= $2`, userID, since).Scan(&n)
	return n, err
}
