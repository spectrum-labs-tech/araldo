// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Passkeys (ADR 0007). A ceremony is two JSON requests from the page's
// script: options for the browser, then the browser's answer. They take
// application/json only, which a page on another site cannot send without
// asking first (a CORS preflight, which is refused), and those made from a
// signed-in session also carry its CSRF token in a header.

// passkeyReply is the second request's body.
type passkeyReply struct {
	Token    string          `json:"token"`
	Response json.RawMessage `json:"response"`
	Name     string          `json:"name"`
	Next     string          `json:"next"`
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		return apperr.Invalid("content_type", "", "Send JSON.")
	}
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// jsonFail answers a ceremony's error for the page to show.
func (s *Server) jsonFail(w http.ResponseWriter, r *http.Request, err error) {
	ae := apperr.As(err)
	status := http.StatusBadRequest
	switch ae.Kind {
	case apperr.KindInternal, apperr.KindUnavailable:
		s.log.ErrorContext(r.Context(), "passkey ceremony failed", "err", err)
		status = http.StatusInternalServerError
	case apperr.KindUnauthorized:
		status = http.StatusUnauthorized
	case apperr.KindForbidden:
		status = http.StatusForbidden
	case apperr.KindRateLimited:
		status = http.StatusTooManyRequests
	}
	out := map[string]string{"error": ae.Message, "code": ae.Code}
	if ae.Code == "reauthentication_required" {
		out["confirm"] = "/confirm?next=" + url.QueryEscape("/account")
	}
	writeJSON(w, status, out)
}

// sessionJSON is a signed-in session for a JSON request, with its CSRF
// token checked.
func (s *Server) sessionJSON(w http.ResponseWriter, r *http.Request) (*model.Session, bool) {
	ck, err := r.Cookie(s.sessionCookie())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Sign in again."})
		return nil, false
	}
	ss, _, err := s.svc.Session(r.Context(), ck.Value)
	if err != nil || ss.State != model.SessionActive || r.Header.Get("X-CSRF-Token") != ss.CSRFToken {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "This page expired. Reload it and try again."})
		return nil, false
	}
	return ss, true
}

// pendingJSON is a session waiting for its second factor.
func (s *Server) pendingJSON(w http.ResponseWriter, r *http.Request) (*model.Session, bool) {
	ck, err := r.Cookie(s.sessionCookie())
	if err == nil {
		if ss, _, err := s.svc.Session(r.Context(), ck.Value); err == nil && ss.State == model.SessionPendingMFA {
			return ss, true
		}
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Your sign-in expired; start again."})
	return nil, false
}

func options(w http.ResponseWriter, opts json.RawMessage, token string) {
	writeJSON(w, http.StatusOK, map[string]any{"options": opts, "token": token})
}

// Signing in with a passkey alone.

func (s *Server) passkeyLoginOptions(w http.ResponseWriter, r *http.Request) {
	if !s.login.allow(s.clientIP(r), time.Now()) {
		s.jsonFail(w, r, &apperr.Error{Kind: apperr.KindRateLimited, Message: "Too many sign-in attempts from your network. Wait a minute."})
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		s.jsonFail(w, r, apperr.Invalid("content_type", "", "Send JSON."))
		return
	}
	opts, token, err := s.svc.BeginPasskeyLogin(r.Context())
	if err != nil {
		s.jsonFail(w, r, err)
		return
	}
	options(w, opts, token)
}

func (s *Server) passkeyLogin(w http.ResponseWriter, r *http.Request) {
	var in passkeyReply
	if err := readJSON(w, r, &in); err != nil {
		s.jsonFail(w, r, err)
		return
	}
	res, err := s.svc.FinishPasskeyLogin(r.Context(), in.Token, in.Response, r.UserAgent(), s.clientIP(r))
	if err != nil {
		s.jsonFail(w, r, err)
		return
	}
	s.setSessionCookie(w, res.Token, res.Session.ExpiresAt.Add(core.SessionAbsolute))
	writeJSON(w, http.StatusOK, map[string]string{"next": safeNext(in.Next)})
}

// A passkey as the second factor after a password.

func (s *Server) mfaPasskeyOptions(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.pendingJSON(w, r)
	if !ok {
		return
	}
	opts, token, err := s.svc.BeginPasskeyAssertion(r.Context(), ss)
	if err != nil {
		s.jsonFail(w, r, err)
		return
	}
	options(w, opts, token)
}

func (s *Server) mfaPasskey(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.pendingJSON(w, r)
	if !ok {
		return
	}
	var in passkeyReply
	if err := readJSON(w, r, &in); err != nil {
		s.jsonFail(w, r, err)
		return
	}
	if err := s.svc.FinishPasskeyAssertion(r.Context(), ss, in.Token, in.Response); err != nil {
		s.jsonFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"next": safeNext(in.Next)})
}

// Confirming it is you (sudo) with a passkey.

func (s *Server) confirmPasskeyOptions(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sessionJSON(w, r)
	if !ok {
		return
	}
	opts, token, err := s.svc.BeginPasskeyAssertion(r.Context(), ss)
	if err != nil {
		s.jsonFail(w, r, err)
		return
	}
	options(w, opts, token)
}

func (s *Server) confirmPasskey(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sessionJSON(w, r)
	if !ok {
		return
	}
	var in passkeyReply
	if err := readJSON(w, r, &in); err != nil {
		s.jsonFail(w, r, err)
		return
	}
	if err := s.svc.FinishPasskeyAssertion(r.Context(), ss, in.Token, in.Response); err != nil {
		s.jsonFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"next": safeNext(in.Next)})
}

// Adding and removing passkeys, from the account page.

func (s *Server) addPasskeyOptions(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sessionJSON(w, r)
	if !ok {
		return
	}
	opts, token, err := s.svc.BeginPasskeyRegistration(r.Context(), ss)
	if err != nil {
		s.jsonFail(w, r, err)
		return
	}
	options(w, opts, token)
}

func (s *Server) addPasskey(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sessionJSON(w, r)
	if !ok {
		return
	}
	var in passkeyReply
	if err := readJSON(w, r, &in); err != nil {
		s.jsonFail(w, r, err)
		return
	}
	p, err := s.svc.FinishPasskeyRegistration(r.Context(), ss, in.Token, in.Name, in.Response)
	if err != nil {
		s.jsonFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"next": "/account?notice=" + url.QueryEscape("Added the passkey "+p.Name+".")})
}

func (s *Server) deletePasskey(c *reqCtx) error {
	pid, err := uuid.Parse(c.r.PathValue("id"))
	if err != nil {
		return apperr.NotFound("passkey")
	}
	if err := s.svc.DeletePasskey(c.ctx(), c.session, pid); err != nil {
		if apperr.As(err).Code == "reauthentication_required" {
			return redirect(c, "/confirm?next=/account", "Confirm it's you to remove a passkey.")
		}
		return err
	}
	return redirect(c, "/account", "Passkey removed.")
}
