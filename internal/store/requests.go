// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// The request log (ADR 0032).

// InsertAPIRequests stores a batch of logged requests in one statement,
// skipping those of orgs deleted meanwhile.
func (s *Store) InsertAPIRequests(ctx context.Context, rs []model.APIRequest) error {
	n := len(rs)
	ids, orgs := make([]uuid.UUID, n), make([]uuid.UUID, n)
	keys, tokens := make([]pgtype.UUID, n), make([]pgtype.UUID, n)
	live := make([]bool, n)
	methods, routes, paths, codes, reqIDs := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	statuses, durations := make([]int32, n), make([]int32, n)
	at := make([]time.Time, n)
	opt := func(u *uuid.UUID) pgtype.UUID {
		if u == nil {
			return pgtype.UUID{}
		}
		return pgtype.UUID{Bytes: *u, Valid: true}
	}
	for i, r := range rs {
		ids[i], orgs[i], live[i], methods[i], routes[i], paths[i] = r.ID, r.OrgID, r.Livemode, r.Method, r.Route, r.Path
		statuses[i], codes[i], durations[i] = int32(r.Status), r.ErrorCode, int32(min(r.DurationMS, 1<<31-1)) //nolint:gosec // G115: statuses and capped durations fit
		keys[i], tokens[i], reqIDs[i], at[i] = opt(r.KeyID), opt(r.UserTokenID), r.RequestID, r.CreatedAt
	}
	_, err := s.q.Exec(ctx, `INSERT INTO api_requests (id, org_id, livemode, method, route, path, status, error_code, duration_ms, key_id,
		user_token_id, request_id, created_at)
		SELECT u.* FROM unnest($1::uuid[], $2::uuid[], $3::boolean[], $4::text[], $5::text[], $6::text[], $7::integer[], $8::text[],
			$9::integer[], $10::uuid[], $11::uuid[], $12::text[], $13::timestamptz[])
			AS u (id, org_id, livemode, method, route, path, status, error_code, duration_ms, key_id, user_token_id, request_id, created_at)
		WHERE EXISTS (SELECT 1 FROM orgs WHERE orgs.id = u.org_id)`,
		ids, orgs, live, methods, routes, paths, statuses, codes, durations, keys, tokens, reqIDs, at)
	return mapErr(err)
}

// RequestFilter narrows the request log: by status class (2, 4 or 5 for
// 2xx, 4xx, 5xx; 0 for all) and credential.
type RequestFilter struct {
	StatusClass int
	KeyID       *uuid.UUID
}

const requestCols = `id, org_id, livemode, method, route, path, status, error_code, duration_ms, key_id, user_token_id, request_id, created_at`

// APIRequests lists an org's logged requests in a mode, newest first.
func (s *Store) APIRequests(ctx context.Context, orgID uuid.UUID, livemode bool, f RequestFilter, p Page) ([]model.APIRequest, bool, error) {
	where, order, args := pageClause(p, "id", 5)
	rows, err := s.q.Query(ctx, `SELECT `+requestCols+` FROM api_requests WHERE org_id = $1 AND livemode = $2
		AND ($3 = 0 OR status / 100 = $3) AND ($4::uuid IS NULL OR key_id = $4)`+where+order,
		append([]any{orgID, livemode, f.StatusClass, f.KeyID}, args...)...)
	if err != nil {
		return nil, false, err
	}
	rs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.APIRequest, error) {
		var q model.APIRequest
		err := r.Scan(&q.ID, &q.OrgID, &q.Livemode, &q.Method, &q.Route, &q.Path, &q.Status, &q.ErrorCode, &q.DurationMS, &q.KeyID,
			&q.UserTokenID, &q.RequestID, &q.CreatedAt)
		return q, err
	})
	if err != nil {
		return nil, false, err
	}
	rs, more := trimPage(p, rs)
	return rs, more, nil
}

// PruneAPIRequests deletes up to limit logged requests made before before,
// oldest first; IDs are time-ordered, so the key finds them.
func (s *Store) PruneAPIRequests(ctx context.Context, before time.Time, limit int) (int, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM api_requests WHERE id IN (SELECT id FROM api_requests WHERE id < $1 ORDER BY id LIMIT $2)`,
		id.Before(before), limit)
	return int(tag.RowsAffected()), err
}
