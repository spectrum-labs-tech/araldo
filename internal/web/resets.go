// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
)

// Password resets by email (ADR 0034).

// resetSendTimeout bounds sending a reset link, apart from the request.
const resetSendTimeout = time.Minute

type forgotData struct {
	Email string
	Sent  bool
}

func (s *Server) forgotPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "login_forgot", s.view(nil, "", "Reset your password", forgotData{Email: r.URL.Query().Get("email")}))
}

// forgotSubmit answers the same whether or not the account exists, and at
// once: the link is sent apart from the request, so neither the answer nor
// its time says whether there is an account.
func (s *Server) forgotSubmit(w http.ResponseWriter, r *http.Request) {
	data := forgotData{Email: r.PostFormValue("email")}
	fail := func(status int, msg string) {
		v := s.view(nil, "", "Reset your password", data)
		v.Error = msg
		s.render(w, status, "login_forgot", v)
	}
	if !s.svc.MailEnabled() {
		fail(http.StatusServiceUnavailable, "This server does not send email. Ask an owner of your org, or the server's operator, to reset your password.")
		return
	}
	if !s.login.allow("reset:"+s.clientIP(r), time.Now()) {
		fail(http.StatusTooManyRequests, "Too many attempts from your network. Wait a minute and try again.")
		return
	}
	ip := s.clientIP(r)
	ctx := context.WithoutCancel(r.Context())
	go func() {
		ctx, cancel := context.WithTimeout(ctx, resetSendTimeout)
		defer cancel()
		if err := s.svc.RequestPasswordReset(ctx, data.Email, ip); err != nil && apperr.As(err).Code != "email_invalid" {
			s.log.WarnContext(ctx, "sending a password reset link", "err", err)
		}
	}()
	data.Sent = true
	s.render(w, http.StatusOK, "login_forgot", s.view(nil, "", "Check your email", data))
}

type resetData struct {
	Token string
	Valid bool
}

func (s *Server) resetPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	token := r.PathValue("token")
	err := s.svc.CheckPasswordReset(r.Context(), token)
	if err != nil && apperr.As(err).Kind != apperr.KindNotFound {
		s.log.ErrorContext(r.Context(), "checking a reset link", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusNotFound
	}
	s.render(w, status, "login_reset", s.view(nil, "", "Choose a new password", resetData{Token: token, Valid: err == nil}))
}

func (s *Server) resetSubmit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	token := r.PathValue("token")
	fail := func(status int, msg string, valid bool) {
		v := s.view(nil, "", "Choose a new password", resetData{Token: token, Valid: valid})
		v.Error = msg
		s.render(w, status, "login_reset", v)
	}
	if !s.login.allow("reset:"+s.clientIP(r), time.Now()) {
		fail(http.StatusTooManyRequests, "Too many attempts from your network. Wait a minute and try again.", true)
		return
	}
	if r.PostFormValue("password") != r.PostFormValue("confirm_password") {
		fail(http.StatusUnprocessableEntity, "The two passwords are not the same.", true)
		return
	}
	if err := s.svc.ResetPasswordWithLink(r.Context(), token, r.PostFormValue("password")); err != nil {
		ae := apperr.As(err)
		switch ae.Kind {
		case apperr.KindNotFound:
			fail(http.StatusNotFound, ae.Message, false)
		case apperr.KindInvalid:
			fail(http.StatusUnprocessableEntity, ae.Message, true)
		default:
			s.log.ErrorContext(r.Context(), "resetting a password", "err", err)
			fail(http.StatusInternalServerError, "Something went wrong on our side.", true)
		}
		return
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login?notice="+url.QueryEscape("Your password is changed. Sign in with it."), http.StatusSeeOther)
}
