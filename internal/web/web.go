// SPDX-License-Identifier: AGPL-3.0-or-later

// Package web is Araldo's dashboard (ADR 0015): server-rendered pages,
// embedded in the binary, signed in with sessions (ADR 0007). Pages call
// core directly and never the HTTP API.
package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/api"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/buildinfo"
	"github.com/spectrum-labs-tech/araldo/internal/clientip"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/media"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// MaxPostForm bounds the new-post form, which can carry four images at
// their largest.
const MaxPostForm = 4*media.MaxBytes + 1<<20

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Config holds dashboard settings.
type Config struct {
	// SecureCookies uses Secure, __Host- cookies; off only for local
	// plain-HTTP development.
	SecureCookies bool
	// ClientIPHeader names a trusted proxy header holding the client IP
	// (for example CF-Connecting-IP), used for sign-in rate limits; for a
	// list, its last address (see clientIP).
	ClientIPHeader string
}

// Server serves the dashboard.
type Server struct {
	svc   *core.Service
	log   *slog.Logger
	cfg   Config
	pages map[string]*template.Template
	mux   *http.ServeMux
	// cop refuses state-changing requests from other sites, signed-in
	// forms (which also carry a CSRF token) and signed-out ones alike.
	cop   *http.CrossOriginProtection
	login *ipLimiter
	doc   *apiDoc
}

// New returns the dashboard.
func New(svc *core.Service, log *slog.Logger, cfg Config) (*Server, error) {
	s := &Server{svc: svc, log: log, cfg: cfg, pages: map[string]*template.Template{}, mux: http.NewServeMux(), login: newIPLimiter(10, time.Minute)}
	if err := s.parse(); err != nil {
		return nil, err
	}
	doc, err := buildAPIDoc()
	if err != nil {
		return nil, fmt.Errorf("web: API reference: %w", err)
	}
	s.doc = doc
	s.routes()
	s.cop = http.NewCrossOriginProtection()
	// One-click unsubscribe (RFC 8058) is posted by mail providers; its
	// token is the only authority it needs.
	s.cop.AddInsecureBypassPattern("POST /unsubscribe/{token}")
	s.cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.publicError(w, http.StatusForbidden, "This form was sent from another site, so it was refused. Open Araldo and try again there.")
	}))
	return s, nil
}

// Route is the pattern a request matches, such as "GET /posts/{id}",
// without serving it; "" when none does.
func (s *Server) Route(r *http.Request) string {
	_, pattern := s.mux.Handler(r)
	return pattern
}

// MaxForm bounds a form or JSON post that carries no files: room for a
// long template or newsletter, far short of what Go would otherwise read.
const MaxForm = 1 << 20

// ServeHTTP sets a strict Content-Security-Policy with a fresh nonce for
// the style elements the template editor adds, refuses cross-site form
// posts, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	nonce, _ := authn.NewToken() // the token, not its hash
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'nonce-"+nonce+"'; "+
		"script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	ctx := context.WithValue(r.Context(), nonceKey{}, nonce)
	if r.Method == http.MethodPost {
		// Every post is bounded at MaxForm, signed out ones included. Only
		// app() takes uploads: it bounds the unread body again at
		// MaxPostForm once the session is known.
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			ctx = context.WithValue(ctx, uploadBodyKey{}, r.Body)
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxForm)
	}
	s.cop.Handler(s.mux).ServeHTTP(w, r.WithContext(ctx))
}

type nonceKey struct{}

// uploadBodyKey holds a multipart post's body before MaxForm bounds it.
type uploadBodyKey struct{}

// cspNonce is the request's style nonce.
func cspNonce(r *http.Request) string {
	n, _ := r.Context().Value(nonceKey{}).(string)
	return n
}

func (s *Server) parse() error {
	entries, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := strings.TrimSuffix(strings.TrimPrefix(e, "templates/"), ".html")
		if name == "layout" || strings.HasPrefix(name, "_") {
			continue // the layout and partials are parsed with every page
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/_*.html", e)
		if err != nil {
			return fmt.Errorf("web: parse %s: %w", e, err)
		}
		s.pages[name] = t
	}
	return nil
}

// Cookie names.
func (s *Server) sessionCookie() string {
	if s.cfg.SecureCookies {
		return "__Host-araldo_session"
	}
	return "araldo_session"
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	//nolint:gosec // G124: Secure is off only for local plain-HTTP development (ARALDO_INSECURE_COOKIES)
	http.SetCookie(w, &http.Cookie{Name: s.sessionCookie(), Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode, Expires: expires})
}

// noticeCookie carries a one-time notice ("Saved.") to the page after a
// redirect. A cookie, not the address: a notice in the query string could
// be written by anyone who sends someone a link.
const noticeCookie = "araldo_notice"

// setNotice shows msg on the next page this browser loads.
func setNotice(w http.ResponseWriter, secure bool, msg string) {
	if msg == "" {
		return
	}
	//nolint:gosec // G124: Secure is off only for local plain-HTTP development (ARALDO_INSECURE_COOKIES)
	http.SetCookie(w, &http.Cookie{Name: noticeCookie, Value: base64.RawURLEncoding.EncodeToString([]byte(msg)), Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 60})
}

// takeNotice reads the notice set for this page, once.
func takeNotice(w http.ResponseWriter, r *http.Request, secure bool) string {
	ck, err := r.Cookie(noticeCookie)
	if err != nil {
		return ""
	}
	//nolint:gosec // G124: as above
	http.SetCookie(w, &http.Cookie{Name: noticeCookie, Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	msg, err := base64.RawURLEncoding.DecodeString(ck.Value)
	if err != nil || len(msg) > 500 {
		return ""
	}
	return string(msg)
}

// redirectNotice redirects to a local path, with a notice for the page
// there.
func (s *Server) redirectNotice(w http.ResponseWriter, r *http.Request, path, notice string) {
	setNotice(w, s.cfg.SecureCookies, notice)
	http.Redirect(w, r, path, http.StatusSeeOther) //nolint:gosec // G710: callers pass local paths
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	//nolint:gosec // G124: as above
	http.SetCookie(w, &http.Cookie{Name: s.sessionCookie(), Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// view is everything a page template gets.
type view struct {
	Title      string
	Nav        string
	User       *model.User
	Session    *model.Session
	Org        *model.Membership
	Orgs       []model.Membership
	Livemode   bool
	CSRF       string
	Notice     string
	Error      string
	Problems   []apperr.Problem
	Version    string
	Data       any
	NeedsMFA   bool
	RequireMFA bool
	// Wide pages use the full width (the template editor).
	Wide bool
	// Guide is set when the page defines a "guide" template: how to do the
	// page's work, beside it.
	Guide bool
	// Nonce allows the editor's style elements (Content-Security-Policy).
	Nonce string
	// Public pages (a shared report) are full width with no navigation,
	// for someone who is not signed in.
	Public bool
	// OrgStatus and StatusNote are the org's, when not active (ADR 0031);
	// BillingLink shows owners the way to billing, and SignupURL where
	// accounts are made.
	OrgStatus   string
	StatusNote  string
	BillingLink bool
	SignupURL   string
	// MailEnabled: the server sends email, so passwords can be reset by
	// email (ADR 0034).
	MailEnabled bool
	// Unread counts the person's unread notifications.
	Unread int
	// Back is where the org and mode switch returns to: the page shown, if
	// it was asked for with GET (a form's POST address may have no page).
	// The path only: a query can carry what must not be echoed (a device
	// code someone sent).
	Back string
}

// widePages use the full width of the window.
var widePages = map[string]bool{"template_edit": true, "api_reference": true}

// reqCtx is a signed-in request.
type reqCtx struct {
	w       http.ResponseWriter
	r       *http.Request
	user    *model.User
	session *model.Session
	member  *model.Membership
	orgs    []model.Membership
	actor   core.Actor
	org     *model.Org
	// unread counts the person's unread notifications, for the header.
	unread int
	// secure is whether cookies are Secure (the notice cookie's too).
	secure bool
}

func (c *reqCtx) ctx() context.Context { return c.r.Context() }

type pageFunc func(c *reqCtx) error

// app wraps a page that needs a signed-in, active session and an org.
func (s *Server) app(nav string, fn pageFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.signedIn(w, r)
		if !ok {
			return
		}
		if c.member == nil {
			http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
			return
		}
		// An org that requires single sign-on shows nothing to a session
		// that did not come through it (ADR 0033); the person's own account
		// and switching orgs still work.
		if c.org.RequireSSO && !c.session.ThroughSSO(c.org.ID) && nav != "account" && nav != "" {
			s.render(w, http.StatusForbidden, "sso_required", s.view(c, "", "Single sign-on required", nil))
			return
		}
		// An org that requires MFA shows nothing until the member enrolls,
		// unless they came through its single sign-on, whose provider
		// applies its own policy.
		if c.org.RequireMFA && !c.user.SecondFactor() && !c.session.ThroughSSO(c.org.ID) && nav != "account" {
			http.Redirect(w, r, "/account?mfa_required=1", http.StatusSeeOther)
			return
		}
		// A suspended org shows only why, and the way to billing; the
		// person's own account and switching orgs still work (ADR 0031).
		if c.org.Status == model.OrgSuspended && nav != "account" && nav != "billing" && nav != "" {
			s.render(w, http.StatusForbidden, "suspended", s.view(c, "", "Suspended", nil))
			return
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			// In memory: the container's file system is read-only.
			if raw, ok := r.Context().Value(uploadBodyKey{}).(io.ReadCloser); ok {
				r.Body = raw
			}
			r.Body = http.MaxBytesReader(w, r.Body, MaxPostForm)
			if err := r.ParseMultipartForm(MaxPostForm); err != nil { //nolint:gosec // G120: the body is bounded on the line above
				s.renderError(c, http.StatusRequestEntityTooLarge, fmt.Sprintf("The upload is too large: images are up to %d MiB each, and %d MiB in all.",
					media.MaxBytes>>20, MaxPostForm>>20))
				return
			}
		}
		if r.Method == http.MethodPost && !s.checkCSRF(c) {
			s.renderError(c, http.StatusForbidden, formExpired)
			return
		}
		if err := fn(c); err != nil {
			s.handleErr(c, err) //nolint:contextcheck // c carries the request context
		}
	}
}

// signedIn resolves the session, redirecting to sign-in when there is
// none. Pending-MFA sessions go to the second step.
func (s *Server) signedIn(w http.ResponseWriter, r *http.Request) (*reqCtx, bool) {
	ck, err := r.Cookie(s.sessionCookie())
	if err != nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return nil, false
	}
	ss, u, err := s.svc.Session(r.Context(), ck.Value)
	if status, msg, fault := s.serverFault(w, r, "loading a session", err); fault {
		// Keep the cookie: the session is likely fine, the database is not.
		s.publicError(w, status, msg)
		return nil, false
	}
	if err != nil {
		s.clearSessionCookie(w)
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return nil, false
	}
	if ss.State == model.SessionPendingMFA {
		http.Redirect(w, r, "/login/mfa", http.StatusSeeOther)
		return nil, false
	}
	c := &reqCtx{w: w, r: r, user: u, session: ss, secure: s.cfg.SecureCookies}
	if n, err := s.svc.UnreadNotifications(r.Context(), u.ID); err == nil {
		c.unread = n
	}
	c.orgs, err = s.svc.UserOrgs(r.Context(), u.ID)
	if err != nil {
		s.failSignedIn(w, r, "loading orgs", err)
		return nil, false
	}
	if ss.SSOOrg != nil { // a session through single sign-on reaches its org only (ADR 0033)
		c.orgs = slices.DeleteFunc(c.orgs, func(m model.Membership) bool { return m.OrgID != *ss.SSOOrg })
	}
	orgID := uuid.Nil
	if ss.CurrentOrg != nil {
		orgID = *ss.CurrentOrg
	}
	for i := range c.orgs {
		if c.orgs[i].OrgID == orgID || orgID == uuid.Nil {
			c.member = &c.orgs[i]
			break
		}
	}
	if c.member == nil && len(c.orgs) > 0 {
		c.member = &c.orgs[0]
	}
	if c.member != nil {
		c.actor, _, err = s.svc.MemberActor(r.Context(), u.ID, c.member.OrgID, ss.Livemode, api.RequestID(r.Context()))
		if err == nil {
			c.org, err = s.svc.Org(r.Context(), c.actor)
		}
		if err != nil {
			s.failSignedIn(w, r, "loading membership", err)
			return nil, false
		}
	}
	return c, true
}

// failSignedIn answers a failure to load a signed-in person's orgs: a
// fault of ours (503 or 500), or, for anything else, 500 as well, since
// their session was already found.
func (s *Server) failSignedIn(w http.ResponseWriter, r *http.Request, what string, err error) {
	status, msg, fault := s.serverFault(w, r, what, err)
	if !fault {
		s.log.ErrorContext(r.Context(), what, "err", err)
		status, msg = http.StatusInternalServerError, "Something went wrong on our side."
	}
	s.publicError(w, status, msg)
}

// formExpired is what a form whose CSRF token does not match is told.
const formExpired = "This form expired. Go back, reload the page and try again."

func (s *Server) checkCSRF(c *reqCtx) bool {
	return sameToken(c.r.PostFormValue("csrf"), c.session.CSRFToken)
}

// sameToken compares a token a request sent with the expected one in
// constant time; an empty one never matches.
func sameToken(got, want string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// signedInForm is signedIn for a page outside app() that takes a form: a
// post must carry the session's CSRF token.
func (s *Server) signedInForm(w http.ResponseWriter, r *http.Request) (*reqCtx, bool) {
	c, ok := s.signedIn(w, r)
	if !ok {
		return nil, false
	}
	if r.Method == http.MethodPost && !s.checkCSRF(c) {
		s.renderError(c, http.StatusForbidden, formExpired)
		return nil, false
	}
	return c, true
}

func (s *Server) view(c *reqCtx, nav, title string, data any) view {
	v := view{Title: title, Nav: nav, Version: buildinfo.Version, Data: data, SignupURL: s.svc.SignupURL(), MailEnabled: s.svc.MailEnabled()}
	if c != nil {
		v.User, v.Session, v.Org, v.Orgs, v.CSRF = c.user, c.session, c.member, c.orgs, c.session.CSRFToken
		v.Nonce = cspNonce(c.r)
		v.Livemode = c.session.Livemode
		v.Notice = takeNotice(c.w, c.r, c.secure)
		if c.org != nil {
			v.RequireMFA = c.org.RequireMFA
			if c.org.Status != model.OrgActive {
				v.OrgStatus, v.StatusNote = string(c.org.Status), c.org.StatusNote
			}
		}
		if c.r.Method == http.MethodGet {
			v.Back = c.r.URL.EscapedPath()
		}
		v.BillingLink = s.svc.Billing() && c.member != nil && c.member.Role == model.RoleOwner
		v.Unread = c.unread
	}
	return v
}

func (s *Server) render(w http.ResponseWriter, status int, page string, v view) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "no such page "+page, http.StatusInternalServerError)
		return
	}
	v.Wide = widePages[page]
	if g := t.Lookup("guide"); g != nil && g.Tree != nil {
		v.Guide = len(g.Tree.Root.Nodes) > 0
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, v); err != nil {
		s.log.Error("rendering page", "page", page, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) page(c *reqCtx, page, nav, title string, data any) error {
	s.render(c.w, http.StatusOK, page, s.view(c, nav, title, data))
	return nil
}

// formErr re-renders a form page with a problem.
func (s *Server) formErr(c *reqCtx, page, nav, title string, data any, err error) error {
	ae := apperr.As(err)
	if ae.Kind == apperr.KindInternal || ae.Kind == apperr.KindUnavailable {
		return err
	}
	v := s.view(c, nav, title, data)
	v.Error, v.Problems = ae.Message, ae.Problems
	status := http.StatusUnprocessableEntity
	if ae.Kind == apperr.KindForbidden {
		status = http.StatusForbidden
		s.recordDenial(c, ae)
	}
	s.render(c.w, status, page, v)
	return nil
}

// recordDenial audits a member refused an action (ADR 0004); a password
// confirmation asked for is not a refusal.
func (s *Server) recordDenial(c *reqCtx, ae *apperr.Error) {
	if c.member == nil || ae.Code == core.CodeReauthRequired {
		return
	}
	op := c.r.Pattern // the route, without the IDs in it
	if op == "" {
		op = c.r.Method + " " + c.r.URL.Path
	}
	s.svc.RecordDenial(c.ctx(), c.actor, op, ae.Code)
}

func (s *Server) handleErr(c *reqCtx, err error) {
	ae := apperr.As(err)
	switch ae.Kind {
	case apperr.KindInternal:
		s.log.ErrorContext(c.ctx(), "page failed", "path", c.r.URL.Path, "err", err)
		s.renderError(c, http.StatusInternalServerError, "Something went wrong on our side. Request "+api.RequestID(c.ctx())+".")
	case apperr.KindUnavailable:
		s.log.WarnContext(c.ctx(), "page failed: a dependency is unavailable", "path", c.r.URL.Path, "err", err)
		c.w.Header().Set("Retry-After", "30")
		s.renderError(c, http.StatusServiceUnavailable, ae.Message)
	case apperr.KindNotFound:
		s.renderError(c, http.StatusNotFound, ae.Message)
	case apperr.KindForbidden:
		if ae.Code == core.CodeReauthRequired {
			http.Redirect(c.w, c.r, "/confirm?next="+url.QueryEscape(c.r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		s.recordDenial(c, ae)
		s.renderError(c, http.StatusForbidden, ae.Message)
	default:
		s.renderError(c, http.StatusUnprocessableEntity, ae.Error())
	}
}

// serverFault is how a page outside app() answers an error that is ours,
// not the visitor's: 503 with Retry-After when a dependency is unavailable
// (Araldo recovers on its own; the visitor should try again), 500 for
// anything else unexpected, logged either way. ok is false for the
// visitor's own errors (invalid, not found, refused), which the page
// answers itself.
func (s *Server) serverFault(w http.ResponseWriter, r *http.Request, what string, err error) (status int, msg string, ok bool) {
	if err == nil {
		return 0, "", false
	}
	ae := apperr.As(err)
	switch ae.Kind {
	case apperr.KindUnavailable:
		s.log.WarnContext(r.Context(), what+": a dependency is unavailable", "err", err)
		w.Header().Set("Retry-After", "30")
		return http.StatusServiceUnavailable, ae.Message, true
	case apperr.KindInternal:
		s.log.ErrorContext(r.Context(), what, "err", err)
		return http.StatusInternalServerError, "Something went wrong on our side. Request " + api.RequestID(r.Context()) + ".", true
	}
	return 0, "", false
}

// publicError renders the error page for someone who may not be signed in.
func (s *Server) publicError(w http.ResponseWriter, status int, msg string) {
	v := s.view(nil, "", "Error", nil)
	v.Error = msg
	s.render(w, status, "error", v)
}

func (s *Server) renderError(c *reqCtx, status int, msg string) {
	v := s.view(c, "", "Error", nil)
	v.Error = msg
	s.render(c.w, status, "error", v)
}

func redirect(c *reqCtx, path, notice string) error {
	setNotice(c.w, c.secure, notice)
	http.Redirect(c.w, c.r, path, http.StatusSeeOther) //nolint:gosec // G710: callers pass local paths (through safeNext when user-supplied)
	return nil
}

// safeNext keeps a post-login redirect on this site: a path, not another
// host. Browsers drop tabs and newlines from URLs and read backslashes as
// slashes, so "/<tab>/evil.example" or "/\evil.example" would leave; those
// are refused too.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") ||
		strings.IndexFunc(next, unicode.IsControl) >= 0 {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return next
}

// clientIP is the address sign-in limits count. With a trusted proxy
// header, it is the header's last address: the one that proxy added. A
// list's earlier entries (X-Forwarded-For) are whatever the client sent.
func (s *Server) clientIP(r *http.Request) string {
	return clientip.From(r, s.cfg.ClientIPHeader)
}

// ipLimiter bounds sign-in attempts per IP (ADR 0007).
type ipLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
	// tracked caps how many addresses it remembers.
	tracked int
}

func newIPLimiter(max int, window time.Duration) *ipLimiter {
	return &ipLimiter{max: max, window: window, hits: map[string][]time.Time{}, tracked: 50_000}
}

// allow reports whether ip may try again, and counts the attempt. When it
// remembers as many addresses as it may, it forgets those with no attempt
// in the window; if that frees nothing (attempts from more addresses than
// that, within a minute) a new address is refused until some expire,
// rather than everyone's count being reset.
func (l *ipLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, known := l.hits[ip]; !known && len(l.hits) >= l.tracked {
		for k, ts := range l.hits {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= l.window {
				delete(l.hits, k)
			}
		}
		if len(l.hits) >= l.tracked {
			return false
		}
	}
	recent := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.max {
		l.hits[ip] = recent
		return false
	}
	l.hits[ip] = append(recent, now)
	return true
}

// statusPills is the pill each status shows as. Statuses of different
// things share values ("failed" is a post's, a target's, an issue's), so one
// entry serves them all. Webhook deliveries and scheduled tasks report the
// plain words at the end.
var statusPills = func() map[string]string {
	m := map[string]string{}
	for class, statuses := range map[string][]string{
		"good": {string(model.PostPublished), string(model.TargetPublished), string(model.ChannelActive), string(model.ConnectionActive),
			string(model.OrgActive), string(model.IssueSent), string(model.DeliverySent), model.EndpointEnabled, "succeeded", "ok"},
		"bad": {string(model.PostFailed), string(model.PostRejected), string(model.TargetFailed), string(model.TargetNeedsAttention),
			string(model.ChannelNeedsReauth), string(model.ChannelDisabled), string(model.ConnectionNeedsReauth), string(model.IssueFailed),
			string(model.DeliveryFailed), string(model.DeliveryNeedsAttention), model.EndpointDisabled},
		"warn": {string(model.PostPendingApproval), string(model.PostPublishing), string(model.PostPartiallyPublished),
			string(model.IssuePendingApproval), string(model.IssueSending), string(model.IssuePartiallySent), string(model.DeliveryHeld),
			string(model.DeliveryHandedOff), string(model.TargetPublishing), "pending", "delivering"},
	} {
		for _, st := range statuses {
			m[st] = class
		}
	}
	return m
}()

// statusClass is the pill class for a status: good, bad, warn, or neutral
// for one that is neither (draft, queued, canceled).
func statusClass(s any) string {
	if class, ok := statusPills[fmt.Sprint(s)]; ok {
		return class
	}
	return "neutral"
}

// Template helpers.
var funcs = template.FuncMap{
	"fmtTime": func(t any) string {
		switch v := t.(type) {
		case time.Time:
			return v.UTC().Format("Jan 2, 2006 15:04 UTC")
		case *time.Time:
			if v == nil {
				return "—"
			}
			return v.UTC().Format("Jan 2, 2006 15:04 UTC")
		}
		return ""
	},
	"ago": func(t time.Time) string {
		d := time.Since(t)
		switch {
		case d < 0:
			return "in " + human(-d)
		case d < time.Minute:
			return "just now"
		}
		return human(d) + " ago"
	},
	"postID":            func(u uuid.UUID) string { return id.Format(id.Post, u) },
	"brandID":           func(u uuid.UUID) string { return id.Format(id.Brand, u) },
	"chanID":            func(u uuid.UUID) string { return id.Format(id.Channel, u) },
	"tmplID":            func(u uuid.UUID) string { return id.Format(id.Template, u) },
	"targetID":          func(u uuid.UUID) string { return id.Format(id.Target, u) },
	"eventID":           func(u uuid.UUID) string { return id.Format(id.Event, u) },
	"endpointID":        func(u uuid.UUID) string { return id.Format(id.WebhookEndpoint, u) },
	"keyID":             func(u uuid.UUID) string { return id.Format(id.APIKey, u) },
	"userID":            func(u uuid.UUID) string { return id.Format(id.User, u) },
	"invitationID":      func(u uuid.UUID) string { return id.Format(id.Invitation, u) },
	"deliveryID":        func(u uuid.UUID) string { return id.Format(id.Delivery, u) },
	"mediaID":           func(u uuid.UUID) string { return id.Format(id.Media, u) },
	"adAccountID":       func(u uuid.UUID) string { return id.Format(id.AdAccount, u) },
	"analyticsSourceID": func(u uuid.UUID) string { return id.Format(id.AnalyticsSource, u) },
	"mailAccountID":     func(u uuid.UUID) string { return id.Format(id.MailAccount, u) },
	"mailConnID":        func(u uuid.UUID) string { return id.Format(id.MailConnection, u) },
	"issueID":           func(u uuid.UUID) string { return id.Format(id.Issue, u) },
	"appID":             func(u uuid.UUID) string { return id.Format(id.ProviderApp, u) },
	"notificationID":    func(u uuid.UUID) string { return id.Format(id.Notification, u) },
	"shareID":           func(u uuid.UUID) string { return id.Format(id.ReportShare, u) },
	"money":             money,
	"change":            change,
	"changeClass":       changeClass,
	"fmtPeriod":         fmtPeriod,
	// duration shows a video's length in milliseconds as m:ss.
	"duration": func(ms int64) string {
		s := (ms + 500) / 1000
		return fmt.Sprintf("%d:%02d", s/60, s%60)
	},
	"stat": func(label string, p core.Pair, lowerBetter bool) statCard {
		return statCard{Label: label, Pair: p, LowerBetter: lowerBetter}
	},
	"moneyStat": func(label string, p core.Pair, currency string, lowerBetter bool) statCard {
		return statCard{Label: label, Pair: p, Currency: currency, LowerBetter: lowerBetter}
	},
	// reportView is a report for the reportBody partial; public leaves out
	// links into the dashboard.
	"reportView": func(r *core.Report, public bool) reportView { return reportView{R: r, Public: public} },
	// issueResults adds up an issue's deliveries' results.
	"issueResults": func(is *model.Issue) model.MailResults {
		var r model.MailResults
		for _, d := range is.Deliveries {
			r.Recipients += d.Results.Recipients
			r.Delivered += d.Results.Delivered
			r.Opens += d.Results.Opens
			r.Clicks += d.Results.Clicks
			r.Unsubscribes += d.Results.Unsubscribes
		}
		return r
	},
	// percent is n of total, as "4.2%".
	"percent": func(n, total int64) string {
		if total == 0 {
			return "—"
		}
		return strconv.FormatFloat(float64(n)*100/float64(total), 'f', 1, 64) + "%"
	},
	// fmtIn shows a time in a time zone, with UTC beside it.
	"fmtIn": func(t *time.Time, tz string) string {
		if t == nil {
			return "—"
		}
		loc, err := time.LoadLocation(tz)
		if err != nil || loc == time.UTC {
			return t.UTC().Format("Jan 2, 2006 15:04 UTC")
		}
		return t.In(loc).Format("Jan 2, 2006 15:04 MST") + " (" + t.UTC().Format("15:04 UTC") + ")"
	},
	"costPer": core.CostPer,
	"rowID": func(group string, u *uuid.UUID) string {
		return core.EngagementRowID(core.EngagementGroup(group), u)
	},
	// engagement adds up a post's latest readings; "" before any.
	"engagement": func(ts []model.Target) string {
		var total int64
		read := false
		for _, t := range ts {
			if t.Engagement != nil && t.Engagement.ReadAt != nil {
				total += t.Engagement.Total()
				read = true
			}
		}
		if !read {
			return ""
		}
		return strconv.FormatInt(total, 10)
	},
	"fileSize": func(n int64) string {
		switch {
		case n >= 1_000_000:
			return fmt.Sprintf("%.1f MB", float64(n)/1_000_000)
		case n >= 1000:
			return fmt.Sprintf("%d kB", n/1000)
		}
		return fmt.Sprintf("%d bytes", n)
	},
	"join": strings.Join,
	"icon": func(provider any) template.HTML {
		p := fmt.Sprint(provider)
		if p == string(platform.LinkedInPages) {
			p = string(platform.LinkedIn) // the same mark
		}
		if !iconNames[p] {
			p = "sandbox"
		}
		return template.HTML(`<svg class="icon" aria-hidden="true" focusable="false"><use href="#i-` + p + `"/></svg>`) //nolint:gosec // p is from a fixed list
	},
	"contains":   func(list []string, v string) bool { return slices.Contains(list, v) },
	"list":       func(v ...string) []string { return v },
	"trimSchema": func(a string) string { return strings.TrimPrefix(a, "schema-") },
	"expired":    func(p *time.Time) bool { return p != nil && time.Now().After(*p) },
	"fmtDate": func(p *time.Time) string {
		if p == nil {
			return ""
		}
		return p.UTC().Format("Jan 2, 2006")
	},
	"derefTime": func(p *time.Time) time.Time {
		if p == nil {
			return time.Time{}
		}
		return *p
	},
	"derefUUID": func(p *uuid.UUID) uuid.UUID {
		if p == nil {
			return uuid.Nil
		}
		return *p
	},
	"statusClass":  statusClass,
	"brandOptions": brandOptions,
	"fieldInput":   fieldInput,
	"inc":          func(i int) int { return i + 1 },
	"deref": func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	},
	"safeURL": func(s string) template.URL {
		if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "/") {
			return template.URL(s) //nolint:gosec // scheme checked above
		}
		return ""
	},
	"dataURL": func(s string) template.URL {
		if strings.HasPrefix(s, "data:image/png;base64,") {
			return template.URL(s) //nolint:gosec // generated PNG
		}
		return ""
	},
}

// iconNames are the symbols in templates/_icons.html.
var iconNames = map[string]bool{"x": true, "bluesky": true, "mastodon": true, "gab": true, "threads": true, "linkedin": true, "pinterest": true, "youtube": true, "tiktok": true,
	"facebook": true, "instagram": true, "discord": true, "telegram": true, "sandbox": true}

func human(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
