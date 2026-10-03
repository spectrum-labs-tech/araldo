// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	contract "github.com/spectrum-labs-tech/araldo/api"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/tmpl"
)

func (h *Handler) routes() {
	h.public("GET /v1/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) error {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(contract.OpenAPI)
		return nil
	})
	h.handle("GET /v1/platforms", h.listPlatforms)

	h.handle("GET /v1/brands", h.listBrands)
	h.handle("POST /v1/brands", h.createBrand)
	h.handle("GET /v1/brands/{id}", h.getBrand)
	h.handle("POST /v1/brands/{id}", h.updateBrand)

	h.handle("GET /v1/channels", h.listChannels, "brand")
	h.handle("POST /v1/channels", h.createChannel)
	h.handle("GET /v1/channels/{id}", h.getChannel)
	h.handle("POST /v1/channels/{id}", h.updateChannel)
	h.handle("POST /v1/channels/{id}/reconnect", h.reconnectChannel)
	h.handle("DELETE /v1/channels/{id}", h.deleteChannel)

	h.handle("GET /v1/templates", h.listTemplates, "brand", "key")
	h.handle("POST /v1/templates", h.createTemplate)
	h.handle("POST /v1/templates/preview", h.previewTemplate)
	h.handle("GET /v1/templates/{id}", h.getTemplate, "version")
	h.handle("POST /v1/templates/{id}", h.updateTemplate)
	h.handle("POST /v1/templates/{id}/versions", h.addTemplateVersion)
	h.handle("DELETE /v1/templates/{id}", h.deleteTemplate)

	h.handle("GET /v1/media", h.listMedia, paged("brand")...)
	h.handle("POST /v1/media", h.createMedia)
	h.handle("GET /v1/media/{id}", h.getMedia)
	h.handle("POST /v1/media/{id}", h.updateMedia)
	h.handle("DELETE /v1/media/{id}", h.deleteMedia)
	h.public("GET /v1/media/{id}/content", h.mediaContent, "expires", "signature")

	h.handle("GET /v1/posts", h.listPosts, paged("brand", "status", "metadata", "q")...)
	h.handle("POST /v1/posts", h.createPost)
	h.handle("POST /v1/posts/preview", h.previewPost)
	h.handle("GET /v1/posts/{id}", h.getPost)
	h.handle("POST /v1/posts/{id}/cancel", h.cancelPost)
	h.handle("POST /v1/posts/{id}/reschedule", h.reschedulePost)
	h.handle("POST /v1/posts/{id}/approve", h.reviewPost(true))
	h.handle("POST /v1/posts/{id}/reject", h.reviewPost(false))
	h.handle("POST /v1/post_targets/{id}/retry", h.retryTarget)
	h.handle("POST /v1/post_targets/{id}/mark_published", h.markPublished)
	h.handle("GET /v1/post_targets/{id}/engagement", h.listEngagement)
	h.handle("GET /v1/post_targets/{id}/attempts", h.listAttempts)
	h.handle("GET /v1/engagement/summary", h.engagementSummary, "group_by", "brand", "since", "until", "limit")
	h.handle("GET /v1/ad_networks", h.listAdNetworks)
	h.handle("GET /v1/ad_accounts", h.listAdAccounts, "brand")
	h.handle("POST /v1/ad_accounts", h.createAdAccount)
	h.handle("GET /v1/ad_accounts/{id}", h.getAdAccount)
	h.handle("DELETE /v1/ad_accounts/{id}", h.deleteAdAccount)
	h.handle("GET /v1/ads/summary", h.adsSummary, "group_by", "brand", "account", "since", "until", "limit")

	h.handle("GET /v1/events", h.listEvents, paged("type")...)

	h.handle("GET /v1/api_keys", h.listKeys)
	h.handle("POST /v1/api_keys", h.createKey)
	h.handle("POST /v1/api_keys/self/roll", h.rollOwnKey)
	h.handle("POST /v1/api_keys/{id}/roll", h.rollKey)
	h.handle("POST /v1/api_keys/{id}/revoke", h.revokeKey)
	h.handle("GET /v1/audit_events", h.listAudit, paged()...)
	h.handle("GET /v1/events/{id}", h.getEvent)

	h.handle("GET /v1/webhook_endpoints", h.listEndpoints)
	h.handle("POST /v1/webhook_endpoints", h.createEndpoint)
	h.handle("GET /v1/webhook_endpoints/{id}", h.getEndpoint)
	h.handle("POST /v1/webhook_endpoints/{id}", h.updateEndpoint)
	h.handle("DELETE /v1/webhook_endpoints/{id}", h.deleteEndpoint)
	h.handle("POST /v1/webhook_endpoints/{id}/roll_secret", h.rollSecret)
	h.handle("GET /v1/webhook_endpoints/{id}/deliveries", h.listDeliveries, paged()...)
	h.handle("POST /v1/webhook_deliveries/{id}/resend", h.resendDelivery)
}

// deleted is the answer to a DELETE.
type deleted struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// Platforms.

type platformView struct {
	Provider       string           `json:"provider"`
	Name           string           `json:"name"`
	MaxLength      int              `json:"max_length"`
	Counting       string           `json:"counting"`
	Threads        bool             `json:"threads"`
	MaxThreadParts int              `json:"max_thread_parts,omitempty"`
	MediaRequired  bool             `json:"media_required"`
	MaxMedia       int              `json:"max_media"`
	Source         string           `json:"source"`
	Images         imagesView       `json:"images"`
	Fields         []platform.Field `json:"connect_fields,omitempty"`
}

// imagesView is what a platform takes as images (ADR 0017).
type imagesView struct {
	// MaxBytes maps each accepted type to its size limit; 0 means none is
	// documented.
	MaxBytes      map[string]int64 `json:"max_bytes"`
	MinAspect     float64          `json:"min_aspect_ratio,omitempty"`
	MaxAspect     float64          `json:"max_aspect_ratio,omitempty"`
	MaxDimensions int              `json:"max_width_plus_height,omitempty"`
	MaxCaption    int              `json:"max_length_with_media,omitempty"`
	Source        string           `json:"source"`
}

func (h *Handler) listPlatforms(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var out []platformView
	for _, p := range platform.Emulable() {
		rules, _ := platform.RulesFor(p)
		v := platformView{Provider: string(p), Name: rules.Name, MaxLength: rules.MaxLength, Counting: string(rules.Counting),
			Threads: rules.Threads, MaxThreadParts: rules.MaxThreadParts, MediaRequired: rules.MediaRequired, MaxMedia: rules.MaxMedia, Source: rules.Source,
			Images: imagesView{MaxBytes: rules.Images, MinAspect: rules.MinAspect, MaxAspect: rules.MaxAspect, MaxDimensions: rules.MaxDimensions,
				MaxCaption: rules.MaxCaption, Source: rules.ImageSource}}
		if ad, ok := h.svc.Platforms().Get(p); ok && a.Livemode {
			v.Fields = ad.Fields()
		}
		out = append(out, v)
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/platforms"})
	return nil
}

// Brands.

type brandBody struct {
	Name           *string   `json:"name"`
	Slug           *string   `json:"slug"`
	Timezone       *string   `json:"timezone"`
	ApprovalPolicy *string   `json:"approval_policy"`
	UTMDomains     *[]string `json:"utm_domains"`
	// Slots replace the brand's weekly slots when present.
	Slots *[]core.SlotView `json:"slots"`
}

// slots parses the body's slots, if any.
func (b brandBody) slots() (*[]model.Slot, error) {
	if b.Slots == nil {
		return nil, nil
	}
	var ps apperr.Problems
	out := make([]model.Slot, 0, len(*b.Slots))
	for i, sv := range *b.Slots {
		sl, ok := core.ParseSlot(sv.Weekday, sv.Time)
		if !ok {
			ps.Add("slot_invalid", "slots["+strconv.Itoa(i)+"]", `A slot is a weekday ("monday") and a 24-hour time ("09:00").`)
			continue
		}
		out = append(out, sl)
	}
	return &out, ps.Err("The slots are not valid.")
}

func (b brandBody) input(base *model.Brand) core.BrandInput {
	in := core.BrandInput{}
	if base != nil {
		in = core.BrandInput{Name: base.Name, Slug: base.Slug, Timezone: base.Timezone, ApprovalPolicy: base.ApprovalPolicy, UTMDomains: base.UTMDomains}
	}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.Slug != nil {
		in.Slug = *b.Slug
	}
	if b.Timezone != nil {
		in.Timezone = *b.Timezone
	}
	if b.ApprovalPolicy != nil {
		in.ApprovalPolicy = model.ApprovalPolicy(*b.ApprovalPolicy)
	}
	if b.UTMDomains != nil {
		in.UTMDomains = *b.UTMDomains
	}
	return in
}

func (h *Handler) listBrands(w http.ResponseWriter, r *http.Request) error {
	bs, err := h.svc.Brands(r.Context(), actor(r))
	if err != nil {
		return err
	}
	out := make([]core.BrandView, 0, len(bs))
	for _, b := range bs {
		out = append(out, core.ViewBrand(b))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/brands"})
	return nil
}

func (h *Handler) createBrand(w http.ResponseWriter, r *http.Request) error {
	var body brandBody
	if err := decode(r, &body); err != nil {
		return err
	}
	in := body.input(nil)
	var err error
	if in.Slots, err = body.slots(); err != nil {
		return err
	}
	b, err := h.svc.CreateBrand(r.Context(), actor(r), in)
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, core.ViewBrand(b))
	return nil
}

func (h *Handler) getBrand(w http.ResponseWriter, r *http.Request) error {
	b, err := h.svc.ResolveBrand(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewBrand(b))
	return nil
}

func (h *Handler) updateBrand(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	b, err := h.svc.ResolveBrand(r.Context(), a, r.PathValue("id"))
	if err != nil {
		return err
	}
	var body brandBody
	if err := decode(r, &body); err != nil {
		return err
	}
	in := body.input(b)
	if in.Slots, err = body.slots(); err != nil {
		return err
	}
	b, err = h.svc.UpdateBrand(r.Context(), a, b.ID, in)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewBrand(b))
	return nil
}

// Channels.

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var brandID *uuid.UUID
	if ref := r.URL.Query().Get("brand"); ref != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, ref)
		if err != nil {
			return err
		}
		brandID = &b.ID
	}
	chs, err := h.svc.Channels(r.Context(), a, brandID)
	if err != nil {
		return err
	}
	out := make([]core.ChannelView, 0, len(chs))
	for _, c := range chs {
		out = append(out, core.ViewChannel(c))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/channels"})
	return nil
}

func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var body struct {
		Brand    string            `json:"brand"`
		Provider string            `json:"provider"`
		Emulates string            `json:"emulates"`
		Fields   map[string]string `json:"fields"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	b, err := h.svc.ResolveBrand(r.Context(), a, body.Brand)
	if err != nil {
		return err
	}
	if body.Fields == nil {
		body.Fields = map[string]string{}
	}
	if body.Emulates != "" {
		body.Fields["emulates"] = body.Emulates
	}
	ch, err := h.svc.ConnectChannel(r.Context(), a, core.ConnectInput{BrandID: b.ID, Provider: platform.Provider(body.Provider), Fields: body.Fields})
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, core.ViewChannel(ch))
	return nil
}

func (h *Handler) getChannel(w http.ResponseWriter, r *http.Request) error {
	cid, err := pathID(r, id.Channel, "channel")
	if err != nil {
		return err
	}
	ch, err := h.svc.Channel(r.Context(), actor(r), cid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewChannel(ch))
	return nil
}

// updateChannel enables or disables a channel (ADR 0019).
func (h *Handler) updateChannel(w http.ResponseWriter, r *http.Request) error {
	cid, err := pathID(r, id.Channel, "channel")
	if err != nil {
		return err
	}
	var body struct {
		Status *string `json:"status"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Status != nil {
		switch model.ChannelStatus(*body.Status) {
		case model.ChannelActive, model.ChannelDisabled:
		default:
			return badRequest("parameter_invalid", "status", `status is "active" or "disabled"; reconnect a channel that needs reauthorization.`)
		}
		if err := h.svc.SetChannelEnabled(r.Context(), actor(r), cid, model.ChannelStatus(*body.Status) == model.ChannelActive); err != nil {
			return err
		}
	}
	ch, err := h.svc.Channel(r.Context(), actor(r), cid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewChannel(ch))
	return nil
}

// reconnectChannel replaces a channel's credentials, checking them with the
// platform, and makes it active again (ADR 0019).
func (h *Handler) reconnectChannel(w http.ResponseWriter, r *http.Request) error {
	cid, err := pathID(r, id.Channel, "channel")
	if err != nil {
		return err
	}
	var body struct {
		Fields map[string]string `json:"fields"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	ch, err := h.svc.ReconnectChannel(r.Context(), actor(r), cid, body.Fields)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewChannel(ch))
	return nil
}

func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request) error {
	cid, err := pathID(r, id.Channel, "channel")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteChannel(r.Context(), actor(r), cid); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: r.PathValue("id"), Object: "channel", Deleted: true})
	return nil
}

// Templates.

type sourceBody struct {
	Variables json.RawMessage                    `json:"variables"`
	Examples  []json.RawMessage                  `json:"examples"`
	Body      string                             `json:"body"`
	Overrides map[platform.Provider]string       `json:"overrides"`
	Fit       map[platform.Provider]platform.Fit `json:"fit"`
}

func (b sourceBody) source() tmpl.Source {
	vars := b.Variables
	if string(vars) == "null" {
		vars = nil
	}
	return tmpl.Source{Variables: vars, Examples: b.Examples, Body: b.Body, Overrides: b.Overrides, Fit: b.Fit}
}

func (h *Handler) listTemplates(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var brandID *uuid.UUID
	if ref := r.URL.Query().Get("brand"); ref != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, ref)
		if err != nil {
			return err
		}
		brandID = &b.ID
	}
	ts, err := h.svc.Templates(r.Context(), a, brandID)
	if err != nil {
		return err
	}
	key := r.URL.Query().Get("key")
	out := make([]core.TemplateView, 0, len(ts))
	for _, t := range ts {
		if key == "" || t.Key == key {
			out = append(out, core.ViewTemplate(t, nil))
		}
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/templates"})
	return nil
}

func (h *Handler) createTemplate(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var body struct {
		sourceBody
		Brand    string `json:"brand"`
		Key      string `json:"key"`
		Name     string `json:"name"`
		Approval string `json:"approval"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	b, err := h.svc.ResolveBrand(r.Context(), a, body.Brand)
	if err != nil {
		return err
	}
	t, v, err := h.svc.CreateTemplate(r.Context(), a, core.TemplateInput{BrandID: b.ID, Key: body.Key, Name: body.Name,
		Approval: model.TemplateApproval(body.Approval), Source: body.source()})
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, core.ViewTemplate(t, v))
	return nil
}

func (h *Handler) getTemplate(w http.ResponseWriter, r *http.Request) error {
	tid, err := templatePathID(r)
	if err != nil {
		return err
	}
	version := 0
	if v := r.URL.Query().Get("version"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return badRequest("parameter_invalid", "version", "version must be a positive integer.")
		}
		version = n
	}
	t, tv, err := h.svc.Template(r.Context(), actor(r), tid, version)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewTemplate(t, tv))
	return nil
}

func (h *Handler) updateTemplate(w http.ResponseWriter, r *http.Request) error {
	tid, err := templatePathID(r)
	if err != nil {
		return err
	}
	var body struct {
		Name     *string `json:"name"`
		Approval *string `json:"approval"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	in := core.TemplateSettings{Name: body.Name}
	if body.Approval != nil {
		ap := model.TemplateApproval(*body.Approval)
		in.Approval = &ap
	}
	t, v, err := h.svc.UpdateTemplate(r.Context(), actor(r), tid, in)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewTemplate(t, v))
	return nil
}

func (h *Handler) addTemplateVersion(w http.ResponseWriter, r *http.Request) error {
	tid, err := templatePathID(r)
	if err != nil {
		return err
	}
	var body struct {
		sourceBody
		Name string `json:"name"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	t, v, err := h.svc.AddTemplateVersion(r.Context(), actor(r), tid, body.Name, body.source())
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, core.ViewTemplate(t, v))
	return nil
}

func (h *Handler) deleteTemplate(w http.ResponseWriter, r *http.Request) error {
	tid, err := templatePathID(r)
	if err != nil {
		return err
	}
	if err := h.svc.DeleteTemplate(r.Context(), actor(r), tid); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: r.PathValue("id"), Object: "template", Deleted: true})
	return nil
}

func (h *Handler) previewTemplate(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var body struct {
		sourceBody
		Brand     string              `json:"brand"`
		Template  string              `json:"template"`
		Data      json.RawMessage     `json:"data"`
		Providers []platform.Provider `json:"providers"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	src, tz := body.source(), "UTC"
	if body.Brand != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, body.Brand)
		if err != nil {
			return err
		}
		tz = b.Timezone
		if body.Template != "" {
			_, v, err := h.svc.TemplateByRef(r.Context(), a, b.ID, body.Template)
			if err != nil {
				return err
			}
			src = tmpl.Source{Variables: v.Variables, Examples: v.Examples, Body: v.Body, Overrides: v.Overrides, Fit: v.Fit}
		}
	}
	renders, err := h.svc.PreviewTemplate(r.Context(), a, src, body.Data, body.Providers, tz)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, map[string]any{"object": "preview", "renditions": renders})
	return nil
}

// templatePathID reads a template ID from the path. A template's key is
// unique only within its brand, so paths take IDs; the error says how to
// find one by key.
func templatePathID(r *http.Request) (uuid.UUID, error) {
	ref := r.PathValue("id")
	tid, err := id.Parse(id.Template, ref)
	if err != nil {
		return uuid.Nil, &apperr.Error{Kind: apperr.KindNotFound, Code: "resource_missing", Param: "id",
			Message: fmt.Sprintf("No such template: %q. Paths take template IDs (tmpl_…); find one by key with GET /v1/templates?brand=…&key=%s.", ref, url.QueryEscape(ref))}
	}
	return tid, nil
}

// Posts.

type postBody struct {
	Brand     string            `json:"brand"`
	Template  string            `json:"template"`
	Data      json.RawMessage   `json:"data"`
	Content   *model.Content    `json:"content"`
	Channels  []string          `json:"channels"`
	PublishAt string            `json:"publish_at"`
	PublishBy *time.Time        `json:"publish_by"`
	Metadata  map[string]string `json:"metadata"`
	Media     []string          `json:"media"`
}

func (h *Handler) postInput(r *http.Request) (core.PostInput, error) {
	var body postBody
	if err := decode(r, &body); err != nil {
		return core.PostInput{}, err
	}
	b, err := h.svc.ResolveBrand(r.Context(), actor(r), body.Brand)
	if err != nil {
		return core.PostInput{}, err
	}
	in := core.PostInput{BrandID: b.ID, Template: body.Template, Data: body.Data, Content: body.Content, PublishAt: body.PublishAt,
		PublishBy: body.PublishBy, Metadata: body.Metadata}
	if string(in.Data) == "null" {
		in.Data = nil
	}
	var ps apperr.Problems
	for i, c := range body.Channels {
		cid, err := id.Parse(id.Channel, c)
		if err != nil {
			ps.Add("channel_invalid", "channels["+strconv.Itoa(i)+"]", "%q is not a channel ID.", c)
			continue
		}
		in.Channels = append(in.Channels, cid)
	}
	for i, m := range body.Media {
		mid, err := id.Parse(id.Media, m)
		if err != nil {
			ps.Add("media_invalid", "media["+strconv.Itoa(i)+"]", "%q is not a media ID.", m)
			continue
		}
		in.Media = append(in.Media, mid)
	}
	return in, ps.Err("The post is not valid.")
}

func (h *Handler) createPost(w http.ResponseWriter, r *http.Request) error {
	in, err := h.postInput(r)
	if err != nil {
		return err
	}
	p, err := h.svc.CreatePost(r.Context(), actor(r), in)
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, core.ViewPost(p))
	return nil
}

func (h *Handler) previewPost(w http.ResponseWriter, r *http.Request) error {
	in, err := h.postInput(r)
	if err != nil {
		return err
	}
	renders, err := h.svc.PreviewPost(r.Context(), actor(r), in)
	if err != nil {
		return err
	}
	valid := true
	for _, rd := range renders {
		if len(rd.Violations) > 0 {
			valid = false
		}
	}
	ok(w, http.StatusOK, map[string]any{"object": "preview", "valid": valid, "renditions": renders})
	return nil
}

func (h *Handler) listPosts(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	pg, err := page(r, id.Post)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := core.PostFilter{Status: q.Get("status"), Query: q.Get("q")}
	if ref := q.Get("brand"); ref != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, ref)
		if err != nil {
			return err
		}
		f.BrandID = &b.ID
	}
	for k, vs := range q {
		if key, found := strings.CutPrefix(k, "metadata["); found && strings.HasSuffix(key, "]") && len(vs) > 0 {
			if f.Metadata == nil {
				f.Metadata = map[string]string{}
			}
			f.Metadata[strings.TrimSuffix(key, "]")] = vs[0]
		}
	}
	posts, more, err := h.svc.Posts(r.Context(), a, f, pg)
	if err != nil {
		return err
	}
	out := make([]core.PostView, 0, len(posts))
	for _, p := range posts {
		out = append(out, core.ViewPost(p))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, HasMore: more, URL: "/v1/posts"})
	return nil
}

func (h *Handler) getPost(w http.ResponseWriter, r *http.Request) error {
	pid, err := pathID(r, id.Post, "post")
	if err != nil {
		return err
	}
	p, err := h.svc.Post(r.Context(), actor(r), pid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewPost(p))
	return nil
}

func (h *Handler) cancelPost(w http.ResponseWriter, r *http.Request) error {
	pid, err := pathID(r, id.Post, "post")
	if err != nil {
		return err
	}
	p, err := h.svc.CancelPost(r.Context(), actor(r), pid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewPost(p))
	return nil
}

func (h *Handler) reschedulePost(w http.ResponseWriter, r *http.Request) error {
	pid, err := pathID(r, id.Post, "post")
	if err != nil {
		return err
	}
	var body struct {
		PublishAt string     `json:"publish_at"`
		PublishBy *time.Time `json:"publish_by"`
		SwapWith  string     `json:"swap_with"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	in := core.RescheduleInput{PublishAt: body.PublishAt, PublishBy: body.PublishBy}
	if body.SwapWith != "" {
		other, err := id.Parse(id.Post, body.SwapWith)
		if err != nil {
			return badRequest("parameter_invalid", "swap_with", "swap_with must be a post ID.")
		}
		in.SwapWith = &other
	}
	moved, err := h.svc.ReschedulePost(r.Context(), actor(r), pid, in)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewPost(moved[0]))
	return nil
}

func (h *Handler) retryTarget(w http.ResponseWriter, r *http.Request) error {
	tid, err := pathID(r, id.Target, "post target")
	if err != nil {
		return err
	}
	p, err := h.svc.RetryTarget(r.Context(), actor(r), tid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewPost(p))
	return nil
}

func (h *Handler) listAttempts(w http.ResponseWriter, r *http.Request) error {
	tid, err := pathID(r, id.Target, "post target")
	if err != nil {
		return err
	}
	attempts, err := h.svc.Attempts(r.Context(), actor(r), tid)
	if err != nil {
		return err
	}
	out := make([]core.AttemptView, 0, len(attempts))
	for i := range attempts {
		out = append(out, core.ViewAttempt(&attempts[i]))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/post_targets/" + r.PathValue("id") + "/attempts"})
	return nil
}

func (h *Handler) markPublished(w http.ResponseWriter, r *http.Request) error {
	tid, err := pathID(r, id.Target, "post target")
	if err != nil {
		return err
	}
	var body struct {
		Permalink string `json:"permalink"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	p, err := h.svc.MarkTargetPublished(r.Context(), actor(r), tid, body.Permalink)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewPost(p))
	return nil
}

// Events.

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) error {
	pg, err := page(r, id.Event)
	if err != nil {
		return err
	}
	evs, more, err := h.svc.Events(r.Context(), actor(r), r.URL.Query().Get("type"), pg)
	if err != nil {
		return err
	}
	out := make([]core.EventView, 0, len(evs))
	for _, e := range evs {
		out = append(out, core.ViewEvent(e))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, HasMore: more, URL: "/v1/events"})
	return nil
}

func (h *Handler) getEvent(w http.ResponseWriter, r *http.Request) error {
	eid, err := pathID(r, id.Event, "event")
	if err != nil {
		return err
	}
	e, err := h.svc.Event(r.Context(), actor(r), eid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewEvent(e))
	return nil
}

// Webhooks.

type endpointBody struct {
	URL           string   `json:"url"`
	Description   string   `json:"description"`
	EnabledEvents []string `json:"enabled_events"`
	Disabled      *bool    `json:"disabled"`
}

func (h *Handler) listEndpoints(w http.ResponseWriter, r *http.Request) error {
	eps, err := h.svc.Endpoints(r.Context(), actor(r))
	if err != nil {
		return err
	}
	out := make([]core.EndpointView, 0, len(eps))
	for _, e := range eps {
		out = append(out, core.ViewEndpoint(e))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/webhook_endpoints"})
	return nil
}

func (h *Handler) createEndpoint(w http.ResponseWriter, r *http.Request) error {
	var body endpointBody
	if err := decode(r, &body); err != nil {
		return err
	}
	ep, secret, err := h.svc.CreateEndpoint(r.Context(), actor(r), core.EndpointInput{URL: body.URL, Description: body.Description, EventTypes: body.EnabledEvents})
	if err != nil {
		return err
	}
	v := core.ViewEndpoint(ep)
	v.Secret = secret
	ok(w, http.StatusCreated, v)
	return nil
}

func (h *Handler) getEndpoint(w http.ResponseWriter, r *http.Request) error {
	eid, err := pathID(r, id.WebhookEndpoint, "webhook endpoint")
	if err != nil {
		return err
	}
	ep, err := h.svc.Endpoint(r.Context(), actor(r), eid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewEndpoint(ep))
	return nil
}

func (h *Handler) updateEndpoint(w http.ResponseWriter, r *http.Request) error {
	eid, err := pathID(r, id.WebhookEndpoint, "webhook endpoint")
	if err != nil {
		return err
	}
	var body endpointBody
	if err := decode(r, &body); err != nil {
		return err
	}
	in := core.EndpointInput{URL: body.URL, Description: body.Description, EventTypes: body.EnabledEvents}
	if body.Disabled != nil {
		enabled := !*body.Disabled
		in.Enabled = &enabled
	}
	ep, err := h.svc.UpdateEndpoint(r.Context(), actor(r), eid, in)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewEndpoint(ep))
	return nil
}

func (h *Handler) deleteEndpoint(w http.ResponseWriter, r *http.Request) error {
	eid, err := pathID(r, id.WebhookEndpoint, "webhook endpoint")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteEndpoint(r.Context(), actor(r), eid); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: r.PathValue("id"), Object: "webhook_endpoint", Deleted: true})
	return nil
}

func (h *Handler) rollSecret(w http.ResponseWriter, r *http.Request) error {
	eid, err := pathID(r, id.WebhookEndpoint, "webhook endpoint")
	if err != nil {
		return err
	}
	secret, err := h.svc.RollEndpointSecret(r.Context(), actor(r), eid)
	if err != nil {
		return err
	}
	ep, err := h.svc.Endpoint(r.Context(), actor(r), eid)
	if err != nil {
		return err
	}
	v := core.ViewEndpoint(ep)
	v.Secret = secret
	ok(w, http.StatusOK, v)
	return nil
}

func (h *Handler) listDeliveries(w http.ResponseWriter, r *http.Request) error {
	eid, err := pathID(r, id.WebhookEndpoint, "webhook endpoint")
	if err != nil {
		return err
	}
	pg, err := page(r, id.Delivery)
	if err != nil {
		return err
	}
	ds, more, err := h.svc.Deliveries(r.Context(), actor(r), eid, pg)
	if err != nil {
		return err
	}
	out := make([]core.DeliveryView, 0, len(ds))
	for i := range ds {
		out = append(out, core.ViewDelivery(&ds[i]))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, HasMore: more, URL: "/v1/webhook_endpoints/" + r.PathValue("id") + "/deliveries"})
	return nil
}

func (h *Handler) resendDelivery(w http.ResponseWriter, r *http.Request) error {
	did, err := pathID(r, id.Delivery, "webhook delivery")
	if err != nil {
		return err
	}
	if err := h.svc.ResendDelivery(r.Context(), actor(r), did); err != nil {
		return err
	}
	ok(w, http.StatusAccepted, map[string]any{"id": r.PathValue("id"), "object": "webhook_delivery", "status": "pending"})
	return nil
}
