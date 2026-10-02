// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Engagement (ADR 0018).

type readingView struct {
	Object string    `json:"object"`
	ReadAt time.Time `json:"read_at"`
	platform.Counts
	Total int64 `json:"total"`
}

func (h *Handler) listEngagement(w http.ResponseWriter, r *http.Request) error {
	tid, err := pathID(r, id.Target, "post target")
	if err != nil {
		return err
	}
	readings, err := h.svc.EngagementReadings(r.Context(), actor(r), tid)
	if err != nil {
		return err
	}
	out := make([]readingView, 0, len(readings))
	for _, e := range readings {
		out = append(out, readingView{Object: "engagement_reading", ReadAt: e.ReadAt.UTC(), Counts: e.Counts, Total: e.Total()})
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/post_targets/" + r.PathValue("id") + "/engagement"})
	return nil
}

type summaryRowView struct {
	ID       string `json:"id,omitempty"`
	Label    string `json:"label"`
	Provider string `json:"provider,omitempty"`
	Posts    int    `json:"posts"`
	Targets  int    `json:"targets"`
	platform.Counts
	Total int64 `json:"total"`
}

func (h *Handler) engagementSummary(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	q := r.URL.Query()
	f := core.EngagementFilter{GroupBy: store.EngagementGroup(q.Get("group_by"))}
	for name, dst := range map[string]*time.Time{"since": &f.Since, "until": &f.Until} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return badRequest("parameter_invalid", name, "%s must be an RFC 3339 time.", name)
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
	rows, err := h.svc.EngagementSummary(r.Context(), a, f)
	if err != nil {
		return err
	}
	group := f.GroupBy
	if group == "" {
		group = store.GroupByPost
	}
	out := make([]summaryRowView, 0, len(rows))
	for _, row := range rows {
		out = append(out, summaryRowView{ID: core.EngagementRowID(group, row.ID), Label: row.Label, Provider: row.Provider,
			Posts: row.Posts, Targets: row.Targets, Counts: row.Counts, Total: row.Total()})
	}
	ok(w, http.StatusOK, map[string]any{"object": "engagement_summary", "group_by": group, "data": out})
	return nil
}
