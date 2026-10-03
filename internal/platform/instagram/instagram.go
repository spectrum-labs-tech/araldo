// SPDX-License-Identifier: AGPL-3.0-or-later

// Package instagram posts to Instagram professional accounts linked to a
// Facebook Page, through the org's Meta app (ADR 0021): Facebook Login with
// instagram_basic, instagram_content_publish, pages_show_list and
// business_management. The Page's token, which does not expire, publishes.
//
// Every Instagram post has an image; Instagram fetches it from a link, so
// the install's API must be reachable from the internet. There is no
// idempotency key, so the adapter is not idempotent.
package instagram

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/meta"
)

var scopes = []string{"instagram_basic", "instagram_content_publish", "pages_show_list", "business_management"}

// Adapter posts to Instagram.
type Adapter struct {
	Meta *meta.Client
}

// New returns an Instagram adapter.
func New(client *http.Client) *Adapter { return &Adapter{Meta: meta.New(client)} }

func (a *Adapter) Provider() platform.Provider { return platform.Instagram }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Instagram)
	return r
}

// Fields are none: accounts connect with a sign-in.
func (a *Adapter) Fields() []platform.Field { return nil }

func (a *Adapter) Idempotent() bool { return false }

func (a *Adapter) AuthorizeURL(app platform.App, redirectURI, state, _ string) string {
	return a.Meta.AuthorizeURL(app, redirectURI, state, scopes...)
}

// Exchange returns the Instagram accounts linked to the member's Pages.
func (a *Adapter) Exchange(ctx context.Context, app platform.App, redirectURI, code, _ string) ([]platform.Connection, error) {
	user, err := a.Meta.UserToken(ctx, app, redirectURI, code)
	if err != nil {
		return nil, err
	}
	pages, err := a.Meta.Pages(ctx, user)
	if err != nil {
		return nil, err
	}
	var out []platform.Connection
	for _, p := range pages {
		ig := p.Instagram
		if ig == nil {
			continue
		}
		name := ig.Name
		if name == "" {
			name = "@" + ig.Username
		}
		out = append(out, platform.Connection{
			Account:     platform.Account{ExternalID: ig.ID, Handle: "@" + ig.Username, DisplayName: name, URL: "https://www.instagram.com/" + ig.Username},
			Credentials: platform.Credentials{"ig_user_id": ig.ID, "access_token": p.AccessToken},
		})
	}
	return out, nil
}

func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	var me struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := a.Meta.Get(ctx, c["ig_user_id"], url.Values{"fields": {"id,username,name"}, "access_token": {c["access_token"]}}, &me); err != nil {
		return platform.Account{}, err
	}
	return platform.Account{ExternalID: me.ID, Handle: "@" + me.Username, DisplayName: me.Name, URL: "https://www.instagram.com/" + me.Username}, nil
}

// Publish posts the single part as a photo, a carousel of up to ten, or a
// video as a reel shared to the feed
// (https://developers.facebook.com/docs/instagram-platform/content-publishing).
func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	if len(p.Posted) >= len(p.Parts) {
		return res, nil
	}
	user, tok := c["ig_user_id"], c["access_token"]
	if user == "" || tok == "" {
		return res, platform.Errorf(platform.AuthRevoked, "the channel has no token: reconnect it")
	}
	if len(p.Media) == 0 {
		return res, &platform.Error{Kind: platform.Rejected, Code: "media_required", Msg: "Instagram posts need an image or a video"}
	}
	for _, m := range p.Media {
		if m.URL == "" {
			return res, &platform.Error{Kind: platform.Rejected, Code: "media_link_missing",
				Msg: "Instagram fetches media from a link, and this install cannot make one: its API must be public at ARALDO_BASE_URL"}
		}
	}
	params := url.Values{"caption": {p.Parts[0]}, "access_token": {tok}}
	switch {
	case len(p.Media) == 1 && p.Media[0].IsVideo():
		params.Set("media_type", "REELS")
		params.Set("video_url", p.Media[0].URL)
		params.Set("share_to_feed", "true")
	case len(p.Media) == 1:
		params.Set("image_url", p.Media[0].URL)
		if p.Media[0].Alt != "" {
			params.Set("alt_text", p.Media[0].Alt)
		}
	default:
		var children []string
		for _, m := range p.Media {
			item := url.Values{"image_url": {m.URL}, "is_carousel_item": {"true"}, "access_token": {tok}}
			if m.Alt != "" {
				item.Set("alt_text", m.Alt)
			}
			id, err := a.container(ctx, user, tok, item)
			if err != nil {
				return res, err
			}
			children = append(children, id)
		}
		params.Set("media_type", "CAROUSEL")
		params.Set("children", strings.Join(children, ","))
	}
	container, err := a.container(ctx, user, tok, params)
	if err != nil {
		return res, err
	}
	var published struct {
		ID string `json:"id"`
	}
	if err := a.Meta.Post(ctx, user+"/media_publish", url.Values{"creation_id": {container}, "access_token": {tok}}, &published); err != nil {
		return res, err
	}
	var link struct {
		Permalink string `json:"permalink"`
	}
	_ = a.Meta.Get(ctx, published.ID, url.Values{"fields": {"permalink"}, "access_token": {tok}}, &link)
	ref := platform.RemoteRef{ID: published.ID, URL: link.Permalink}
	if onPart != nil {
		if err := onPart(ref); err != nil {
			return res, err
		}
	}
	res.Parts = append(res.Parts, ref)
	res.Permalink = ref.URL
	return res, nil
}

// container creates a media container and waits until Instagram has
// fetched and processed its image or video.
func (a *Adapter) container(ctx context.Context, user, tok string, params url.Values) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := a.Meta.Post(ctx, user+"/media", params, &out); err != nil {
		return "", err
	}
	for {
		var st struct {
			StatusCode string `json:"status_code"`
		}
		if err := a.Meta.Get(ctx, out.ID, url.Values{"fields": {"status_code"}, "access_token": {tok}}, &st); err != nil {
			return "", err
		}
		switch st.StatusCode {
		case "FINISHED", "PUBLISHED", "":
			return out.ID, nil
		case "ERROR", "EXPIRED":
			return "", &platform.Error{Kind: platform.Rejected, Code: "media_rejected", Msg: "Instagram could not process the media (" + st.StatusCode + ")"}
		}
		select {
		case <-ctx.Done():
			return "", &platform.Error{Kind: platform.Transient, Code: "processing", Msg: "Instagram was still processing the media", Err: ctx.Err()}
		case <-time.After(a.Meta.Poll):
		}
	}
}

// Engagement reads each post's likes and comments (replies). A post
// Instagram no longer has is left out.
func (a *Adapter) Engagement(ctx context.Context, c platform.Credentials, refs []platform.RemoteRef) (map[string]platform.Counts, error) {
	out := map[string]platform.Counts{}
	for _, r := range refs {
		var m struct {
			LikeCount     int64 `json:"like_count"`
			CommentsCount int64 `json:"comments_count"`
		}
		err := a.Meta.Get(ctx, r.ID, url.Values{"fields": {"like_count,comments_count"}, "access_token": {c["access_token"]}}, &m)
		if platform.KindOf(err) == platform.Rejected {
			continue // deleted
		}
		if err != nil {
			return nil, err
		}
		out[r.ID] = platform.Counts{Likes: m.LikeCount, Replies: m.CommentsCount}
	}
	return out, nil
}
