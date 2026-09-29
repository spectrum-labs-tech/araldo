// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
	"github.com/spectrum-labs-tech/araldo/internal/tmpl"
)

func (s *Server) routes() {
	static, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))

	s.mux.HandleFunc("GET /login", s.loginPage)
	s.mux.HandleFunc("POST /login", s.loginSubmit)
	s.mux.HandleFunc("GET /login/mfa", s.mfaPage)
	s.mux.HandleFunc("POST /login/mfa", s.mfaSubmit)
	s.mux.HandleFunc("POST /logout", s.logout)
	s.mux.HandleFunc("GET /confirm", s.confirmPage)
	s.mux.HandleFunc("POST /confirm", s.confirmSubmit)
	s.mux.HandleFunc("GET /onboarding", s.onboarding)
	s.mux.HandleFunc("POST /onboarding", s.onboarding)

	s.mux.HandleFunc("GET /{$}", s.app("home", s.home))
	s.mux.HandleFunc("POST /context", s.app("", s.switchContext))

	s.mux.HandleFunc("GET /posts", s.app("posts", s.posts))
	s.mux.HandleFunc("GET /posts/new", s.app("posts", s.newPost))
	s.mux.HandleFunc("POST /posts", s.app("posts", s.createPost))
	s.mux.HandleFunc("GET /posts/{id}", s.app("posts", s.postDetail))
	s.mux.HandleFunc("POST /posts/{id}/cancel", s.app("posts", s.cancelPost))
	s.mux.HandleFunc("POST /posts/{id}/review", s.app("posts", s.reviewPost))
	s.mux.HandleFunc("POST /targets/{id}/retry", s.app("posts", s.retryTarget))
	s.mux.HandleFunc("POST /targets/{id}/published", s.app("posts", s.markPublished))

	s.mux.HandleFunc("GET /templates", s.app("templates", s.templates))
	s.mux.HandleFunc("GET /templates/new", s.app("templates", s.newTemplate))
	s.mux.HandleFunc("POST /templates", s.app("templates", s.createTemplate))
	s.mux.HandleFunc("GET /templates/{id}", s.app("templates", s.templateDetail))
	s.mux.HandleFunc("POST /templates/{id}", s.app("templates", s.saveTemplate))
	s.mux.HandleFunc("POST /templates/{id}/delete", s.app("templates", s.deleteTemplate))
	s.mux.HandleFunc("POST /preview", s.app("templates", s.previewJSON))

	s.mux.HandleFunc("GET /channels", s.app("channels", s.channels))
	s.mux.HandleFunc("GET /channels/new", s.app("channels", s.newChannel))
	s.mux.HandleFunc("POST /channels", s.app("channels", s.createChannel))
	s.mux.HandleFunc("POST /channels/{id}/toggle", s.app("channels", s.toggleChannel))
	s.mux.HandleFunc("POST /channels/{id}/delete", s.app("channels", s.deleteChannel))
	s.mux.HandleFunc("GET /channels/{id}/reconnect", s.app("channels", s.reconnectChannel))
	s.mux.HandleFunc("POST /channels/{id}/reconnect", s.app("channels", s.reconnectChannel))

	s.mux.HandleFunc("GET /brands", s.app("brands", s.brands))
	s.mux.HandleFunc("GET /brands/new", s.app("brands", s.newBrand))
	s.mux.HandleFunc("POST /brands", s.app("brands", s.createBrand))
	s.mux.HandleFunc("GET /brands/{id}", s.app("brands", s.brandDetail))
	s.mux.HandleFunc("POST /brands/{id}", s.app("brands", s.saveBrand))

	s.mux.HandleFunc("GET /keys", s.app("developers", s.keys))
	s.mux.HandleFunc("POST /keys", s.app("developers", s.createKey))
	s.mux.HandleFunc("POST /keys/{id}/revoke", s.app("developers", s.revokeKey))
	s.mux.HandleFunc("GET /webhooks", s.app("developers", s.webhooks))
	s.mux.HandleFunc("POST /webhooks", s.app("developers", s.createWebhook))
	s.mux.HandleFunc("GET /webhooks/{id}", s.app("developers", s.webhookDetail))
	s.mux.HandleFunc("POST /webhooks/{id}", s.app("developers", s.updateWebhook))
	s.mux.HandleFunc("POST /webhooks/{id}/roll", s.app("developers", s.rollWebhook))
	s.mux.HandleFunc("POST /webhooks/{id}/delete", s.app("developers", s.deleteWebhook))
	s.mux.HandleFunc("POST /deliveries/{id}/resend", s.app("developers", s.resendDelivery))
	s.mux.HandleFunc("GET /events", s.app("developers", s.events))
	s.mux.HandleFunc("GET /events/{id}", s.app("developers", s.eventDetail))

	s.mux.HandleFunc("GET /org", s.app("org", s.orgPage))
	s.mux.HandleFunc("POST /org", s.app("org", s.saveOrg))
	s.mux.HandleFunc("POST /org/members", s.app("org", s.addMember))
	s.mux.HandleFunc("POST /org/members/{id}", s.app("org", s.changeMember))
	s.mux.HandleFunc("GET /org/audit", s.app("org", s.audit))
	s.mux.HandleFunc("GET /org/tasks", s.app("org", s.tasks))

	s.mux.HandleFunc("GET /account", s.app("account", s.accountPage))
	s.mux.HandleFunc("POST /account/mfa/begin", s.app("account", s.accountMFABegin))
	s.mux.HandleFunc("POST /account/mfa/confirm", s.app("account", s.accountMFAConfirm))
	s.mux.HandleFunc("POST /account/mfa/disable", s.app("account", s.accountMFADisable))
	s.mux.HandleFunc("POST /account/recovery-codes", s.app("account", s.accountRecoveryCodes))
	s.mux.HandleFunc("POST /account/password", s.app("account", s.accountPassword))

	s.mux.HandleFunc("GET /sandbox/{id}", s.app("posts", s.sandboxPost))
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		h.ServeHTTP(w, r)
	})
}

func pathUUID(c *reqCtx, p id.Prefix, what string) (uuid.UUID, error) {
	return core.ParseID(p, c.r.PathValue("id"), what)
}

// Home.

type homeData struct {
	Brands   []*model.Brand
	Channels []*model.Channel
	Posts    []*model.Post
	Keys     int
	Queue    store.QueueStats
	Steps    []step
}

type step struct {
	Label, Href string
	Done        bool
}

func (s *Server) home(c *reqCtx) error {
	d := homeData{}
	var err error
	if d.Brands, err = s.svc.Brands(c.ctx(), c.actor); err != nil {
		return err
	}
	if d.Channels, err = s.svc.Channels(c.ctx(), c.actor, nil); err != nil {
		return err
	}
	if d.Posts, _, err = s.svc.Posts(c.ctx(), c.actor, core.PostFilter{}, store.Page{Limit: 8}); err != nil {
		return err
	}
	if c.actor.Can(core.PermKeysWrite) {
		keys, err := s.svc.APIKeys(c.ctx(), c.actor)
		if err != nil {
			return err
		}
		for _, k := range keys {
			if k.Livemode == c.actor.Livemode && k.RevokedAt == nil {
				d.Keys++
			}
		}
	}
	if d.Queue, err = s.svc.QueueStats(c.ctx()); err != nil {
		return err
	}
	mode := "test"
	if c.actor.Livemode {
		mode = "live"
	}
	d.Steps = []step{
		{"Create a brand", "/brands/new", len(d.Brands) > 0},
		{"Connect a " + mode + "-mode channel", "/channels/new", len(d.Channels) > 0},
		{"Create a " + mode + " API key", "/keys", d.Keys > 0},
		{"Publish your first post", "/posts/new", len(d.Posts) > 0},
		{"Turn on two-factor authentication", "/account", c.user.MFAEnabled()},
	}
	return s.page(c, "home", "home", "Overview", d)
}

// Posts.

type postsData struct {
	Posts   []*model.Post
	HasMore bool
	Status  string
	Next    string
}

func (s *Server) posts(c *reqCtx) error {
	q := c.r.URL.Query()
	pg := store.Page{Limit: 25}
	if v := q.Get("after"); v != "" {
		if u, err := id.Parse(id.Post, v); err == nil {
			pg.StartingAfter = u
		}
	}
	posts, more, err := s.svc.Posts(c.ctx(), c.actor, core.PostFilter{Status: q.Get("status")}, pg)
	if err != nil {
		return err
	}
	d := postsData{Posts: posts, HasMore: more, Status: q.Get("status")}
	if more && len(posts) > 0 {
		d.Next = id.Format(id.Post, posts[len(posts)-1].ID)
	}
	return s.page(c, "posts", "posts", "Posts", d)
}

type newPostData struct {
	Brands     []*model.Brand
	Channels   []*model.Channel
	Templates  []*model.Template
	Form       map[string]string
	Chosen     []string
	Renditions []core.Rendition
}

func (s *Server) newPostData(c *reqCtx) (*newPostData, error) {
	d := &newPostData{Form: map[string]string{"publish": "now", "mode": "content"}}
	var err error
	if d.Brands, err = s.svc.Brands(c.ctx(), c.actor); err != nil {
		return nil, err
	}
	if d.Channels, err = s.svc.Channels(c.ctx(), c.actor, nil); err != nil {
		return nil, err
	}
	if d.Templates, err = s.svc.Templates(c.ctx(), c.actor, nil); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Server) newPost(c *reqCtx) error {
	d, err := s.newPostData(c)
	if err != nil {
		return err
	}
	if t := c.r.URL.Query().Get("template"); t != "" {
		d.Form["mode"], d.Form["template"] = "template", t
	}
	return s.page(c, "post_new", "posts", "New post", d)
}

func (s *Server) createPost(c *reqCtx) error {
	d, err := s.newPostData(c)
	if err != nil {
		return err
	}
	f := c.r.PostForm
	for _, k := range []string{"brand", "mode", "body", "template", "data", "publish", "publish_at", "fit"} {
		d.Form[k] = f.Get(k)
	}
	d.Chosen = f["channels"]
	in := core.PostInput{PublishAt: f.Get("publish")}
	brandRef := f.Get("brand")
	var tplBrand uuid.UUID
	if d.Form["mode"] == "template" {
		tid, err := id.Parse(id.Template, f.Get("template"))
		if err != nil {
			return s.formErr(c, "post_new", "posts", "New post", d, apperr.Invalid("template_required", "template", "Choose a template."))
		}
		t, _, err := s.svc.Template(c.ctx(), c.actor, tid, 0)
		if err != nil {
			return s.formErr(c, "post_new", "posts", "New post", d, err)
		}
		in.Template, tplBrand = id.Format(id.Template, t.ID), t.BrandID
		in.Data = json.RawMessage(strings.TrimSpace(f.Get("data")))
		if len(in.Data) == 0 {
			in.Data = json.RawMessage("{}")
		}
	} else {
		in.Content = &model.Content{Body: f.Get("body")}
		if fit := platform.Fit(f.Get("fit")); fit == platform.FitThread || fit == platform.FitTruncate {
			in.Content.Fit = map[platform.Provider]platform.Fit{}
			for _, p := range platform.Emulable() {
				in.Content.Fit[p] = fit
			}
		}
	}
	if tplBrand != uuid.Nil {
		in.BrandID = tplBrand
	} else {
		b, err := s.svc.ResolveBrand(c.ctx(), c.actor, brandRef)
		if err != nil {
			return s.formErr(c, "post_new", "posts", "New post", d, apperr.Invalid("brand_required", "brand", "Choose a brand."))
		}
		in.BrandID = b.ID
	}
	for _, ref := range d.Chosen {
		if u, err := id.Parse(id.Channel, ref); err == nil {
			in.Channels = append(in.Channels, u)
		}
	}
	if in.PublishAt == "at" {
		t, err := time.Parse("2006-01-02T15:04", f.Get("publish_at"))
		if err != nil {
			return s.formErr(c, "post_new", "posts", "New post", d, apperr.Invalid("publish_at_invalid", "publish_at", "Pick a date and time."))
		}
		b, _ := s.svc.Brand(c.ctx(), c.actor, in.BrandID)
		loc := time.UTC
		if b != nil {
			if l, err := time.LoadLocation(b.Timezone); err == nil {
				loc = l
			}
		}
		in.PublishAt = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc).Format(time.RFC3339)
	}
	if f.Get("action") == "preview" {
		renders, err := s.svc.PreviewPost(c.ctx(), c.actor, in)
		if err != nil {
			return s.formErr(c, "post_new", "posts", "New post", d, err)
		}
		d.Renditions = renders
		return s.page(c, "post_new", "posts", "New post", d)
	}
	p, err := s.svc.CreatePost(c.ctx(), c.actor, in)
	if err != nil {
		return s.formErr(c, "post_new", "posts", "New post", d, err)
	}
	return redirect(c, "/posts/"+id.Format(id.Post, p.ID), "Post scheduled.")
}

type postDetailData struct {
	Post       *model.Post
	View       core.PostView
	Brand      *model.Brand
	Attempts   map[uuid.UUID][]model.Attempt
	JSON       string
	CanApprove bool
}

func (s *Server) postDetail(c *reqCtx) error {
	pid, err := pathUUID(c, id.Post, "post")
	if err != nil {
		return err
	}
	p, err := s.svc.Post(c.ctx(), c.actor, pid)
	if err != nil {
		return err
	}
	d := postDetailData{Post: p, View: core.ViewPost(p), Attempts: map[uuid.UUID][]model.Attempt{}, CanApprove: c.actor.Can(core.PermPostsApprove)}
	d.Brand, _ = s.svc.Brand(c.ctx(), c.actor, p.BrandID)
	for _, t := range p.Targets {
		if d.Attempts[t.ID], err = s.svc.Attempts(c.ctx(), c.actor, t.ID); err != nil {
			return err
		}
	}
	b, _ := json.MarshalIndent(d.View, "", "  ")
	d.JSON = string(b)
	return s.page(c, "post_detail", "posts", "Post", d)
}

func (s *Server) cancelPost(c *reqCtx) error {
	pid, err := pathUUID(c, id.Post, "post")
	if err != nil {
		return err
	}
	if _, err := s.svc.CancelPost(c.ctx(), c.actor, pid); err != nil {
		return err
	}
	return redirect(c, "/posts/"+c.r.PathValue("id"), "Canceled.")
}

func (s *Server) reviewPost(c *reqCtx) error {
	pid, err := pathUUID(c, id.Post, "post")
	if err != nil {
		return err
	}
	approve := c.r.PostFormValue("decision") == "approve"
	if _, err := s.svc.ReviewPost(c.ctx(), c.actor, pid, approve, c.r.PostFormValue("note")); err != nil {
		return err
	}
	msg := "Rejected."
	if approve {
		msg = "Approved; it will publish on schedule."
	}
	return redirect(c, "/posts/"+c.r.PathValue("id"), msg)
}

func (s *Server) retryTarget(c *reqCtx) error {
	tid, err := pathUUID(c, id.Target, "post target")
	if err != nil {
		return err
	}
	p, err := s.svc.RetryTarget(c.ctx(), c.actor, tid)
	if err != nil {
		return err
	}
	return redirect(c, "/posts/"+id.Format(id.Post, p.ID), "Queued again.")
}

func (s *Server) markPublished(c *reqCtx) error {
	tid, err := pathUUID(c, id.Target, "post target")
	if err != nil {
		return err
	}
	p, err := s.svc.MarkTargetPublished(c.ctx(), c.actor, tid, c.r.PostFormValue("permalink"))
	if err != nil {
		return err
	}
	return redirect(c, "/posts/"+id.Format(id.Post, p.ID), "Marked published.")
}

func (s *Server) sandboxPost(c *reqCtx) error {
	t, err := s.svc.SandboxTarget(c.ctx(), c.r.PathValue("id"))
	if err != nil {
		return err
	}
	if t.OrgID != c.actor.OrgID {
		return apperr.NotFound("post target")
	}
	return s.page(c, "sandbox", "posts", "Sandbox post", t)
}

// Templates.

type templatesData struct {
	Templates []*model.Template
	Brands    map[uuid.UUID]string
}

func (s *Server) brandNames(c *reqCtx) (map[uuid.UUID]string, []*model.Brand, error) {
	bs, err := s.svc.Brands(c.ctx(), c.actor)
	if err != nil {
		return nil, nil, err
	}
	names := map[uuid.UUID]string{}
	for _, b := range bs {
		names[b.ID] = b.Name
	}
	return names, bs, nil
}

func (s *Server) templates(c *reqCtx) error {
	ts, err := s.svc.Templates(c.ctx(), c.actor, nil)
	if err != nil {
		return err
	}
	names, _, err := s.brandNames(c)
	if err != nil {
		return err
	}
	return s.page(c, "templates", "templates", "Templates", templatesData{Templates: ts, Brands: names})
}

type templateForm struct {
	Template  *model.Template
	Version   *model.TemplateVersion
	Brands    []*model.Brand
	Brand     string
	Key       string
	Name      string
	Body      string
	Variables string
	Example   string
	Overrides map[string]string
	Fit       map[string]string
	Providers []platform.Provider
}

var starterBody = "New on AR15.build: {{.name}}\n{{.url}}\n\n{{hashtags .tags}}"

const starterVariables = `{
  "type": "object",
  "required": ["name", "url"],
  "properties": {
    "name": {"type": "string"},
    "url": {"type": "string"},
    "tags": {"type": "array", "items": {"type": "string"}}
  }
}`

const starterExample = `{"name": "Recce build", "url": "https://example.com/builds/1", "tags": ["AR-15", "range day"]}`

func (s *Server) newTemplate(c *reqCtx) error {
	_, bs, err := s.brandNames(c)
	if err != nil {
		return err
	}
	f := templateForm{Brands: bs, Body: starterBody, Variables: starterVariables, Example: starterExample,
		Overrides: map[string]string{}, Fit: map[string]string{}, Providers: platform.Emulable()}
	return s.page(c, "template_edit", "templates", "New template", f)
}

func (s *Server) templateFromForm(c *reqCtx, f *templateForm) (tmpl.Source, error) {
	r := c.r
	f.Brand, f.Key, f.Name, f.Body = r.PostFormValue("brand"), r.PostFormValue("key"), r.PostFormValue("name"), r.PostFormValue("body")
	f.Variables, f.Example = r.PostFormValue("variables"), r.PostFormValue("example")
	f.Overrides, f.Fit, f.Providers = map[string]string{}, map[string]string{}, platform.Emulable()
	src := tmpl.Source{Body: f.Body, Overrides: map[platform.Provider]string{}, Fit: map[platform.Provider]platform.Fit{}}
	for _, p := range platform.Emulable() {
		if o := strings.TrimSpace(r.PostFormValue("override_" + string(p))); o != "" {
			f.Overrides[string(p)], src.Overrides[p] = o, o
		}
		if fit := r.PostFormValue("fit_" + string(p)); fit != "" && fit != "error" {
			f.Fit[string(p)], src.Fit[p] = fit, platform.Fit(fit)
		}
	}
	if v := strings.TrimSpace(f.Variables); v != "" {
		if !json.Valid([]byte(v)) {
			return src, apperr.Invalid("variables_invalid", "variables", "The variables schema is not valid JSON.")
		}
		src.Variables = json.RawMessage(v)
	}
	if ex := strings.TrimSpace(f.Example); ex != "" {
		if !json.Valid([]byte(ex)) {
			return src, apperr.Invalid("example_invalid", "example", "The example data is not valid JSON.")
		}
		src.Examples = []json.RawMessage{json.RawMessage(ex)}
	}
	return src, nil
}

func (s *Server) createTemplate(c *reqCtx) error {
	_, bs, err := s.brandNames(c)
	if err != nil {
		return err
	}
	f := templateForm{Brands: bs}
	src, err := s.templateFromForm(c, &f)
	if err != nil {
		return s.formErr(c, "template_edit", "templates", "New template", f, err)
	}
	b, err := s.svc.ResolveBrand(c.ctx(), c.actor, f.Brand)
	if err != nil {
		return s.formErr(c, "template_edit", "templates", "New template", f, apperr.Invalid("brand_required", "brand", "Choose a brand."))
	}
	t, _, err := s.svc.CreateTemplate(c.ctx(), c.actor, core.TemplateInput{BrandID: b.ID, Key: f.Key, Name: f.Name, Source: src})
	if err != nil {
		return s.formErr(c, "template_edit", "templates", "New template", f, err)
	}
	return redirect(c, "/templates/"+id.Format(id.Template, t.ID), "Template created (version 1).")
}

func (s *Server) templateDetail(c *reqCtx) error {
	tid, err := pathUUID(c, id.Template, "template")
	if err != nil {
		return err
	}
	version, _ := strconv.Atoi(c.r.URL.Query().Get("version"))
	t, v, err := s.svc.Template(c.ctx(), c.actor, tid, version)
	if err != nil {
		return err
	}
	f := templateForm{Template: t, Version: v, Key: t.Key, Name: t.Name, Body: v.Body, Overrides: map[string]string{}, Fit: map[string]string{},
		Providers: platform.Emulable()}
	if len(v.Variables) > 0 {
		var pretty any
		if json.Unmarshal(v.Variables, &pretty) == nil {
			b, _ := json.MarshalIndent(pretty, "", "  ")
			f.Variables = string(b)
		}
	}
	if len(v.Examples) > 0 {
		f.Example = string(v.Examples[0])
	}
	for p, o := range v.Overrides {
		f.Overrides[string(p)] = o
	}
	for p, fit := range v.Fit {
		f.Fit[string(p)] = string(fit)
	}
	return s.page(c, "template_edit", "templates", t.Key, f)
}

func (s *Server) saveTemplate(c *reqCtx) error {
	tid, err := pathUUID(c, id.Template, "template")
	if err != nil {
		return err
	}
	t, v, err := s.svc.Template(c.ctx(), c.actor, tid, 0)
	if err != nil {
		return err
	}
	f := templateForm{Template: t, Version: v}
	src, err := s.templateFromForm(c, &f)
	if err != nil {
		return s.formErr(c, "template_edit", "templates", t.Key, f, err)
	}
	f.Key = t.Key
	t, v, err = s.svc.AddTemplateVersion(c.ctx(), c.actor, tid, f.Name, src)
	if err != nil {
		return s.formErr(c, "template_edit", "templates", f.Key, f, err)
	}
	return redirect(c, "/templates/"+id.Format(id.Template, t.ID), "Saved as version "+strconv.Itoa(v.Version)+".")
}

func (s *Server) deleteTemplate(c *reqCtx) error {
	tid, err := pathUUID(c, id.Template, "template")
	if err != nil {
		return err
	}
	if err := s.svc.DeleteTemplate(c.ctx(), c.actor, tid); err != nil {
		return err
	}
	return redirect(c, "/templates", "Template deleted.")
}

// previewJSON renders the template form for the live preview.
func (s *Server) previewJSON(c *reqCtx) error {
	f := templateForm{}
	src, err := s.templateFromForm(c, &f)
	var renders []core.Rendition
	if err == nil {
		tz := "UTC"
		if b, berr := s.svc.ResolveBrand(c.ctx(), c.actor, f.Brand); berr == nil {
			tz = b.Timezone
		}
		renders, err = s.svc.PreviewTemplate(c.ctx(), c.actor, src, nil, nil, tz)
	}
	c.w.Header().Set("Content-Type", "application/json")
	c.w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		ae := apperr.As(err)
		if ae.Kind == apperr.KindInternal {
			return err
		}
		return json.NewEncoder(c.w).Encode(map[string]any{"error": ae.Message, "problems": ae.Problems})
	}
	return json.NewEncoder(c.w).Encode(map[string]any{"renditions": renders})
}

// Channels.

type channelsData struct {
	Channels []*model.Channel
	Brands   map[uuid.UUID]string
}

func (s *Server) channels(c *reqCtx) error {
	chs, err := s.svc.Channels(c.ctx(), c.actor, nil)
	if err != nil {
		return err
	}
	names, _, err := s.brandNames(c)
	if err != nil {
		return err
	}
	return s.page(c, "channels", "channels", "Channels", channelsData{Channels: chs, Brands: names})
}

type channelForm struct {
	Providers []core.ProviderInfo
	Provider  *core.ProviderInfo
	Brands    []*model.Brand
	Brand     string
	Values    map[string]string
	Emulable  []platform.Provider
	Channel   *model.Channel
}

func (s *Server) channelForm(c *reqCtx, provider string) (*channelForm, error) {
	_, bs, err := s.brandNames(c)
	if err != nil {
		return nil, err
	}
	f := &channelForm{Providers: s.svc.Providers(c.actor.Livemode), Brands: bs, Values: map[string]string{}, Emulable: platform.Emulable()}
	for i := range f.Providers {
		if string(f.Providers[i].Provider) == provider || len(f.Providers) == 1 {
			f.Provider = &f.Providers[i]
		}
	}
	return f, nil
}

func (s *Server) newChannel(c *reqCtx) error {
	f, err := s.channelForm(c, c.r.URL.Query().Get("provider"))
	if err != nil {
		return err
	}
	f.Brand = c.r.URL.Query().Get("brand")
	return s.page(c, "channel_new", "channels", "Connect a channel", f)
}

func (s *Server) createChannel(c *reqCtx) error {
	f, err := s.channelForm(c, c.r.PostFormValue("provider"))
	if err != nil {
		return err
	}
	f.Brand = c.r.PostFormValue("brand")
	if f.Provider == nil {
		return s.formErr(c, "channel_new", "channels", "Connect a channel", f, apperr.Invalid("provider_required", "provider", "Choose a platform."))
	}
	fields := map[string]string{}
	for _, fd := range f.Provider.Fields {
		fields[fd.Name] = c.r.PostFormValue("field_" + fd.Name)
		if !fd.Secret {
			f.Values[fd.Name] = fields[fd.Name]
		}
	}
	b, err := s.svc.ResolveBrand(c.ctx(), c.actor, f.Brand)
	if err != nil {
		return s.formErr(c, "channel_new", "channels", "Connect a channel", f, apperr.Invalid("brand_required", "brand", "Choose a brand."))
	}
	ch, err := s.svc.ConnectChannel(c.ctx(), c.actor, core.ConnectInput{BrandID: b.ID, Provider: f.Provider.Provider, Fields: fields})
	if err != nil {
		return s.formErr(c, "channel_new", "channels", "Connect a channel", f, err)
	}
	return redirect(c, "/channels", "Connected "+ch.DisplayName+".")
}

func (s *Server) reconnectChannel(c *reqCtx) error {
	cid, err := pathUUID(c, id.Channel, "channel")
	if err != nil {
		return err
	}
	ch, err := s.svc.Channel(c.ctx(), c.actor, cid)
	if err != nil {
		return err
	}
	f, err := s.channelForm(c, string(ch.Provider))
	if err != nil {
		return err
	}
	f.Channel = ch
	for k, v := range ch.Settings {
		f.Values[k] = v
	}
	if c.r.Method == http.MethodGet || f.Provider == nil {
		return s.page(c, "channel_new", "channels", "Reconnect "+ch.DisplayName, f)
	}
	fields := map[string]string{}
	for _, fd := range f.Provider.Fields {
		fields[fd.Name] = c.r.PostFormValue("field_" + fd.Name)
	}
	if _, err := s.svc.ReconnectChannel(c.ctx(), c.actor, cid, fields); err != nil {
		return s.formErr(c, "channel_new", "channels", "Reconnect "+ch.DisplayName, f, err)
	}
	return redirect(c, "/channels", "Reconnected.")
}

func (s *Server) toggleChannel(c *reqCtx) error {
	cid, err := pathUUID(c, id.Channel, "channel")
	if err != nil {
		return err
	}
	enable := c.r.PostFormValue("enable") == "1"
	if err := s.svc.SetChannelEnabled(c.ctx(), c.actor, cid, enable); err != nil {
		return err
	}
	return redirect(c, "/channels", "")
}

func (s *Server) deleteChannel(c *reqCtx) error {
	cid, err := pathUUID(c, id.Channel, "channel")
	if err != nil {
		return err
	}
	if err := s.svc.DeleteChannel(c.ctx(), c.actor, cid); err != nil {
		return err
	}
	return redirect(c, "/channels", "Channel disconnected.")
}

// Brands.

type brandForm struct {
	Brand    *model.Brand
	Name     string
	Slug     string
	Timezone string
	Policy   string
	Slots    string
	Zones    []string
}

var commonZones = []string{"UTC", "America/New_York", "America/Chicago", "America/Denver", "America/Phoenix", "America/Los_Angeles",
	"Europe/London", "Europe/Berlin", "Europe/Rome", "Asia/Tokyo", "Australia/Sydney"}

func (s *Server) brands(c *reqCtx) error {
	bs, err := s.svc.Brands(c.ctx(), c.actor)
	if err != nil {
		return err
	}
	return s.page(c, "brands", "brands", "Brands", bs)
}

func (s *Server) newBrand(c *reqCtx) error {
	return s.page(c, "brand_edit", "brands", "New brand", brandForm{Timezone: "America/Denver", Policy: "none", Zones: commonZones})
}

func (s *Server) createBrand(c *reqCtx) error {
	f := brandForm{Name: c.r.PostFormValue("name"), Slug: c.r.PostFormValue("slug"), Timezone: c.r.PostFormValue("timezone"),
		Policy: c.r.PostFormValue("approval_policy"), Zones: commonZones}
	b, err := s.svc.CreateBrand(c.ctx(), c.actor, core.BrandInput{Name: f.Name, Slug: f.Slug, Timezone: f.Timezone, ApprovalPolicy: model.ApprovalPolicy(f.Policy)})
	if err != nil {
		return s.formErr(c, "brand_edit", "brands", "New brand", f, err)
	}
	return redirect(c, "/channels/new?brand="+id.Format(id.Brand, b.ID), "Brand created. Connect a channel to it.")
}

func (s *Server) brandDetail(c *reqCtx) error {
	bid, err := pathUUID(c, id.Brand, "brand")
	if err != nil {
		return err
	}
	b, err := s.svc.Brand(c.ctx(), c.actor, bid)
	if err != nil {
		return err
	}
	slots, err := s.svc.Slots(c.ctx(), c.actor, bid)
	if err != nil {
		return err
	}
	f := brandForm{Brand: b, Name: b.Name, Slug: b.Slug, Timezone: b.Timezone, Policy: string(b.ApprovalPolicy), Slots: formatSlots(slots), Zones: commonZones}
	return s.page(c, "brand_edit", "brands", b.Name, f)
}

func (s *Server) saveBrand(c *reqCtx) error {
	bid, err := pathUUID(c, id.Brand, "brand")
	if err != nil {
		return err
	}
	b, err := s.svc.Brand(c.ctx(), c.actor, bid)
	if err != nil {
		return err
	}
	f := brandForm{Brand: b, Name: c.r.PostFormValue("name"), Slug: c.r.PostFormValue("slug"), Timezone: c.r.PostFormValue("timezone"),
		Policy: c.r.PostFormValue("approval_policy"), Slots: c.r.PostFormValue("slots"), Zones: commonZones}
	slots, err := parseSlots(f.Slots)
	if err != nil {
		return s.formErr(c, "brand_edit", "brands", b.Name, f, err)
	}
	if _, err := s.svc.UpdateBrand(c.ctx(), c.actor, bid, core.BrandInput{Name: f.Name, Slug: f.Slug, Timezone: f.Timezone, ApprovalPolicy: model.ApprovalPolicy(f.Policy)}); err != nil {
		return s.formErr(c, "brand_edit", "brands", b.Name, f, err)
	}
	if err := s.svc.SetSlots(c.ctx(), c.actor, bid, slots); err != nil {
		return s.formErr(c, "brand_edit", "brands", b.Name, f, err)
	}
	return redirect(c, "/brands/"+c.r.PathValue("id"), "Saved.")
}

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// formatSlots writes slots one per line: "mon 09:00".
func formatSlots(slots []model.Slot) string {
	var lines []string
	for _, sl := range slots {
		lines = append(lines, weekdays[sl.Weekday]+" "+pad2(sl.MinuteOfDay/60)+":"+pad2(sl.MinuteOfDay%60))
	}
	return strings.Join(lines, "\n")
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// parseSlots reads "mon 09:00" lines; "weekdays 09:00" and "daily 09:00"
// expand.
func parseSlots(text string) ([]model.Slot, error) {
	var out []model.Slot
	var ps apperr.Problems
	for n, line := range strings.Split(text, "\n") {
		fields := strings.Fields(strings.ToLower(line))
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			ps.Add("slot_invalid", "slots", "Line %d: write a day and a time, like \"mon 09:00\".", n+1)
			continue
		}
		t, err := time.Parse("15:04", fields[1])
		if err != nil {
			ps.Add("slot_invalid", "slots", "Line %d: %q is not a 24-hour time.", n+1, fields[1])
			continue
		}
		minute := t.Hour()*60 + t.Minute()
		var days []int
		switch fields[0] {
		case "daily":
			days = []int{0, 1, 2, 3, 4, 5, 6}
		case "weekdays":
			days = []int{1, 2, 3, 4, 5}
		case "weekends":
			days = []int{0, 6}
		default:
			i := slices.Index(weekdays, strings.TrimSuffix(fields[0], ":")[:min(3, len(fields[0]))])
			if i < 0 {
				ps.Add("slot_invalid", "slots", "Line %d: %q is not a day.", n+1, fields[0])
				continue
			}
			days = []int{i}
		}
		for _, d := range days {
			out = append(out, model.Slot{Weekday: time.Weekday(d), MinuteOfDay: minute})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weekday != out[j].Weekday {
			return out[i].Weekday < out[j].Weekday
		}
		return out[i].MinuteOfDay < out[j].MinuteOfDay
	})
	return out, ps.Err("The slots are not valid.")
}
