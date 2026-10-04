// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/opsched"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// API keys.

type keysData struct {
	Keys   []*model.APIKey
	Brands []*model.Brand
	Scopes []core.Permission
	// Admin are the explicit-only scopes a member can add (ADR 0019).
	Admin   []core.Permission
	NewKey  string
	NewName string
	// Rolled says NewKey replaced a key whose old secret still works.
	Rolled  bool
	BaseURL string
	// CLI names the computer `araldo auth login` opened this page from
	// (ADR 0028): the page says so and suggests a name for the key.
	CLI string
}

// cliDevice reads the computer `araldo auth login` named, as sent in the
// cli parameter: printable, and short enough for a key name.
func cliDevice(c *reqCtx) string {
	v := c.r.FormValue("cli")
	out := make([]rune, 0, len(v))
	for _, r := range v {
		if unicode.IsPrint(r) {
			out = append(out, r)
		}
		if len(out) == 60 {
			break
		}
	}
	return strings.TrimSpace(string(out))
}

func (s *Server) keysData(c *reqCtx) (*keysData, error) {
	keys, err := s.svc.APIKeys(c.ctx(), c.actor)
	if err != nil {
		return nil, err
	}
	d := &keysData{Scopes: core.KeyScopes, Admin: core.AdminScopes, BaseURL: baseURL(c), CLI: cliDevice(c)}
	for _, k := range keys {
		if k.Livemode == c.actor.Livemode {
			d.Keys = append(d.Keys, k)
		}
	}
	if d.Brands, err = s.svc.Brands(c.ctx(), c.actor); err != nil {
		return nil, err
	}
	return d, nil
}

func baseURL(c *reqCtx) string {
	scheme := "https"
	if c.r.TLS == nil && c.r.Header.Get("X-Forwarded-Proto") != "https" && strings.HasPrefix(c.r.Host, "localhost") {
		scheme = "http"
	}
	return scheme + "://" + c.r.Host
}

func (s *Server) keys(c *reqCtx) error {
	d, err := s.keysData(c)
	if err != nil {
		return err
	}
	return s.page(c, "keys", "developers", "API keys", d)
}

func (s *Server) createKey(c *reqCtx) error {
	d, err := s.keysData(c)
	if err != nil {
		return err
	}
	in := core.APIKeyInput{Name: c.r.PostFormValue("name"), Livemode: c.actor.Livemode, Scopes: c.r.PostForm["scopes"]}
	if c.r.PostFormValue("access") == "full" {
		in.Scopes = nil
	}
	if admin := c.r.PostForm["admin"]; len(admin) > 0 {
		// Admin scopes are held only when listed, and listing any scope
		// ends "full access": spell the integration scopes out.
		if len(in.Scopes) == 0 {
			for _, p := range core.KeyScopes {
				in.Scopes = append(in.Scopes, string(p))
			}
		}
		in.Scopes = append(in.Scopes, admin...)
	}
	expires, err := keyExpiry(c.r.PostFormValue("expires"), c.r.PostFormValue("expires_on"), time.Now())
	if err != nil {
		return s.formErr(c, "keys", "developers", "API keys", d, err)
	}
	in.Expires = expires
	if ref := c.r.PostFormValue("brand"); ref != "" {
		b, err := s.svc.ResolveBrand(c.ctx(), c.actor, ref)
		if err != nil {
			return s.formErr(c, "keys", "developers", "API keys", d, err)
		}
		in.BrandID = &b.ID
	}
	plain, k, err := s.svc.CreateAPIKey(c.ctx(), c.actor, c.session, in)
	if err != nil {
		if apperr.As(err).Code == "reauthentication_required" {
			next := "/keys"
			if d.CLI != "" {
				next += "?cli=" + url.QueryEscape(d.CLI)
			}
			return redirect(c, "/confirm?next="+url.QueryEscape(next), "Confirm your password to create a key.")
		}
		return s.formErr(c, "keys", "developers", "API keys", d, err)
	}
	d.NewKey, d.NewName = plain, k.Name
	d.Keys = append([]*model.APIKey{k}, d.Keys...)
	return s.page(c, "keys", "developers", "API keys", d)
}

// keyExpiry turns the form's choice into an expiry time (nil: never).
func keyExpiry(choice, on string, now time.Time) (*time.Time, error) {
	var t time.Time
	switch choice {
	case "", "never":
		return nil, nil
	case "30d":
		t = now.AddDate(0, 0, 30)
	case "90d":
		t = now.AddDate(0, 0, 90)
	case "1y":
		t = now.AddDate(1, 0, 0)
	case "date":
		d, err := time.Parse("2006-01-02", on)
		if err != nil {
			return nil, apperr.Invalid("expires_invalid", "expires_on", "Pick the date the key stops working.")
		}
		t = d.Add(24*time.Hour - time.Second) // the end of that day, UTC
	default:
		return nil, apperr.Invalid("expires_invalid", "expires", "Choose when the key expires.")
	}
	if !t.After(now) {
		return nil, apperr.Invalid("expires_invalid", "expires_on", "The expiry date must be in the future.")
	}
	return &t, nil
}

type keyDetailData struct {
	Key       *model.APIKey
	Brand     *model.Brand
	Creator   *actorRef
	Posts     []*model.Post
	CanManage bool
}

// keyDetail shows a key's definition (never its secret) and the posts it
// made.
func (s *Server) keyDetail(c *reqCtx) error {
	kid, err := pathUUID(c, id.APIKey, "API key")
	if err != nil {
		return err
	}
	k, err := s.svc.APIKey(c.ctx(), c.actor, kid)
	if err != nil {
		return err
	}
	d := keyDetailData{Key: k, Creator: s.whoIs(c, k.CreatedBy, nil), CanManage: c.actor.Can(core.PermKeysWrite)}
	if k.BrandID != nil {
		d.Brand, _ = s.svc.Brand(c.ctx(), c.actor, *k.BrandID)
	}
	if k.Livemode == c.actor.Livemode {
		d.Posts, _, err = s.svc.Posts(c.ctx(), c.actor, core.PostFilter{CreatedByKey: &k.ID}, store.Page{Limit: 20})
		if err != nil {
			return err
		}
	}
	return s.page(c, "key_detail", "developers", "API key", d)
}

type memberDetailData struct {
	Member *model.Membership
	Posts  []*model.Post
	IsYou  bool
}

// memberDetail shows a member's profile and the posts they made.
func (s *Server) memberDetail(c *reqCtx) error {
	uid, err := pathUUID(c, id.User, "member")
	if err != nil {
		return err
	}
	m, err := s.svc.Member(c.ctx(), c.actor, uid)
	if err != nil {
		return err
	}
	d := memberDetailData{Member: m, IsYou: uid == c.user.ID}
	d.Posts, _, err = s.svc.Posts(c.ctx(), c.actor, core.PostFilter{CreatedByUser: &uid}, store.Page{Limit: 20})
	if err != nil {
		return err
	}
	title := m.UserEmail
	if m.UserName != "" {
		title = m.UserName
	}
	return s.page(c, "member_detail", "org", title, d)
}

// RollOverlap is how long a rolled key's old secret keeps working, so the
// new one can be deployed without downtime.
const RollOverlap = 24 * time.Hour

// rollKey replaces a key's secret; the old one works for RollOverlap.
func (s *Server) rollKey(c *reqCtx) error {
	kid, err := pathUUID(c, id.APIKey, "API key")
	if err != nil {
		return err
	}
	plain, k, err := s.svc.RollAPIKey(c.ctx(), c.actor, c.session, kid, RollOverlap)
	if err != nil {
		if apperr.As(err).Code == "reauthentication_required" {
			return redirect(c, "/confirm?next=/keys", "Confirm your password to roll a key.")
		}
		return err
	}
	d, err := s.keysData(c)
	if err != nil {
		return err
	}
	d.NewKey, d.NewName, d.Rolled = plain, k.Name, true
	return s.page(c, "keys", "developers", "API keys", d)
}

func (s *Server) revokeKey(c *reqCtx) error {
	kid, err := pathUUID(c, id.APIKey, "API key")
	if err != nil {
		return err
	}
	if err := s.svc.RevokeAPIKey(c.ctx(), c.actor, kid); err != nil {
		return err
	}
	return redirect(c, "/keys", "Key revoked.")
}

// Webhooks.

type webhooksData struct {
	Endpoints  []*model.WebhookEndpoint
	EventTypes []string
	Secret     string
	SecretFor  string
	Form       map[string]string
}

func (s *Server) webhooks(c *reqCtx) error {
	eps, err := s.svc.Endpoints(c.ctx(), c.actor)
	if err != nil {
		return err
	}
	return s.page(c, "webhooks", "developers", "Webhooks", webhooksData{Endpoints: eps, EventTypes: core.EventTypes, Form: map[string]string{}})
}

func (s *Server) createWebhook(c *reqCtx) error {
	eps, err := s.svc.Endpoints(c.ctx(), c.actor)
	if err != nil {
		return err
	}
	d := webhooksData{Endpoints: eps, EventTypes: core.EventTypes, Form: map[string]string{"url": c.r.PostFormValue("url"), "description": c.r.PostFormValue("description")}}
	types := c.r.PostForm["events"]
	if len(types) == 0 || c.r.PostFormValue("all") == "1" {
		types = []string{"*"}
	}
	ep, secret, err := s.svc.CreateEndpoint(c.ctx(), c.actor, core.EndpointInput{URL: d.Form["url"], Description: d.Form["description"], EventTypes: types})
	if err != nil {
		return s.formErr(c, "webhooks", "developers", "Webhooks", d, err)
	}
	d.Endpoints = append(d.Endpoints, ep)
	d.Secret, d.SecretFor, d.Form = secret, ep.URL, map[string]string{}
	return s.page(c, "webhooks", "developers", "Webhooks", d)
}

type webhookDetailData struct {
	Endpoint   *model.WebhookEndpoint
	Deliveries []model.Delivery
	EventTypes []string
	Secret     string
}

func (s *Server) webhookDetail(c *reqCtx) error {
	eid, err := pathUUID(c, id.WebhookEndpoint, "webhook endpoint")
	if err != nil {
		return err
	}
	ep, err := s.svc.Endpoint(c.ctx(), c.actor, eid)
	if err != nil {
		return err
	}
	ds, _, err := s.svc.Deliveries(c.ctx(), c.actor, eid, store.Page{Limit: 50})
	if err != nil {
		return err
	}
	return s.page(c, "webhook_detail", "developers", "Webhook endpoint", webhookDetailData{Endpoint: ep, Deliveries: ds, EventTypes: core.EventTypes})
}

func (s *Server) updateWebhook(c *reqCtx) error {
	eid, err := pathUUID(c, id.WebhookEndpoint, "webhook endpoint")
	if err != nil {
		return err
	}
	enabled := c.r.PostFormValue("enabled") == "1"
	types := c.r.PostForm["events"]
	if len(types) == 0 || c.r.PostFormValue("all") == "1" {
		types = []string{"*"}
	}
	if _, err := s.svc.UpdateEndpoint(c.ctx(), c.actor, eid, core.EndpointInput{URL: c.r.PostFormValue("url"),
		Description: c.r.PostFormValue("description"), EventTypes: types, Enabled: &enabled}); err != nil {
		return err
	}
	return redirect(c, "/webhooks/"+c.r.PathValue("id"), "Saved.")
}

func (s *Server) rollWebhook(c *reqCtx) error {
	eid, err := pathUUID(c, id.WebhookEndpoint, "webhook endpoint")
	if err != nil {
		return err
	}
	secret, err := s.svc.RollEndpointSecret(c.ctx(), c.actor, eid)
	if err != nil {
		return err
	}
	ep, err := s.svc.Endpoint(c.ctx(), c.actor, eid)
	if err != nil {
		return err
	}
	ds, _, err := s.svc.Deliveries(c.ctx(), c.actor, eid, store.Page{Limit: 50})
	if err != nil {
		return err
	}
	return s.page(c, "webhook_detail", "developers", "Webhook endpoint", webhookDetailData{Endpoint: ep, Deliveries: ds, EventTypes: core.EventTypes, Secret: secret})
}

func (s *Server) deleteWebhook(c *reqCtx) error {
	eid, err := pathUUID(c, id.WebhookEndpoint, "webhook endpoint")
	if err != nil {
		return err
	}
	if err := s.svc.DeleteEndpoint(c.ctx(), c.actor, eid); err != nil {
		return err
	}
	return redirect(c, "/webhooks", "Endpoint deleted.")
}

func (s *Server) resendDelivery(c *reqCtx) error {
	did, err := pathUUID(c, id.Delivery, "webhook delivery")
	if err != nil {
		return err
	}
	if err := s.svc.ResendDelivery(c.ctx(), c.actor, did); err != nil {
		return err
	}
	return redirect(c, safeNext(c.r.PostFormValue("back")), "Queued for delivery.")
}

// Events.

type eventsData struct {
	Events  []*model.Event
	HasMore bool
	Next    string
	Type    string
	Types   []string
}

func (s *Server) events(c *reqCtx) error {
	q := c.r.URL.Query()
	pg := store.Page{Limit: 50}
	if u, err := id.Parse(id.Event, q.Get("after")); err == nil {
		pg.StartingAfter = u
	}
	evs, more, err := s.svc.Events(c.ctx(), c.actor, q.Get("type"), pg)
	if err != nil {
		return err
	}
	d := eventsData{Events: evs, HasMore: more, Type: q.Get("type"), Types: core.EventTypes}
	if more && len(evs) > 0 {
		d.Next = id.Format(id.Event, evs[len(evs)-1].ID)
	}
	return s.page(c, "events", "developers", "Events", d)
}

func (s *Server) eventDetail(c *reqCtx) error {
	eid, err := pathUUID(c, id.Event, "event")
	if err != nil {
		return err
	}
	e, err := s.svc.Event(c.ctx(), c.actor, eid)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(core.ViewEvent(e), "", "  ")
	return s.page(c, "event_detail", "developers", e.Type, map[string]any{"Event": e, "JSON": string(b)})
}

// Org.

type orgData struct {
	Org     *model.Org
	Members []model.Membership
	Roles   []model.Role
	TempPW  string
	IsOwner bool
	IsAdmin bool
}

func (s *Server) orgPage(c *reqCtx) error {
	d, err := s.orgData(c)
	if err != nil {
		return err
	}
	return s.page(c, "org", "org", "Organization", d)
}

func (s *Server) orgData(c *reqCtx) (*orgData, error) {
	ms, err := s.svc.Members(c.ctx(), c.actor)
	if err != nil {
		return nil, err
	}
	return &orgData{Org: c.org, Members: ms, Roles: []model.Role{model.RoleOwner, model.RoleAdmin, model.RoleEditor, model.RoleViewer},
		IsOwner: c.actor.Can(core.PermOrgWrite), IsAdmin: c.actor.Can(core.PermMembersWrite)}, nil
}

func (s *Server) saveOrg(c *reqCtx) error {
	if err := s.svc.UpdateOrg(c.ctx(), c.actor, c.r.PostFormValue("name"), c.r.PostFormValue("require_mfa") == "1"); err != nil {
		d, derr := s.orgData(c)
		if derr != nil {
			return derr
		}
		return s.formErr(c, "org", "org", "Organization", d, err)
	}
	return redirect(c, "/org", "Saved.")
}

func (s *Server) addMember(c *reqCtx) error {
	d, err := s.orgData(c)
	if err != nil {
		return err
	}
	pw := c.r.PostFormValue("password")
	u, err := s.svc.AddMember(c.ctx(), c.actor, c.r.PostFormValue("email"), model.Role(c.r.PostFormValue("role")), pw)
	if err != nil {
		return s.formErr(c, "org", "org", "Organization", d, err)
	}
	return redirect(c, "/org", "Added "+u.Email+".")
}

func (s *Server) changeMember(c *reqCtx) error {
	uid, err := uuid.Parse(c.r.PathValue("id"))
	if err != nil {
		if u, perr := id.Parse(id.User, c.r.PathValue("id")); perr == nil {
			uid = u
		} else {
			return apperr.NotFound("member")
		}
	}
	if c.r.PostFormValue("action") == "remove" {
		if err := s.svc.RemoveMember(c.ctx(), c.actor, uid); err != nil {
			return err
		}
		return redirect(c, "/org", "Removed.")
	}
	if err := s.svc.SetMemberRole(c.ctx(), c.actor, uid, model.Role(c.r.PostFormValue("role"))); err != nil {
		return err
	}
	return redirect(c, "/org", "Role changed.")
}

func (s *Server) audit(c *reqCtx) error {
	pg := store.Page{Limit: 100}
	if u, err := id.Parse(id.Event, c.r.URL.Query().Get("after")); err == nil {
		pg.StartingAfter = u
	}
	evs, _, err := s.svc.AuditEvents(c.ctx(), c.actor, pg)
	if err != nil {
		return err
	}
	return s.page(c, "audit", "org", "Audit log", evs)
}

func (s *Server) tasks(c *reqCtx) error {
	ts, err := s.svc.TaskStatuses(c.ctx(), c.actor)
	if err != nil {
		return err
	}
	return s.page(c, "tasks", "org", "Background tasks", struct {
		Tasks []opsched.TaskStatus
		Now   time.Time
	}{ts, time.Now()})
}
