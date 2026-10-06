// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"net/http"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
)

// Unsubscribe links in notification emails (ADR 0034). Opening one asks
// first, so a mail scanner following links turns nothing off; posting it,
// as mail apps do for one-click unsubscribe (RFC 8058), turns that type's
// email off. Neither needs a session: the link is signed.

type unsubscribeData struct {
	Token  string
	Target *core.UnsubscribeTarget
	Done   bool
}

func (s *Server) unsubscribePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	token := r.PathValue("token")
	u, err := s.svc.CheckUnsubscribe(r.Context(), token)
	s.unsubscribeResult(w, r, unsubscribeData{Token: token, Target: u}, err)
}

func (s *Server) unsubscribeSubmit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	token := r.PathValue("token")
	u, err := s.svc.Unsubscribe(r.Context(), token)
	s.unsubscribeResult(w, r, unsubscribeData{Token: token, Target: u, Done: err == nil}, err)
}

func (s *Server) unsubscribeResult(w http.ResponseWriter, r *http.Request, d unsubscribeData, err error) {
	status := http.StatusOK
	v := s.view(nil, "", "Email settings", d)
	if err != nil {
		ae := apperr.As(err)
		status = http.StatusNotFound
		if ae.Kind != apperr.KindNotFound {
			s.log.ErrorContext(r.Context(), "unsubscribing", "err", err)
			status = http.StatusInternalServerError
		}
		v.Error = ae.Message
		if status == http.StatusInternalServerError {
			v.Error = "Something went wrong on our side. Try again in a moment."
		}
	}
	s.render(w, status, "unsubscribe", v)
}
