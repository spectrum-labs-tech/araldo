// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Denials (ADR 0004 decision 6): a refused action is audited like a
// change, so a key probing for scopes it lacks, or a member trying what
// their role does not allow, shows in the log.

// denialEvery is how often one actor's denials of one operation are
// recorded: a client retrying in a loop is one entry a minute, not
// thousands.
const denialEvery = time.Minute

// denials remembers when each actor's denial of an operation was last
// recorded.
type denials struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (d *denials) due(key string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.last == nil {
		d.last = map[string]time.Time{}
	}
	if t, ok := d.last[key]; ok && now.Sub(t) < denialEvery {
		return false
	}
	d.last[key] = now
	if len(d.last) > 10000 { // forget the old, so it cannot grow without bound
		for k, t := range d.last {
			if now.Sub(t) >= denialEvery {
				delete(d.last, k)
			}
		}
	}
	return true
}

// RecordDenial audits an actor being refused an operation (such as
// "POST /v1/api_keys"), with the refusal's code. It never fails the
// request: a denial that cannot be recorded is logged instead.
func (s *Service) RecordDenial(ctx context.Context, a Actor, operation, code string) {
	if a.OrgID == uuid.Nil || a.Operator {
		return
	}
	who := "-"
	switch {
	case a.KeyID != nil:
		who = a.KeyID.String()
	case a.UserID != nil:
		who = a.UserID.String()
	}
	if !s.denied.due(a.OrgID.String()+"|"+who+"|"+operation, s.Now()) {
		return
	}
	org := a.OrgID
	e := &model.AuditEvent{ID: id.New(), OrgID: &org, ActorUser: a.UserID, ActorKey: a.KeyID, Action: "access.denied",
		Target: operation, Outcome: "denied", RequestID: a.RequestID, Detail: map[string]any{"code": code}}
	if a.TokenID != nil {
		e.Detail["user_token"] = id.Format(id.UserToken, *a.TokenID)
	}
	if err := s.store.RecordAudit(context.WithoutCancel(ctx), e); err != nil {
		s.log.WarnContext(ctx, "recording a denial failed", "operation", operation, "err", err)
	}
}
