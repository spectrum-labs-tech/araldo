// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// Administration by API key (ADR 0019): keys managing keys, approval and
// the audit log, each behind an explicit-only scope.

// defaultRollOverlap is how long a rolled key's old secret keeps working.
const defaultRollOverlap = 24 * time.Hour

// me tells a credential who it is (ADR 0028), whatever its scopes.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	v, err := h.svc.Me(r.Context(), actor(r))
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, v)
	return nil
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) error {
	keys, err := h.svc.APIKeys(r.Context(), actor(r))
	if err != nil {
		return err
	}
	out := make([]core.APIKeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, core.ViewAPIKey(k))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/api_keys"})
	return nil
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var body struct {
		Name      string     `json:"name"`
		Scopes    []string   `json:"scopes"`
		Brand     string     `json:"brand"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	in := core.APIKeyInput{Name: body.Name, Scopes: body.Scopes, Expires: body.ExpiresAt}
	if body.Brand != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, body.Brand)
		if err != nil {
			return err
		}
		in.BrandID = &b.ID
	}
	plain, k, err := h.svc.CreateKeyWithKey(r.Context(), a, in)
	if err != nil {
		return err
	}
	v := core.ViewAPIKey(k)
	v.Secret = plain
	ok(w, http.StatusCreated, v)
	return nil
}

// rollBody is a roll's options: how long the old secret keeps working.
type rollBody struct {
	OverlapHours *int `json:"overlap_hours"`
}

func (b rollBody) overlap() (time.Duration, error) {
	if b.OverlapHours == nil {
		return defaultRollOverlap, nil
	}
	if *b.OverlapHours < 0 || *b.OverlapHours > 168 {
		return 0, badRequest("parameter_invalid", "overlap_hours", "overlap_hours is between 0 and 168.")
	}
	return time.Duration(*b.OverlapHours) * time.Hour, nil
}

func (h *Handler) rollKey(w http.ResponseWriter, r *http.Request) error {
	kid, err := pathID(r, id.APIKey, "API key")
	if err != nil {
		return err
	}
	var body rollBody
	if err := decode(r, &body); err != nil {
		return err
	}
	overlap, err := body.overlap()
	if err != nil {
		return err
	}
	plain, k, err := h.svc.RollAPIKey(r.Context(), actor(r), nil, kid, overlap)
	if err != nil {
		return err
	}
	v := core.ViewAPIKey(k)
	v.Secret = plain
	ok(w, http.StatusOK, v)
	return nil
}

func (h *Handler) rollOwnKey(w http.ResponseWriter, r *http.Request) error {
	var body rollBody
	if err := decode(r, &body); err != nil {
		return err
	}
	overlap, err := body.overlap()
	if err != nil {
		return err
	}
	plain, k, err := h.svc.RollOwnKey(r.Context(), actor(r), overlap)
	if err != nil {
		return err
	}
	v := core.ViewAPIKey(k)
	v.Secret = plain
	ok(w, http.StatusOK, v)
	return nil
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request) error {
	kid, err := pathID(r, id.APIKey, "API key")
	if err != nil {
		return err
	}
	if err := h.svc.RevokeAPIKey(r.Context(), actor(r), kid); err != nil {
		return err
	}
	keys, err := h.svc.APIKeys(r.Context(), actor(r))
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.ID == kid {
			ok(w, http.StatusOK, core.ViewAPIKey(k))
			return nil
		}
	}
	// A key that revoked itself can no longer list keys; say what happened.
	ok(w, http.StatusOK, deleted{ID: r.PathValue("id"), Object: "api_key", Deleted: true})
	return nil
}

func (h *Handler) reviewPost(approve bool) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		pid, err := pathID(r, id.Post, "post")
		if err != nil {
			return err
		}
		var body struct {
			Note string `json:"note"`
		}
		if err := decode(r, &body); err != nil {
			return err
		}
		p, err := h.svc.ReviewPost(r.Context(), actor(r), pid, approve, body.Note)
		if err != nil {
			return err
		}
		ok(w, http.StatusOK, core.ViewPost(p))
		return nil
	}
}

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) error {
	pg, err := page(r, id.AuditEvent)
	if err != nil {
		return err
	}
	events, more, err := h.svc.AuditEvents(r.Context(), actor(r), pg)
	if err != nil {
		return err
	}
	out := make([]core.AuditEventView, 0, len(events))
	for i := range events {
		out = append(out, core.ViewAuditEvent(&events[i]))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, HasMore: more, URL: "/v1/audit_events"})
	return nil
}
