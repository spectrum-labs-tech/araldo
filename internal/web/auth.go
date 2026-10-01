// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

type loginData struct {
	Email string
	Next  string
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	v := s.view(nil, "", "Sign in", loginData{Next: r.URL.Query().Get("next")})
	v.Notice = r.URL.Query().Get("notice")
	s.render(w, http.StatusOK, "login", v)
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	data := loginData{Email: r.PostFormValue("email"), Next: r.PostFormValue("next")}
	fail := func(status int, msg string) {
		v := s.view(nil, "", "Sign in", data)
		v.Error = msg
		s.render(w, status, "login", v)
	}
	if !s.login.allow(s.clientIP(r), time.Now()) {
		fail(http.StatusTooManyRequests, "Too many sign-in attempts from your network. Wait a minute and try again.")
		return
	}
	res, err := s.svc.Login(r.Context(), data.Email, r.PostFormValue("password"), r.UserAgent(), s.clientIP(r))
	if err != nil {
		ae := apperr.As(err)
		if ae.Kind == apperr.KindInternal {
			s.log.ErrorContext(r.Context(), "sign-in failed", "err", err)
			fail(http.StatusInternalServerError, "Something went wrong on our side.")
			return
		}
		fail(http.StatusUnauthorized, ae.Message)
		return
	}
	s.setSessionCookie(w, res.Token, res.Session.ExpiresAt.Add(core.SessionAbsolute))
	next := safeNext(data.Next)
	if res.NeedsMFA {
		http.Redirect(w, r, "/login/mfa?next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther) //nolint:gosec // G710: next went through safeNext
}

func (s *Server) pendingSession(w http.ResponseWriter, r *http.Request) (*model.Session, bool) {
	ck, err := r.Cookie(s.sessionCookie())
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil, false
	}
	ss, _, err := s.svc.Session(r.Context(), ck.Value)
	if err != nil {
		s.clearSessionCookie(w)
		http.Redirect(w, r, "/login?notice="+url.QueryEscape("Your sign-in expired; start again."), http.StatusSeeOther)
		return nil, false
	}
	if ss.State != model.SessionPendingMFA {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return nil, false
	}
	return ss, true
}

func (s *Server) mfaPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pendingSession(w, r); !ok {
		return
	}
	s.render(w, http.StatusOK, "login_mfa", s.view(nil, "", "Two-factor authentication", loginData{Next: r.URL.Query().Get("next")}))
}

func (s *Server) mfaSubmit(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.pendingSession(w, r)
	if !ok {
		return
	}
	if !s.login.allow("mfa:"+ss.ID.String(), time.Now()) {
		v := s.view(nil, "", "Two-factor authentication", loginData{})
		v.Error = "Too many attempts. Sign in again."
		s.render(w, http.StatusTooManyRequests, "login_mfa", v)
		return
	}
	if err := s.svc.VerifySecondFactor(r.Context(), ss, r.PostFormValue("code")); err != nil {
		v := s.view(nil, "", "Two-factor authentication", loginData{Next: r.PostFormValue("next")})
		v.Error = apperr.As(err).Message
		s.render(w, http.StatusUnauthorized, "login_mfa", v)
		return
	}
	http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther) //nolint:gosec // G710: safeNext keeps it on this site
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	c, ok := s.signedIn(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(c) {
		s.renderError(c, http.StatusForbidden, "This form expired. Reload and try again.")
		return
	}
	_ = s.svc.Logout(r.Context(), c.session.ID)
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login?notice="+url.QueryEscape("Signed out."), http.StatusSeeOther)
}

// confirm is sudo mode: re-enter the password (and code) before sensitive
// actions (ADR 0007).
func (s *Server) confirmPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.signedIn(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, "confirm", s.view(c, "", "Confirm it's you", loginData{Next: r.URL.Query().Get("next")}))
}

func (s *Server) confirmSubmit(w http.ResponseWriter, r *http.Request) {
	c, ok := s.signedIn(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(c) {
		s.renderError(c, http.StatusForbidden, "This form expired. Reload and try again.")
		return
	}
	next := safeNext(r.PostFormValue("next"))
	if !s.login.allow("sudo:"+c.session.ID.String(), time.Now()) {
		v := s.view(c, "", "Confirm it's you", loginData{Next: next})
		v.Error = "Too many attempts. Wait a minute."
		s.render(w, http.StatusTooManyRequests, "confirm", v)
		return
	}
	if err := s.svc.Reauthenticate(r.Context(), c.session, r.PostFormValue("password"), r.PostFormValue("code")); err != nil {
		v := s.view(c, "", "Confirm it's you", loginData{Next: next})
		v.Error = apperr.As(err).Message
		s.render(w, http.StatusUnauthorized, "confirm", v)
		return
	}
	// A GET target re-renders; forms that needed sudo are sent back to.
	http.Redirect(w, r, next, http.StatusSeeOther) //nolint:gosec // G710: next went through safeNext
}

// Account: password, two-factor authentication, sessions.

type accountData struct {
	MFARequired   bool
	Enrolling     bool
	SecretText    string
	QR            string
	RecoveryCodes []string
	RecoveryLeft  int
	Sessions      []*model.Session
}

func (s *Server) accountPage(c *reqCtx) error {
	d := accountData{MFARequired: c.r.URL.Query().Get("mfa_required") == "1"}
	n, err := s.svc.RemainingRecoveryCodes(c.ctx(), c.user.ID)
	if err != nil {
		return err
	}
	d.RecoveryLeft = n
	return s.page(c, "account", "account", "Your account", d)
}

func (s *Server) accountMFABegin(c *reqCtx) error {
	secret, uri, err := s.svc.BeginTOTP(c.ctx(), c.session)
	if err != nil {
		return err
	}
	d := accountData{Enrolling: true, SecretText: secret, QR: qrDataURL(uri)}
	return s.page(c, "account", "account", "Your account", d)
}

func (s *Server) accountMFAConfirm(c *reqCtx) error {
	codes, err := s.svc.ConfirmTOTP(c.ctx(), c.session, c.r.PostFormValue("code"))
	if err != nil {
		return s.formErr(c, "account", "account", "Your account", accountData{}, err)
	}
	return s.page(c, "account", "account", "Your account", accountData{RecoveryCodes: codes})
}

func (s *Server) accountMFADisable(c *reqCtx) error {
	if err := s.svc.DisableTOTP(c.ctx(), c.session); err != nil {
		return err
	}
	return redirect(c, "/account", "Two-factor authentication is off.")
}

func (s *Server) accountRecoveryCodes(c *reqCtx) error {
	codes, err := s.svc.RegenerateRecoveryCodes(c.ctx(), c.session)
	if err != nil {
		return err
	}
	return s.page(c, "account", "account", "Your account", accountData{RecoveryCodes: codes})
}

func (s *Server) accountPassword(c *reqCtx) error {
	next := c.r.PostFormValue("new_password")
	if next != c.r.PostFormValue("confirm_password") {
		return s.formErr(c, "account", "account", "Your account", accountData{},
			apperr.Invalid("password_mismatch", "confirm_password", "The new passwords do not match."))
	}
	if err := s.svc.ChangePassword(c.ctx(), c.session, c.r.PostFormValue("current_password"), next); err != nil {
		return s.formErr(c, "account", "account", "Your account", accountData{}, err)
	}
	return redirect(c, "/account", "Password changed. Other sessions were signed out.")
}

// qrDataURL renders an otpauth:// URI as a PNG data URL.
func qrDataURL(uri string) string {
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, qrImage(code, qrScale)); err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// qrScale is pixels per module; qrQuiet is the white margin, in modules, scanners need around the code.
const (
	qrScale = 6
	qrQuiet = 4
)

// qrImage draws code with scale pixels per module inside a qrQuiet-module margin. It replaces
// qr.Code.Image, which sizes the canvas for the scale but draws each module as a single pixel in the
// top-left corner, leaving a speck in a large white square.
func qrImage(code *qr.Code, scale int) *image.Gray {
	side := (code.Size + 2*qrQuiet) * scale
	img := image.NewGray(image.Rect(0, 0, side, side))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for my := 0; my < code.Size; my++ {
		for mx := 0; mx < code.Size; mx++ {
			if !code.Black(mx, my) {
				continue
			}
			x0, y0 := (mx+qrQuiet)*scale, (my+qrQuiet)*scale
			for y := y0; y < y0+scale; y++ {
				for x := x0; x < x0+scale; x++ {
					img.Pix[y*img.Stride+x] = 0
				}
			}
		}
	}
	return img
}

// Context switches: org and mode.
func (s *Server) switchContext(c *reqCtx) error {
	orgRef := c.r.PostFormValue("org")
	orgID := c.member.OrgID
	if orgRef != "" {
		for _, m := range c.orgs {
			if m.OrgID.String() == orgRef {
				orgID = m.OrgID
			}
		}
	}
	live := c.session.Livemode
	switch c.r.PostFormValue("mode") {
	case "live":
		live = true
	case "test":
		live = false
	}
	if err := s.svc.SwitchContext(c.ctx(), c.session, orgID, live); err != nil {
		return err
	}
	back := c.r.PostFormValue("back")
	if back == "" || strings.Contains(back, "/posts/") || strings.Contains(back, "/channels/") || strings.Contains(back, "/webhooks/") {
		back = "/"
	}
	return redirect(c, safeNext(back), "")
}

// Onboarding: a signed-in user with no org creates one.
func (s *Server) onboarding(w http.ResponseWriter, r *http.Request) {
	c, ok := s.signedIn(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		if !s.checkCSRF(c) {
			s.renderError(c, http.StatusForbidden, "This form expired. Reload and try again.")
			return
		}
		o, err := s.svc.CreateOrg(r.Context(), c.user.ID, r.PostFormValue("name"))
		if err != nil {
			v := s.view(c, "", "Create your org", nil)
			v.Error = apperr.As(err).Message
			s.render(w, http.StatusUnprocessableEntity, "onboarding", v)
			return
		}
		_ = s.svc.SwitchContext(r.Context(), c.session, o.ID, false)
		http.Redirect(w, r, "/brands/new?notice="+url.QueryEscape("Org created. Now add your first brand."), http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "onboarding", s.view(c, "", "Create your org", nil))
}
