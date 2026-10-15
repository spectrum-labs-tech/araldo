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
		var fault bool
		if status, v.Error, fault = s.serverFault(w, r, "unsubscribing", err); !fault {
			status, v.Error = http.StatusNotFound, apperr.As(err).Message
		}
	}
	s.render(w, status, "unsubscribe", v)
}
