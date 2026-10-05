// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Single sign-on (ADR 0033).

// ssoCookie holds a sign-in's state while the person is at their provider,
// so the provider's answer is accepted only in the browser that asked:
// someone else's code cannot be slipped into a victim's browser to sign
// them in as someone else.
func (s *Server) ssoCookie() string {
	if s.cfg.SecureCookies {
		return "__Host-araldo_sso"
	}
	return "araldo_sso"
}

func (s *Server) setSSOCookie(w http.ResponseWriter, state string, maxAge int) {
	//nolint:gosec // G124: Secure is off only for local plain-HTTP development (ARALDO_INSECURE_COOKIES)
	http.SetCookie(w, &http.Cookie{Name: s.ssoCookie(), Value: state, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

type ssoLoginData struct {
	Email string
	Next  string
}

func (s *Server) ssoLoginPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.render(w, http.StatusOK, "login_sso", s.view(nil, "", "Sign in with single sign-on", ssoLoginData{Email: q.Get("email"), Next: q.Get("next")}))
}

// ssoLoginSubmit starts the sign-in, then shows a link to the provider:
// the dashboard's Content-Security-Policy lets a form lead only to this
// site.
func (s *Server) ssoLoginSubmit(w http.ResponseWriter, r *http.Request) {
	data := ssoLoginData{Email: r.PostFormValue("email"), Next: r.PostFormValue("next")}
	fail := func(status int, msg string) {
		v := s.view(nil, "", "Sign in with single sign-on", data)
		v.Error = msg
		s.render(w, status, "login_sso", v)
	}
	if !s.login.allow(s.clientIP(r), time.Now()) {
		fail(http.StatusTooManyRequests, "Too many sign-in attempts from your network. Wait a minute and try again.")
		return
	}
	to, state, err := s.svc.BeginSSO(r.Context(), data.Email, safeNext(data.Next))
	if err != nil {
		ae := apperr.As(err)
		if ae.Kind == apperr.KindInternal {
			s.log.ErrorContext(r.Context(), "starting single sign-on", "err", err)
			fail(http.StatusInternalServerError, "Something went wrong on our side.")
			return
		}
		fail(http.StatusBadRequest, ae.Message)
		return
	}
	s.setSSOCookie(w, state, int(core.SSOStateTTL/time.Second))
	s.render(w, http.StatusOK, "sso_redirect", s.view(nil, "", "Continue to sign in", struct{ URL string }{to}))
}

// ssoCallback is where the provider sends the person back.
func (s *Server) ssoCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fail := func(status int, msg string) {
		v := s.view(nil, "", "Sign in with single sign-on", ssoLoginData{})
		v.Error = msg
		s.render(w, status, "login_sso", v)
	}
	ck, err := r.Cookie(s.ssoCookie())
	s.setSSOCookie(w, "", -1)
	if e := q.Get("error"); e != "" {
		msg := "Your identity provider did not sign you in."
		if e == "access_denied" {
			msg = "Your identity provider did not let you in: ask your administrator to give you access to Araldo."
		}
		fail(http.StatusUnauthorized, msg)
		return
	}
	state := q.Get("state")
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(state)) != 1 {
		fail(http.StatusBadRequest, "This sign-in was started in another browser, or took too long. Start again here.")
		return
	}
	res, err := s.svc.FinishSSO(r.Context(), state, q.Get("code"), r.UserAgent(), s.clientIP(r))
	if err != nil {
		ae := apperr.As(err)
		status := http.StatusUnauthorized
		if ae.Kind == apperr.KindInternal || ae.Kind == apperr.KindUnavailable {
			s.log.ErrorContext(r.Context(), "finishing single sign-on", "err", err)
			status = http.StatusBadGateway
		}
		fail(status, ae.Message)
		return
	}
	s.setSessionCookie(w, res.Token, res.Session.ExpiresAt.Add(core.SessionAbsolute))
	http.Redirect(w, r, safeNext(res.Next), http.StatusSeeOther) //nolint:gosec // G710: next went through safeNext
}

// The org's single sign-on settings, for owners.

type ssoPageData struct {
	*core.SSOSettings
	CallbackURL string
	VerifyName  string
	Roles       []model.Role
	ThroughSSO  bool
	HasVerified bool
	Connected   bool
}

func (s *Server) ssoPage(c *reqCtx) error {
	st, err := s.svc.SSO(c.ctx(), c.actor)
	if err != nil {
		return err
	}
	return s.page(c, "sso", "org", "Single sign-on", s.ssoData(c, st))
}

func (s *Server) ssoData(c *reqCtx, st *core.SSOSettings) ssoPageData {
	d := ssoPageData{SSOSettings: st, CallbackURL: s.svc.SSOCallbackURL(), VerifyName: core.SSOVerifyPrefix,
		Roles: []model.Role{model.RoleViewer, model.RoleEditor, model.RoleAdmin}, ThroughSSO: c.session.ThroughSSO(c.org.ID)}
	d.Connected = st.Connection != nil
	for _, dom := range st.Domains {
		d.HasVerified = d.HasVerified || dom.VerifiedAt != nil
	}
	return d
}

// ssoFormErr shows the settings again with what went wrong.
func (s *Server) ssoFormErr(c *reqCtx, err error) error {
	st, serr := s.svc.SSO(c.ctx(), c.actor)
	if serr != nil {
		return serr
	}
	return s.formErr(c, "sso", "org", "Single sign-on", s.ssoData(c, st), err)
}

func needsConfirm(err error) bool { return apperr.As(err).Code == "reauthentication_required" }

func (s *Server) saveSSO(c *reqCtx) error {
	err := s.svc.SaveSSOConnection(c.ctx(), c.actor, c.session, core.SSOConnectionInput{Issuer: c.r.PostFormValue("issuer"),
		ClientID: c.r.PostFormValue("client_id"), ClientSecret: c.r.PostFormValue("client_secret"),
		DefaultRole: model.Role(c.r.PostFormValue("default_role"))})
	switch {
	case needsConfirm(err):
		return redirect(c, "/confirm?next=/org/sso", "Confirm it's you to change single sign-on.")
	case err != nil:
		return s.ssoFormErr(c, err)
	}
	return redirect(c, "/org/sso", "Saved the identity provider.")
}

func (s *Server) deleteSSO(c *reqCtx) error {
	err := s.svc.DeleteSSOConnection(c.ctx(), c.actor, c.session)
	switch {
	case needsConfirm(err):
		return redirect(c, "/confirm?next=/org/sso", "Confirm it's you to disconnect single sign-on.")
	case err != nil:
		return s.ssoFormErr(c, err)
	}
	return redirect(c, "/org/sso", "Disconnected the identity provider.")
}

func (s *Server) ssoDomain(c *reqCtx) error {
	domain := c.r.PostFormValue("domain")
	var err error
	notice := ""
	switch c.r.PostFormValue("action") {
	case "add":
		_, err = s.svc.AddSSODomain(c.ctx(), c.actor, domain)
		notice = "Added " + domain + ": publish its TXT record, then verify it."
	case "verify":
		err = s.svc.VerifySSODomain(c.ctx(), c.actor, domain)
		notice = "Verified " + domain + "."
	case "remove":
		err = s.svc.RemoveSSODomain(c.ctx(), c.actor, domain)
		notice = "Removed " + domain + "."
	default:
		return apperr.Invalid("action_invalid", "action", "Unknown action.")
	}
	if err != nil {
		return s.ssoFormErr(c, err)
	}
	return redirect(c, "/org/sso", notice)
}

func (s *Server) requireSSO(c *reqCtx) error {
	on := c.r.PostFormValue("require") == "1"
	err := s.svc.SetRequireSSO(c.ctx(), c.actor, c.session, on)
	switch {
	case needsConfirm(err):
		return redirect(c, "/confirm?next=/org/sso", "Confirm it's you to stop requiring single sign-on.")
	case err != nil:
		return s.ssoFormErr(c, err)
	case on:
		return redirect(c, "/org/sso", "Single sign-on is now required.")
	}
	return redirect(c, "/org/sso", "Single sign-on is no longer required.")
}
