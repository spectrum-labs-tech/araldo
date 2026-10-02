// SPDX-License-Identifier: AGPL-3.0-or-later

// Package facebook posts to Facebook Pages with Page tokens from a sign-in
// through the org's Meta app (ADR 0021): Facebook Login with
// pages_show_list, pages_manage_posts and pages_read_engagement. Page
// tokens derived from a long-lived user token do not expire.
//
// Images are uploaded, not linked, so they work from any install. There is
// no idempotency key, so the adapter is not idempotent.
package facebook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/meta"
)

var scopes = []string{"pages_show_list", "pages_manage_posts", "pages_read_engagement"}

// Adapter posts to Facebook Pages.
type Adapter struct {
	Meta *meta.Client
}

// New returns a Facebook adapter.
func New(client *http.Client) *Adapter { return &Adapter{Meta: meta.New(client)} }

func (a *Adapter) Provider() platform.Provider { return platform.Facebook }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Facebook)
	return r
}

// Fields are none: Pages connect with a sign-in.
func (a *Adapter) Fields() []platform.Field { return nil }

func (a *Adapter) Idempotent() bool { return false }

func (a *Adapter) AuthorizeURL(app platform.App, redirectURI, state, _ string) string {
	return a.Meta.AuthorizeURL(app, redirectURI, state, scopes...)
}

// Exchange returns every Page the member manages; they choose which to
// connect.
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
		out = append(out, platform.Connection{
			Account:     platform.Account{ExternalID: p.ID, Handle: p.Name, DisplayName: p.Name, URL: "https://www.facebook.com/" + p.ID},
			Credentials: platform.Credentials{"page_id": p.ID, "access_token": p.AccessToken},
		})
	}
	return out, nil
}

func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	var page struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := a.Meta.Get(ctx, c["page_id"], url.Values{"fields": {"id,name"}, "access_token": {c["access_token"]}}, &page); err != nil {
		return platform.Account{}, err
	}
	return platform.Account{ExternalID: page.ID, Handle: page.Name, DisplayName: page.Name, URL: "https://www.facebook.com/" + page.ID}, nil
}

// Publish posts the single part (Pages have no threads): text to the feed,
// one image as a photo with the text as its caption, or several images
// uploaded unpublished and attached to one post
// (https://developers.facebook.com/docs/pages-api/posts).
func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	if len(p.Posted) >= len(p.Parts) {
		return res, nil
	}
	page, tok := c["page_id"], c["access_token"]
	if page == "" || tok == "" {
		return res, platform.Errorf(platform.AuthRevoked, "the channel has no Page token: reconnect it")
	}
	text := p.Parts[0]
	var postID string
	switch len(p.Media) {
	case 0:
		var out struct {
			ID string `json:"id"`
		}
		if err := a.Meta.Post(ctx, page+"/feed", url.Values{"message": {text}, "access_token": {tok}}, &out); err != nil {
			return res, err
		}
		postID = out.ID
	case 1:
		var out struct {
			PostID string `json:"post_id"`
		}
		if err := a.photo(ctx, page, tok, p.Media[0], 0, [][2]string{{"message", text}, {"published", "true"}}, &out); err != nil {
			return res, err
		}
		postID = out.PostID
	default:
		params := url.Values{"message": {text}, "access_token": {tok}}
		for i, m := range p.Media {
			var out struct {
				ID string `json:"id"`
			}
			if err := a.photo(ctx, page, tok, m, i, [][2]string{{"published", "false"}}, &out); err != nil {
				return res, err
			}
			ref, _ := json.Marshal(map[string]string{"media_fbid": out.ID})
			params.Set("attached_media["+itoa(i)+"]", string(ref))
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := a.Meta.Post(ctx, page+"/feed", params, &out); err != nil {
			return res, err
		}
		postID = out.ID
	}
	var link struct {
		PermalinkURL string `json:"permalink_url"`
	}
	_ = a.Meta.Get(ctx, postID, url.Values{"fields": {"permalink_url"}, "access_token": {tok}}, &link)
	ref := platform.RemoteRef{ID: postID, URL: link.PermalinkURL}
	if onPart != nil {
		if err := onPart(ref); err != nil {
			return res, err
		}
	}
	res.Parts = append(res.Parts, ref)
	res.Permalink = ref.URL
	return res, nil
}

// photo uploads an image to the Page's photos.
func (a *Adapter) photo(ctx context.Context, page, tok string, m platform.Media, i int, fields [][2]string, out any) error {
	data, err := m.Read(ctx)
	if err != nil {
		return err
	}
	if m.Alt != "" {
		fields = append(fields, [2]string{"alt_text_custom", m.Alt})
	}
	fields = append(fields, [2]string{"access_token", tok})
	return a.Meta.PostMultipart(ctx, page+"/photos", fields, platform.File{Field: "source", Name: m.Filename(i), Type: m.Type, Data: data}, out)
}

// Engagement reads each post's reactions (as likes), comments (replies) and
// shares (reposts). A post Facebook no longer has is left out.
func (a *Adapter) Engagement(ctx context.Context, c platform.Credentials, refs []platform.RemoteRef) (map[string]platform.Counts, error) {
	out := map[string]platform.Counts{}
	for _, r := range refs {
		var post struct {
			Reactions struct {
				Summary struct {
					TotalCount int64 `json:"total_count"`
				} `json:"summary"`
			} `json:"reactions"`
			Comments struct {
				Summary struct {
					TotalCount int64 `json:"total_count"`
				} `json:"summary"`
			} `json:"comments"`
			Shares struct {
				Count int64 `json:"count"`
			} `json:"shares"`
		}
		err := a.Meta.Get(ctx, r.ID, url.Values{"fields": {"reactions.summary(total_count).limit(0),comments.summary(total_count).limit(0),shares"},
			"access_token": {c["access_token"]}}, &post)
		if platform.KindOf(err) == platform.Rejected {
			continue // deleted
		}
		if err != nil {
			return nil, err
		}
		out[r.ID] = platform.Counts{Likes: post.Reactions.Summary.TotalCount, Replies: post.Comments.Summary.TotalCount, Reposts: post.Shares.Count}
	}
	return out, nil
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
