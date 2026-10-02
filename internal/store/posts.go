// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

const postCols = `id, org_id, brand_id, livemode, status, template_id, template_version, data, content, publish_at, publish_by, slot_at,
	metadata, approval_needed, reviewed_by, reviewed_at, review_note, created_by_user, created_by_key, created_at, updated_at`

func scanPost(r pgx.Row) (*model.Post, error) {
	var p model.Post
	var data, content []byte
	err := r.Scan(&p.ID, &p.OrgID, &p.BrandID, &p.Livemode, &p.Status, &p.TemplateID, &p.TemplateVersion, &data, &content,
		&p.PublishAt, &p.PublishBy, &p.SlotAt, &p.Metadata, &p.ApprovalNeeded, &p.ReviewedBy, &p.ReviewedAt, &p.ReviewNote,
		&p.CreatedByUser, &p.CreatedByKey, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	p.Data = data
	if len(content) > 0 {
		p.Content = &model.Content{}
		if err := json.Unmarshal(content, p.Content); err != nil {
			return nil, err
		}
	}
	return &p, nil
}

// CreatePost inserts a post and its targets.
func (s *Store) CreatePost(ctx context.Context, p *model.Post) error {
	if p.Metadata == nil {
		p.Metadata = map[string]string{}
	}
	var data, content any
	if len(p.Data) > 0 {
		data = p.Data
	}
	if p.Content != nil {
		content = p.Content
	}
	_, err := s.q.Exec(ctx, `INSERT INTO posts (id, org_id, brand_id, livemode, status, template_id, template_version, data, content,
		publish_at, publish_by, slot_at, metadata, approval_needed, created_by_user, created_by_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		p.ID, p.OrgID, p.BrandID, p.Livemode, p.Status, p.TemplateID, p.TemplateVersion, data, content,
		p.PublishAt, p.PublishBy, p.SlotAt, p.Metadata, p.ApprovalNeeded, p.CreatedByUser, p.CreatedByKey)
	if err != nil {
		return mapErr(err)
	}
	for i, m := range p.Media {
		if _, err := s.q.Exec(ctx, `INSERT INTO post_media (org_id, post_id, position, media_id) VALUES ($1, $2, $3, $4)`,
			p.OrgID, p.ID, i, m.ID); err != nil {
			return mapErr(err)
		}
	}
	for i := range p.Targets {
		t := &p.Targets[i]
		if t.Posted == nil {
			t.Posted = []platform.RemoteRef{}
		}
		if _, err := s.q.Exec(ctx, `INSERT INTO post_targets (id, org_id, post_id, channel_id, livemode, provider, parts, status,
			next_attempt_at, publish_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			t.ID, t.OrgID, t.PostID, t.ChannelID, t.Livemode, string(t.Provider), t.Parts, t.Status, t.NextAttemptAt, t.PublishBy); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// Post returns a post with its targets and media.
func (s *Store) Post(ctx context.Context, orgID, id uuid.UUID) (*model.Post, error) {
	p, err := scanPost(s.q.QueryRow(ctx, `SELECT `+postCols+` FROM posts WHERE org_id = $1 AND id = $2`, orgID, id))
	if err != nil {
		return nil, err
	}
	return p, s.complete(ctx, orgID, p)
}

// PostForUpdate locks a post row.
func (s *Store) PostForUpdate(ctx context.Context, orgID, id uuid.UUID) (*model.Post, error) {
	p, err := scanPost(s.q.QueryRow(ctx, `SELECT `+postCols+` FROM posts WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
	if err != nil {
		return nil, err
	}
	return p, s.complete(ctx, orgID, p)
}

// complete loads a post's targets and media.
func (s *Store) complete(ctx context.Context, orgID uuid.UUID, p *model.Post) error {
	var err error
	if p.Targets, err = s.Targets(ctx, orgID, p.ID); err != nil {
		return err
	}
	return s.attachMedia(ctx, orgID, []*model.Post{p})
}

// PostMedia returns a post's media, in order.
func (s *Store) PostMedia(ctx context.Context, orgID, postID uuid.UUID) ([]*model.Media, error) {
	p := &model.Post{ID: postID}
	err := s.attachMedia(ctx, orgID, []*model.Post{p})
	return p.Media, err
}

// PostFilter narrows a post listing.
type PostFilter struct {
	BrandID  *uuid.UUID
	Status   string
	Metadata map[string]string
}

// Posts lists posts newest first, each with its targets.
func (s *Store) Posts(ctx context.Context, orgID uuid.UUID, livemode bool, f PostFilter, page Page) ([]*model.Post, bool, error) {
	where, order, extra := pageClause(page, "id", 6)
	var meta any
	if len(f.Metadata) > 0 {
		meta = f.Metadata
	}
	args := append([]any{orgID, livemode, f.BrandID, f.Status, meta}, extra...)
	rows, err := s.q.Query(ctx, `SELECT `+postCols+` FROM posts WHERE org_id = $1 AND livemode = $2
		AND ($3::uuid IS NULL OR brand_id = $3) AND ($4 = '' OR status = $4) AND ($5::jsonb IS NULL OR metadata @> $5)`+where+order, args...)
	if err != nil {
		return nil, false, err
	}
	posts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Post, error) { return scanPost(r) })
	if err != nil {
		return nil, false, err
	}
	posts, more := trimPage(page, posts)
	if err := s.attachTargets(ctx, orgID, posts); err != nil {
		return nil, false, err
	}
	if err := s.attachMedia(ctx, orgID, posts); err != nil {
		return nil, false, err
	}
	return posts, more, nil
}

func (s *Store) attachTargets(ctx context.Context, orgID uuid.UUID, posts []*model.Post) error {
	if len(posts) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(posts))
	byID := map[uuid.UUID]*model.Post{}
	for i, p := range posts {
		ids[i] = p.ID
		byID[p.ID] = p
	}
	rows, err := s.q.Query(ctx, `SELECT `+targetCols+` FROM post_targets t JOIN channels c ON c.id = t.channel_id
		WHERE t.org_id = $1 AND t.post_id = ANY($2) ORDER BY t.id`, orgID, ids)
	if err != nil {
		return err
	}
	targets, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Target, error) { return scanTarget(r) })
	if err != nil {
		return err
	}
	for _, t := range targets {
		byID[t.PostID].Targets = append(byID[t.PostID].Targets, t)
	}
	return nil
}

// SetPostStatus stores a post's status.
func (s *Store) SetPostStatus(ctx context.Context, orgID, id uuid.UUID, status model.PostStatus) error {
	return s.execOne(ctx, `UPDATE posts SET status = $3, updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, status)
}

// ReviewPost records an approval or rejection.
func (s *Store) ReviewPost(ctx context.Context, orgID, id, reviewer uuid.UUID, status model.PostStatus, note string, at time.Time) error {
	return s.execOne(ctx, `UPDATE posts SET status = $3, reviewed_by = $4, reviewed_at = $5, review_note = $6, updated_at = now()
		WHERE org_id = $1 AND id = $2`, orgID, id, status, reviewer, at, note)
}

// Targets.

const targetCols = `t.id, t.org_id, t.post_id, t.channel_id, t.livemode, t.provider, t.parts, t.status, t.attempts, t.next_attempt_at,
	t.publish_by, COALESCE(t.lease_owner, ''), t.lease_until, t.posted, t.permalink, t.error_code, t.error_message, t.published_at,
	t.created_at, t.updated_at, c.display_name`

func scanTarget(r pgx.Row) (model.Target, error) {
	var t model.Target
	var provider string
	err := r.Scan(&t.ID, &t.OrgID, &t.PostID, &t.ChannelID, &t.Livemode, &provider, &t.Parts, &t.Status, &t.Attempts, &t.NextAttemptAt,
		&t.PublishBy, &t.LeaseOwner, &t.LeaseUntil, &t.Posted, &t.Permalink, &t.ErrorCode, &t.ErrorMessage, &t.PublishedAt,
		&t.CreatedAt, &t.UpdatedAt, &t.ChannelName)
	t.Provider = platform.Provider(provider)
	return t, mapErr(err)
}

func (s *Store) Targets(ctx context.Context, orgID, postID uuid.UUID) ([]model.Target, error) {
	rows, err := s.q.Query(ctx, `SELECT `+targetCols+` FROM post_targets t JOIN channels c ON c.id = t.channel_id
		WHERE t.org_id = $1 AND t.post_id = $2 ORDER BY t.id`, orgID, postID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Target, error) { return scanTarget(r) })
}

func (s *Store) Target(ctx context.Context, orgID, id uuid.UUID) (*model.Target, error) {
	t, err := scanTarget(s.q.QueryRow(ctx, `SELECT `+targetCols+` FROM post_targets t JOIN channels c ON c.id = t.channel_id
		WHERE t.org_id = $1 AND t.id = $2`, orgID, id))
	return &t, err
}

// TargetAnyOrg reads a target by ID alone, for the sandbox page, which
// shows only what the sandbox "published".
func (s *Store) TargetAnyOrg(ctx context.Context, id uuid.UUID) (*model.Target, error) {
	t, err := scanTarget(s.q.QueryRow(ctx, `SELECT `+targetCols+` FROM post_targets t JOIN channels c ON c.id = t.channel_id
		WHERE t.id = $1 AND NOT t.livemode`, id))
	return &t, err
}

// UpdateTargetParts replaces a queued target's text.
func (s *Store) UpdateTargetParts(ctx context.Context, orgID, id uuid.UUID, parts []string) error {
	return s.execOne(ctx, `UPDATE post_targets SET parts = $3, updated_at = now() WHERE org_id = $1 AND id = $2 AND status IN ('held', 'queued')`,
		orgID, id, parts)
}

// SetTargetStatus moves a target to status (from any of from, if given).
func (s *Store) SetTargetStatus(ctx context.Context, orgID, id uuid.UUID, status model.TargetStatus, from ...model.TargetStatus) error {
	if len(from) == 0 {
		return s.execOne(ctx, `UPDATE post_targets SET status = $3, lease_owner = NULL, lease_until = NULL, updated_at = now()
			WHERE org_id = $1 AND id = $2`, orgID, id, status)
	}
	fs := make([]string, len(from))
	for i, f := range from {
		fs[i] = string(f)
	}
	return s.execOne(ctx, `UPDATE post_targets SET status = $3, lease_owner = NULL, lease_until = NULL, updated_at = now()
		WHERE org_id = $1 AND id = $2 AND status = ANY($4)`, orgID, id, status, fs)
}

// ReleaseHeldTargets queues a post's held targets (after approval).
func (s *Store) ReleaseHeldTargets(ctx context.Context, orgID, postID uuid.UUID) error {
	_, err := s.q.Exec(ctx, `UPDATE post_targets SET status = 'queued', updated_at = now() WHERE org_id = $1 AND post_id = $2 AND status = 'held'`,
		orgID, postID)
	return err
}

// CancelOpenTargets cancels a post's targets that have not started.
func (s *Store) CancelOpenTargets(ctx context.Context, orgID, postID uuid.UUID) (int, error) {
	tag, err := s.q.Exec(ctx, `UPDATE post_targets SET status = 'canceled', updated_at = now()
		WHERE org_id = $1 AND post_id = $2 AND status IN ('held', 'queued', 'needs_attention')`, orgID, postID)
	return int(tag.RowsAffected()), err
}

// ClaimedTarget is a target leased to a worker, with what it needs.
type ClaimedTarget struct {
	model.Target
	Metadata map[string]string
}

// ClaimDueTargets leases up to limit queued targets that are due, one per
// channel, skipping channels on hold or already publishing (ADR 0011).
func (s *Store) ClaimDueTargets(ctx context.Context, owner string, now, leaseUntil time.Time, limit int) ([]ClaimedTarget, error) {
	rows, err := s.q.Query(ctx, `
		WITH due AS (
			SELECT DISTINCT ON (t.channel_id) t.id
			FROM post_targets t JOIN channels c ON c.id = t.channel_id
			WHERE t.status = 'queued' AND t.next_attempt_at <= $2
			  AND c.status = 'active' AND (c.hold_until IS NULL OR c.hold_until <= $2)
			  AND NOT EXISTS (SELECT 1 FROM post_targets o WHERE o.channel_id = t.channel_id AND o.status = 'publishing')
			ORDER BY t.channel_id, t.next_attempt_at, t.id
		), picked AS (
			SELECT t.id FROM post_targets t JOIN due ON due.id = t.id
			ORDER BY t.next_attempt_at LIMIT $4
			FOR UPDATE OF t SKIP LOCKED
		)
		UPDATE post_targets t SET status = 'publishing', lease_owner = $1, lease_until = $3, attempts = t.attempts + 1, updated_at = $2
		FROM picked WHERE t.id = picked.id AND t.status = 'queued'
		RETURNING t.id`, owner, now, leaseUntil, limit)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	rows, err = s.q.Query(ctx, `SELECT `+targetCols+`, p.metadata FROM post_targets t JOIN channels c ON c.id = t.channel_id
		JOIN posts p ON p.id = t.post_id WHERE t.id = ANY($1) ORDER BY t.next_attempt_at`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ClaimedTarget, error) {
		var ct ClaimedTarget
		var provider string
		t := &ct.Target
		err := r.Scan(&t.ID, &t.OrgID, &t.PostID, &t.ChannelID, &t.Livemode, &provider, &t.Parts, &t.Status, &t.Attempts, &t.NextAttemptAt,
			&t.PublishBy, &t.LeaseOwner, &t.LeaseUntil, &t.Posted, &t.Permalink, &t.ErrorCode, &t.ErrorMessage, &t.PublishedAt,
			&t.CreatedAt, &t.UpdatedAt, &t.ChannelName, &ct.Metadata)
		t.Provider = platform.Provider(provider)
		return ct, err
	})
}

// RecordPostedPart appends a published thread part, while the lease holds.
func (s *Store) RecordPostedPart(ctx context.Context, id uuid.UUID, owner string, ref platform.RemoteRef) error {
	b, err := json.Marshal([]platform.RemoteRef{ref})
	if err != nil {
		return err
	}
	return s.execOne(ctx, `UPDATE post_targets SET posted = posted || $3::jsonb, updated_at = now()
		WHERE id = $1 AND status = 'publishing' AND lease_owner = $2`, id, owner, b)
}

// TargetOutcome is how an attempt ended.
type TargetOutcome struct {
	Status        model.TargetStatus
	NextAttemptAt time.Time
	Permalink     string
	Posted        []platform.RemoteRef
	ErrorCode     string
	ErrorMessage  string
	PublishedAt   *time.Time
}

// FinishTarget records an attempt's outcome, if owner still holds the
// lease. A lost lease is ErrNotFound: someone else owns the target now.
func (s *Store) FinishTarget(ctx context.Context, id uuid.UUID, owner string, o TargetOutcome) error {
	var posted any
	if o.Posted != nil {
		posted = o.Posted
	}
	return s.execOne(ctx, `UPDATE post_targets SET status = $3, next_attempt_at = $4, permalink = COALESCE(NULLIF($5, ''), permalink),
		posted = COALESCE($6::jsonb, posted), error_code = $7, error_message = $8, published_at = COALESCE($9, published_at),
		lease_owner = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 AND status = 'publishing' AND lease_owner = $2`,
		id, owner, o.Status, o.NextAttemptAt, o.Permalink, posted, o.ErrorCode, o.ErrorMessage, o.PublishedAt)
}

// ExpiredLeases returns targets whose worker vanished mid-publish.
func (s *Store) ExpiredLeases(ctx context.Context, now time.Time, limit int) ([]model.Target, error) {
	rows, err := s.q.Query(ctx, `SELECT `+targetCols+` FROM post_targets t JOIN channels c ON c.id = t.channel_id
		WHERE t.status = 'publishing' AND t.lease_until < $1 ORDER BY t.lease_until LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Target, error) { return scanTarget(r) })
}

// ReclaimTarget moves a target with an expired lease to status.
func (s *Store) ReclaimTarget(ctx context.Context, id uuid.UUID, now time.Time, status model.TargetStatus, code, msg string) error {
	return s.execOne(ctx, `UPDATE post_targets SET status = $3, error_code = $4, error_message = $5, next_attempt_at = $2,
		lease_owner = NULL, lease_until = NULL, updated_at = now() WHERE id = $1 AND status = 'publishing' AND lease_until < $2`,
		id, now, status, code, msg)
}

// ExpireOverdueTargets fails queued targets past their deadline.
func (s *Store) ExpireOverdueTargets(ctx context.Context, now time.Time, limit int) ([]model.Target, error) {
	rows, err := s.q.Query(ctx, `UPDATE post_targets t SET status = 'failed', error_code = 'expired',
		error_message = 'Not published before its publish_by deadline.', updated_at = $1
		FROM channels c WHERE c.id = t.channel_id AND t.id IN (
			SELECT id FROM post_targets WHERE status IN ('queued', 'held') AND publish_by < $1 ORDER BY publish_by LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING `+targetCols, now, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Target, error) { return scanTarget(r) })
}

// RequeueTarget puts a target back in the queue (a person chose retry).
func (s *Store) RequeueTarget(ctx context.Context, orgID, id uuid.UUID, at, publishBy time.Time) error {
	return s.execOne(ctx, `UPDATE post_targets SET status = 'queued', next_attempt_at = $3, publish_by = GREATEST(publish_by, $4),
		error_code = '', error_message = '', updated_at = now()
		WHERE org_id = $1 AND id = $2 AND status IN ('needs_attention', 'failed')`, orgID, id, at, publishBy)
}

// MarkTargetPublished records that a person confirmed the post went out.
func (s *Store) MarkTargetPublished(ctx context.Context, orgID, id uuid.UUID, permalink string, at time.Time) error {
	return s.execOne(ctx, `UPDATE post_targets SET status = 'published', permalink = COALESCE(NULLIF($3, ''), permalink), published_at = $4,
		error_code = '', error_message = '', updated_at = now() WHERE org_id = $1 AND id = $2 AND status = 'needs_attention'`, orgID, id, permalink, at)
}

// StartAttempt and FinishAttempt keep the attempt history.
func (s *Store) StartAttempt(ctx context.Context, a *model.Attempt) error {
	_, err := s.q.Exec(ctx, `INSERT INTO publish_attempts (id, org_id, target_id, attempt, started_at) VALUES ($1, $2, $3, $4, $5)`,
		a.ID, a.OrgID, a.TargetID, a.Attempt, a.StartedAt)
	return mapErr(err)
}

func (s *Store) FinishAttempt(ctx context.Context, id uuid.UUID, at time.Time, outcome, code, msg string) error {
	_, err := s.q.Exec(ctx, `UPDATE publish_attempts SET finished_at = $2, outcome = $3, error_code = $4, error = $5 WHERE id = $1`,
		id, at, outcome, code, msg)
	return err
}

func (s *Store) Attempts(ctx context.Context, orgID, targetID uuid.UUID) ([]model.Attempt, error) {
	rows, err := s.q.Query(ctx, `SELECT id, org_id, target_id, attempt, started_at, finished_at, outcome, error_code, error
		FROM publish_attempts WHERE org_id = $1 AND target_id = $2 ORDER BY started_at`, orgID, targetID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Attempt, error) {
		var a model.Attempt
		err := r.Scan(&a.ID, &a.OrgID, &a.TargetID, &a.Attempt, &a.StartedAt, &a.FinishedAt, &a.Outcome, &a.ErrorCode, &a.Error)
		return a, err
	})
}

// QueueStats summarizes the queue for health pages.
type QueueStats struct {
	Due            int
	Publishing     int
	NeedsAttention int
	OldestDue      *time.Time
}

func (s *Store) QueueStats(ctx context.Context, orgID uuid.UUID, livemode bool, now time.Time) (QueueStats, error) {
	var q QueueStats
	err := s.q.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE status = 'queued' AND next_attempt_at <= $3),
		count(*) FILTER (WHERE status = 'publishing'),
		count(*) FILTER (WHERE status = 'needs_attention'),
		min(next_attempt_at) FILTER (WHERE status = 'queued' AND next_attempt_at <= $3)
		FROM post_targets WHERE org_id = $1 AND livemode = $2`, orgID, livemode, now).Scan(&q.Due, &q.Publishing, &q.NeedsAttention, &q.OldestDue)
	return q, err
}

// TargetCount is how many targets share a status and mode. For queued
// targets it also counts those that are due on a channel able to publish,
// and when the oldest of them came due.
type TargetCount struct {
	Status    model.TargetStatus
	Livemode  bool
	Count     int
	Due       int
	OldestDue *time.Time
}

// TargetCounts counts targets by status and mode in one round trip, across
// every org when orgID is nil (metrics, ADR 0014) or in one org. Due
// targets are those the publisher would claim now: queued, past their next
// attempt, on an active channel not held by a rate limit.
func (s *Store) TargetCounts(ctx context.Context, orgID *uuid.UUID, now time.Time) ([]TargetCount, error) {
	rows, err := s.q.Query(ctx, `
		WITH counts AS (
			SELECT status, livemode, count(*) AS n FROM post_targets
			WHERE ($1::uuid IS NULL OR org_id = $1) GROUP BY status, livemode
		), due AS (
			SELECT t.livemode, count(*) AS n, min(t.next_attempt_at) AS oldest
			FROM post_targets t JOIN channels c ON c.id = t.channel_id
			WHERE t.status = 'queued' AND t.next_attempt_at <= $2 AND ($1::uuid IS NULL OR t.org_id = $1)
			  AND c.status = 'active' AND (c.hold_until IS NULL OR c.hold_until <= $2)
			GROUP BY t.livemode
		)
		SELECT c.status, c.livemode, c.n, COALESCE(d.n, 0), d.oldest
		FROM counts c LEFT JOIN due d ON c.status = 'queued' AND d.livemode = c.livemode
		ORDER BY c.status, c.livemode`, orgID, now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TargetCount, error) {
		var c TargetCount
		err := r.Scan(&c.Status, &c.Livemode, &c.Count, &c.Due, &c.OldestDue)
		return c, err
	})
}
