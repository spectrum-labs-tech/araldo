// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/api"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Invitations (ADR 0004): admins invite people from the org page, and the
// invitee accepts at /invite/{token}, signed in or by creating an account.

// inviteSendTimeout bounds emailing an invitation, which the admin waits
// for.
const inviteSendTimeout = 20 * time.Second

func (s *Server) inviteMember(c *reqCtx) error {
	d, err := s.orgData(c)
	if err != nil {
		return err
	}
	link, inv, err := s.svc.InviteMember(c.ctx(), c.actor, c.session, c.r.PostFormValue("email"), model.Role(c.r.PostFormValue("role")))
	if err != nil {
		if apperr.As(err).Code == "reauthentication_required" {
			return redirect(c, "/confirm?next=/org", "Confirm your password to invite an owner.")
		}
		return s.formErr(c, "org", "org", "Organization", d, err)
	}
	// The link is shown once, here; only its hash is kept. It is emailed
	// too when the server sends email; if that fails, it can be shared.
	d.InviteLink, d.Invited = link, inv
	if s.svc.MailEnabled() {
		ctx, cancel := context.WithTimeout(c.ctx(), inviteSendTimeout)
		err := s.svc.EmailInvitation(ctx, c.actor, inv, link)
		cancel()
		if d.InviteEmailed = err == nil; err != nil {
			s.log.WarnContext(c.ctx(), "emailing an invitation", "err", err)
			d.InviteEmailError = "We could not email it, so share the link yourself."
		}
	}
	if d.Invitations, err = s.svc.Invitations(c.ctx(), c.actor); err != nil {
		return err
	}
	return s.page(c, "org", "org", "Organization", d)
}

func (s *Server) revokeInvitation(c *reqCtx) error {
	iid, err := pathUUID(c, id.Invitation, "invitation")
	if err != nil {
		return err
	}
	if err := s.svc.RevokeInvitation(c.ctx(), c.actor, iid); err != nil {
		return err
	}
	return redirect(c, "/org", "Invitation withdrawn.")
}

type inviteData struct {
	Invitation *model.Invitation
	// Signed in is the account looking at the page, if any, and whether it
	// is the one invited.
	SignedIn *model.User
	Matches  bool
	CSRF     string
	Name     string
	Token    string
}

// activeSession is the request's signed-in session, if it has one.
func (s *Server) activeSession(r *http.Request) (*model.Session, *model.User) {
	ck, err := r.Cookie(s.sessionCookie())
	if err != nil {
		return nil, nil
	}
	ss, u, err := s.svc.Session(r.Context(), ck.Value)
	if err != nil || ss.State != model.SessionActive {
		return nil, nil
	}
	return ss, u
}

func (s *Server) invitePage(w http.ResponseWriter, r *http.Request) {
	s.renderInvite(w, r, http.StatusOK, inviteData{}, "")
}

func (s *Server) renderInvite(w http.ResponseWriter, r *http.Request, status int, d inviteData, problem string) {
	token := r.PathValue("token")
	inv, err := s.svc.Invitation(r.Context(), token)
	if err != nil {
		v := s.view(nil, "", "Invitation", nil)
		v.Error = apperr.As(err).Message
		s.render(w, http.StatusNotFound, "invite", v)
		return
	}
	d.Invitation, d.Token = inv, token
	if ss, u := s.activeSession(r); u != nil {
		d.SignedIn, d.CSRF = u, ss.CSRFToken
		mine, _ := core.NormalizeEmail(u.Email)
		invited, _ := core.NormalizeEmail(inv.Email)
		d.Matches = mine == invited
	}
	v := s.view(nil, "", "Join "+inv.OrgName, d)
	v.Error = problem
	s.render(w, status, "invite", v)
}

func (s *Server) inviteSubmit(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if ss, u := s.activeSession(r); u != nil {
		// Signed in: accept as this account.
		got := r.PostFormValue("csrf")
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(ss.CSRFToken)) != 1 {
			s.renderInvite(w, r, http.StatusForbidden, inviteData{}, "This form expired. Try again.")
			return
		}
		inv, err := s.svc.AcceptInvitation(r.Context(), token, u, api.RequestID(r.Context()))
		if err != nil {
			s.inviteErr(w, r, err, inviteData{})
			return
		}
		if err := s.svc.SwitchContext(r.Context(), ss, inv.OrgID, false); err != nil {
			s.log.WarnContext(r.Context(), "switching to the joined org", "err", err)
		}
		http.Redirect(w, r, "/?notice="+url.QueryEscape("You joined "+inv.OrgName+"."), http.StatusSeeOther)
		return
	}
	// Not signed in: make the invitee's account, then sign them in.
	if !s.login.allow(s.clientIP(r), time.Now()) {
		s.renderInvite(w, r, http.StatusTooManyRequests, inviteData{}, "Too many attempts from your network. Wait a minute and try again.")
		return
	}
	d := inviteData{Name: r.PostFormValue("name")}
	password := r.PostFormValue("password")
	if password != r.PostFormValue("confirm") {
		s.renderInvite(w, r, http.StatusUnprocessableEntity, d, "The passwords do not match.")
		return
	}
	u, inv, err := s.svc.AcceptInvitationNewAccount(r.Context(), token, d.Name, password, api.RequestID(r.Context()))
	if err != nil {
		s.inviteErr(w, r, err, d)
		return
	}
	res, err := s.svc.Login(r.Context(), u.Email, password, r.UserAgent(), s.clientIP(r))
	if err != nil {
		http.Redirect(w, r, "/login?notice="+url.QueryEscape("Your account is ready: sign in."), http.StatusSeeOther)
		return
	}
	s.setSessionCookie(w, res.Token, res.Session.ExpiresAt.Add(core.SessionAbsolute))
	_ = s.svc.SwitchContext(r.Context(), res.Session, inv.OrgID, false)
	http.Redirect(w, r, "/?notice="+url.QueryEscape("Welcome to "+inv.OrgName+"."), http.StatusSeeOther)
}

func (s *Server) inviteErr(w http.ResponseWriter, r *http.Request, err error, d inviteData) {
	ae := apperr.As(err)
	if ae.Kind == apperr.KindInternal || ae.Kind == apperr.KindUnavailable {
		s.log.ErrorContext(r.Context(), "accepting an invitation", "err", err)
		s.renderInvite(w, r, http.StatusInternalServerError, d, "Something went wrong on our side. Try again.")
		return
	}
	status := http.StatusUnprocessableEntity
	if ae.Kind == apperr.KindForbidden {
		status = http.StatusForbidden
	}
	s.renderInvite(w, r, status, d, ae.Message)
}
