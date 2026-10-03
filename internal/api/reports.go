// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
)

// Reports (ADR 0026).

func (h *Handler) report(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	q := r.URL.Query()
	b, err := h.svc.ResolveBrand(r.Context(), a, q.Get("brand"))
	if err != nil {
		return err
	}
	in := core.ReportInput{BrandID: b.ID, Month: q.Get("month")}
	for name, dst := range map[string]*time.Time{"since": &in.Since, "until": &in.Until} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.DateOnly, v)
			if err != nil {
				return badRequest("parameter_invalid", name, "%s must be a date (YYYY-MM-DD).", name)
			}
			*dst = t
		}
	}
	rep, err := h.svc.BrandReport(r.Context(), a, in)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewReport(rep))
	return nil
}
