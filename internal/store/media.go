// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

const mediaCols = `m.id, m.org_id, m.brand_id, m.livemode, m.content_type, m.size_bytes, m.width, m.height, m.sha256, m.alt, m.filename,
	m.storage, m.storage_key, m.created_by_user, m.created_by_key, m.created_at, m.transparent`

// scanMedia reads mediaCols, then any extra columns into extra.
func scanMedia(r pgx.Row, extra ...any) (*model.Media, error) {
	var m model.Media
	dest := append([]any{&m.ID, &m.OrgID, &m.BrandID, &m.Livemode, &m.ContentType, &m.Size, &m.Width, &m.Height, &m.SHA256, &m.Alt,
		&m.Filename, &m.Storage, &m.StorageKey, &m.CreatedByUser, &m.CreatedByKey, &m.CreatedAt, &m.Transparent}, extra...)
	if err := r.Scan(dest...); err != nil {
		return nil, mapErr(err)
	}
	return &m, nil
}

// CreateMedia inserts a media row, with its file when it is stored in
// Postgres (data is ignored otherwise).
func (s *Store) CreateMedia(ctx context.Context, m *model.Media, data []byte) error {
	_, err := s.q.Exec(ctx, `INSERT INTO media (id, org_id, brand_id, livemode, content_type, size_bytes, width, height, sha256, alt, filename,
		storage, storage_key, created_by_user, created_by_key, transparent) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		m.ID, m.OrgID, m.BrandID, m.Livemode, m.ContentType, m.Size, m.Width, m.Height, m.SHA256, m.Alt, m.Filename,
		m.Storage, m.StorageKey, m.CreatedByUser, m.CreatedByKey, m.Transparent)
	if err != nil {
		return mapErr(err)
	}
	if m.Storage == model.StoragePostgres {
		if _, err := s.q.Exec(ctx, `INSERT INTO media_blobs (media_id, data) VALUES ($1, $2)`, m.ID, data); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// Media returns one media row.
func (s *Store) Media(ctx context.Context, orgID, id uuid.UUID) (*model.Media, error) {
	return scanMedia(s.q.QueryRow(ctx, `SELECT `+mediaCols+` FROM media m WHERE m.org_id = $1 AND m.id = $2`, orgID, id))
}

// MediaAnyOrg reads media by ID alone, for a signed link, whose signature
// already proves which file it is.
func (s *Store) MediaAnyOrg(ctx context.Context, id uuid.UUID) (*model.Media, error) {
	return scanMedia(s.q.QueryRow(ctx, `SELECT `+mediaCols+` FROM media m WHERE m.id = $1`, id))
}

// MediaByIDs returns the org's media with these IDs, in no order.
func (s *Store) MediaByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]*model.Media, error) {
	rows, err := s.q.Query(ctx, `SELECT `+mediaCols+` FROM media m WHERE m.org_id = $1 AND m.id = ANY($2)`, orgID, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Media, error) { return scanMedia(r) })
}

// MediaFilter narrows a media listing.
type MediaFilter struct {
	BrandID *uuid.UUID
}

// MediaList lists media newest first.
func (s *Store) MediaList(ctx context.Context, orgID uuid.UUID, livemode bool, f MediaFilter, page Page) ([]*model.Media, bool, error) {
	where, order, extra := pageClause(page, "m.id", 4)
	args := append([]any{orgID, livemode, f.BrandID}, extra...)
	rows, err := s.q.Query(ctx, `SELECT `+mediaCols+` FROM media m WHERE m.org_id = $1 AND m.livemode = $2
		AND ($3::uuid IS NULL OR m.brand_id = $3)`+where+order, args...)
	if err != nil {
		return nil, false, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Media, error) { return scanMedia(r) })
	if err != nil {
		return nil, false, err
	}
	list, more := trimPage(page, list)
	return list, more, nil
}

// MediaBlob returns a file stored in Postgres.
func (s *Store) MediaBlob(ctx context.Context, mediaID uuid.UUID) ([]byte, error) {
	var data []byte
	err := s.q.QueryRow(ctx, `SELECT data FROM media_blobs WHERE media_id = $1`, mediaID).Scan(&data)
	return data, mapErr(err)
}

// SetMediaAlt changes a media item's alt text.
func (s *Store) SetMediaAlt(ctx context.Context, orgID, id uuid.UUID, alt string) error {
	return s.execOne(ctx, `UPDATE media SET alt = $3 WHERE org_id = $1 AND id = $2`, orgID, id, alt)
}

// DeleteMedia deletes a media row (and its Postgres file). It fails with
// ErrReferenced while a post, a newsletter issue or a brand's email theme
// uses it.
func (s *Store) DeleteMedia(ctx context.Context, orgID, id uuid.UUID) error {
	return s.execOne(ctx, `DELETE FROM media WHERE org_id = $1 AND id = $2`, orgID, id)
}

// UnusedMedia lists media created before cutoff that no post, issue or
// email theme uses, oldest first, in one org or (orgID nil) every org, for
// pruning.
func (s *Store) UnusedMedia(ctx context.Context, orgID *uuid.UUID, cutoff time.Time, limit int) ([]*model.Media, error) {
	rows, err := s.q.Query(ctx, `SELECT `+mediaCols+` FROM media m WHERE m.created_at < $1 AND ($3::uuid IS NULL OR m.org_id = $3)
		AND NOT EXISTS (SELECT 1 FROM post_media pm WHERE pm.media_id = m.id)
		AND NOT EXISTS (SELECT 1 FROM newsletter_media nm WHERE nm.media_id = m.id)
		AND NOT EXISTS (SELECT 1 FROM brands b WHERE b.email_logo_media_id = m.id) ORDER BY m.id LIMIT $2`, cutoff, limit, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*model.Media, error) { return scanMedia(r) })
}

// attachMedia loads each post's media, in order.
func (s *Store) attachMedia(ctx context.Context, orgID uuid.UUID, posts []*model.Post) error {
	if len(posts) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(posts))
	byID := map[uuid.UUID]*model.Post{}
	for i, p := range posts {
		ids[i] = p.ID
		byID[p.ID] = p
	}
	rows, err := s.q.Query(ctx, `SELECT `+mediaCols+`, pm.post_id FROM post_media pm JOIN media m ON m.id = pm.media_id
		WHERE pm.org_id = $1 AND pm.post_id = ANY($2) ORDER BY pm.post_id, pm.position`, orgID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var postID uuid.UUID
		m, err := scanMedia(rows, &postID)
		if err != nil {
			return err
		}
		byID[postID].Media = append(byID[postID].Media, m)
	}
	return rows.Err()
}
