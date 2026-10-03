// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Web analytics (ADR 0025).

func (h *Handler) listAnalyticsProviders(w http.ResponseWriter, r *http.Request) error {
	providers := h.svc.AnalyticsProviders(actor(r).Livemode)
	out := make([]core.AnalyticsProviderView, 0, len(providers))
	for _, p := range providers {
		out = append(out, core.ViewAnalyticsProvider(p))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/analytics_providers"})
	return nil
}

func (h *Handler) listAnalyticsSources(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var brandID *uuid.UUID
	if ref := r.URL.Query().Get("brand"); ref != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, ref)
		if err != nil {
			return err
		}
		brandID = &b.ID
	}
	sources, err := h.svc.AnalyticsSources(r.Context(), a, brandID)
	if err != nil {
		return err
	}
	out := make([]core.AnalyticsSourceView, 0, len(sources))
	for _, s := range sources {
		out = append(out, core.ViewAnalyticsSource(s))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/analytics_sources"})
	return nil
}

func (h *Handler) createAnalyticsSource(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var body struct {
		Brand    string            `json:"brand"`
		Provider string            `json:"provider"`
		Goals    []string          `json:"goals"`
		Fields   map[string]string `json:"fields"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	b, err := h.svc.ResolveBrand(r.Context(), a, body.Brand)
	if err != nil {
		return err
	}
	src, err := h.svc.ConnectAnalyticsSource(r.Context(), a, core.AnalyticsSourceInput{BrandID: b.ID, Provider: analytics.Provider(body.Provider),
		Goals: body.Goals, Fields: body.Fields})
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, core.ViewAnalyticsSource(src))
	return nil
}

func (h *Handler) getAnalyticsSource(w http.ResponseWriter, r *http.Request) error {
	sid, err := pathID(r, id.AnalyticsSource, "analytics source")
	if err != nil {
		return err
	}
	src, err := h.svc.AnalyticsSource(r.Context(), actor(r), sid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewAnalyticsSource(src))
	return nil
}

func (h *Handler) deleteAnalyticsSource(w http.ResponseWriter, r *http.Request) error {
	sid, err := pathID(r, id.AnalyticsSource, "analytics source")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteAnalyticsSource(r.Context(), actor(r), sid); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: r.PathValue("id"), Object: "analytics_source", Deleted: true})
	return nil
}

func (h *Handler) analyticsSummary(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	q := r.URL.Query()
	f := core.AnalyticsFilter{GroupBy: store.AnalyticsGroup(q.Get("group_by"))}
	for name, dst := range map[string]*time.Time{"since": &f.Since, "until": &f.Until} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.DateOnly, v)
			if err != nil {
				return badRequest("parameter_invalid", name, "%s must be a date (YYYY-MM-DD).", name)
			}
			*dst = t
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return badRequest("parameter_invalid", "limit", "limit must be between 1 and 100.")
		}
		f.Limit = n
	}
	if ref := q.Get("brand"); ref != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, ref)
		if err != nil {
			return err
		}
		f.BrandID = &b.ID
	}
	sum, err := h.svc.AnalyticsSummary(r.Context(), a, f)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewAnalyticsSummary(sum))
	return nil
}
