// SPDX-License-Identifier: AGPL-3.0-or-later

// Package threads posts to Threads through Meta's Threads API, with a
// token from an OAuth sign-in (ADR 0021) through the org's Threads app
// (scopes threads_basic, threads_content_publish, threads_manage_insights).
// Tokens last 60 days and are renewed before they expire.
//
// Posting is two steps: create a container, then publish it. Threads
// fetches images from a link, so posts with images need the install's API
// to be reachable from the internet. There is no idempotency key, so the
// adapter is not idempotent.
package threads

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Defaults.
const (
	DefaultGraph     = "https://graph.threads.net"
	DefaultAuthorize = "https://threads.net/oauth/authorize"
	scopes           = "threads_basic,threads_content_publish,threads_manage_insights"
)

// Adapter posts to Threads.
type Adapter struct {
	Client *http.Client
	// Graph and Authorize are Meta's endpoints; tests replace them.
	Graph, Authorize string
	// Poll is how long to wait between checks on a container Threads is
	// still processing; tests shorten it.
	Poll time.Duration
	// Now is the clock for token expiry; tests replace it.
	Now func() time.Time
}

// New returns a Threads adapter.
func New(client *http.Client) *Adapter {
	return &Adapter{Client: client, Graph: DefaultGraph, Authorize: DefaultAuthorize, Poll: 2 * time.Second, Now: time.Now}
}

func (a *Adapter) Provider() platform.Provider { return platform.Threads }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Threads)
	return r
}

// Fields are none: Threads channels connect with OAuth.
func (a *Adapter) Fields() []platform.Field { return nil }

func (a *Adapter) Idempotent() bool { return false }

func (a *Adapter) AuthorizeURL(app platform.App, redirectURI, state, _ string) string {
	q := url.Values{"client_id": {app.ClientID}, "redirect_uri": {redirectURI}, "scope": {scopes}, "response_type": {"code"}, "state": {state}}
	return a.Authorize + "?" + q.Encode()
}

type token struct {
	AccessToken string      `json:"access_token"`
	UserID      json.Number `json:"user_id"`
	ExpiresIn   int64       `json:"expires_in"`
}

// Exchange trades the code for a short-lived token, then that for a
// long-lived (60-day) one, and reads the profile
// (https://developers.facebook.com/docs/threads/get-started/get-access-tokens-and-permissions).
func (a *Adapter) Exchange(ctx context.Context, app platform.App, redirectURI, code, _ string) ([]platform.Connection, error) {
	form := url.Values{"client_id": {app.ClientID}, "client_secret": {app.ClientSecret}, "grant_type": {"authorization_code"},
		"redirect_uri": {redirectURI}, "code": {code}}
	var short token
	if err := platform.Send(ctx, a.Client, http.MethodPost, a.Graph+"/oauth/access_token", nil, []byte(form.Encode()),
		"application/x-www-form-urlencoded", &short); err != nil {
		return nil, err
	}
	q := url.Values{"grant_type": {"th_exchange_token"}, "client_secret": {app.ClientSecret}, "access_token": {short.AccessToken}}
	var long token
	if err := platform.JSON(ctx, a.Client, http.MethodGet, a.Graph+"/access_token?"+q.Encode(), nil, nil, &long); err != nil {
		return nil, err
	}
	creds := platform.Credentials{"access_token": long.AccessToken}
	acct, err := a.Verify(ctx, creds)
	if err != nil {
		return nil, err
	}
	creds["user_id"] = acct.ExternalID
	return []platform.Connection{{Account: acct, Credentials: creds, ExpiresAt: a.expiry(long.ExpiresIn)}}, nil
}

func (a *Adapter) expiry(seconds int64) *time.Time {
	if seconds <= 0 {
		return nil
	}
	t := a.Now().Add(time.Duration(seconds) * time.Second)
	return &t
}

// Refresh renews a long-lived token for another 60 days.
func (a *Adapter) Refresh(ctx context.Context, _ platform.App, c platform.Credentials) (platform.Credentials, *time.Time, error) {
	q := url.Values{"grant_type": {"th_refresh_token"}, "access_token": {c["access_token"]}}
	var t token
	if err := platform.JSON(ctx, a.Client, http.MethodGet, a.Graph+"/refresh_access_token?"+q.Encode(), nil, nil, &t); err != nil {
		return nil, nil, revoked(err)
	}
	fresh := platform.Credentials{"access_token": t.AccessToken, "user_id": c["user_id"]}
	return fresh, a.expiry(t.ExpiresIn), nil
}

func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	var me struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	q := url.Values{"fields": {"id,username,name"}, "access_token": {c["access_token"]}}
	if err := platform.JSON(ctx, a.Client, http.MethodGet, a.Graph+"/v1.0/me?"+q.Encode(), nil, nil, &me); err != nil {
		return platform.Account{}, revoked(err)
	}
	name := me.Name
	if name == "" {
		name = "@" + me.Username
	}
	return platform.Account{ExternalID: me.ID, Handle: "@" + me.Username, DisplayName: name, URL: "https://www.threads.net/@" + me.Username}, nil
}

// post sends a Graph API call with parameters in the query, as Threads
// documents them.
func (a *Adapter) post(ctx context.Context, path string, params url.Values, out any) error {
	return platform.Send(ctx, a.Client, http.MethodPost, a.Graph+"/v1.0/"+path+"?"+params.Encode(), nil, nil, "", out)
}

// Publish posts each part, the first with the images, each later part a
// reply to the one before
// (https://developers.facebook.com/docs/threads/posts).
func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	user, tok := c["user_id"], c["access_token"]
	if user == "" || tok == "" {
		return platform.Result{}, platform.Errorf(platform.AuthRevoked, "the channel has no token: reconnect it")
	}
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	replyTo := ""
	if len(p.Posted) > 0 {
		replyTo = p.Posted[len(p.Posted)-1].ID
	}
	for i := len(p.Posted); i < len(p.Parts); i++ {
		var media []platform.Media
		if i == 0 {
			media = p.Media
		}
		container, err := a.container(ctx, user, tok, p.Parts[i], replyTo, media)
		if err != nil {
			return res, revoked(err)
		}
		var published struct {
			ID string `json:"id"`
		}
		if err := a.post(ctx, user+"/threads_publish", url.Values{"creation_id": {container}, "access_token": {tok}}, &published); err != nil {
			return res, revoked(err)
		}
		ref := platform.RemoteRef{ID: published.ID, URL: a.permalink(ctx, tok, published.ID)}
		if onPart != nil {
			if err := onPart(ref); err != nil {
				return res, err
			}
		}
		res.Parts = append(res.Parts, ref)
		replyTo = published.ID
	}
	if len(res.Parts) > 0 {
		res.Permalink = res.Parts[0].URL
	}
	return res, nil
}

// container creates the container for one part (text, an image, a video,
// or a carousel of images) and waits until Threads has processed it.
func (a *Adapter) container(ctx context.Context, user, tok, text, replyTo string, media []platform.Media) (string, error) {
	params := url.Values{"text": {text}, "access_token": {tok}}
	if replyTo != "" {
		params.Set("reply_to_id", replyTo)
	}
	switch {
	case len(media) == 1 && media[0].IsVideo():
		if media[0].URL == "" {
			return "", noLink()
		}
		params.Set("media_type", "VIDEO")
		params.Set("video_url", media[0].URL)
	case len(media) == 1:
		if media[0].URL == "" {
			return "", noLink()
		}
		params.Set("media_type", "IMAGE")
		params.Set("image_url", media[0].URL)
	case len(media) > 1:
		var children []string
		for _, m := range media {
			if m.URL == "" {
				return "", noLink()
			}
			var item struct {
				ID string `json:"id"`
			}
			if err := a.post(ctx, user+"/threads", url.Values{"media_type": {"IMAGE"}, "image_url": {m.URL}, "is_carousel_item": {"true"},
				"access_token": {tok}}, &item); err != nil {
				return "", err
			}
			if err := a.ready(ctx, tok, item.ID); err != nil {
				return "", err
			}
			children = append(children, item.ID)
		}
		params.Set("media_type", "CAROUSEL")
		params.Set("children", strings.Join(children, ","))
	default:
		params.Set("media_type", "TEXT")
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := a.post(ctx, user+"/threads", params, &out); err != nil {
		return "", err
	}
	return out.ID, a.ready(ctx, tok, out.ID)
}

func noLink() error {
	return &platform.Error{Kind: platform.Rejected, Code: "media_link_missing",
		Msg: "Threads fetches media from a link, and this install cannot make one: its API must be public at ARALDO_BASE_URL"}
}

// ready waits for a container to finish processing
// (https://developers.facebook.com/docs/threads/troubleshooting#publishing-does-not-return-a-media-id).
func (a *Adapter) ready(ctx context.Context, tok, container string) error {
	for {
		var st struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"error_message"`
		}
		q := url.Values{"fields": {"status,error_message"}, "access_token": {tok}}
		if err := platform.JSON(ctx, a.Client, http.MethodGet, a.Graph+"/v1.0/"+url.PathEscape(container)+"?"+q.Encode(), nil, nil, &st); err != nil {
			return err
		}
		switch st.Status {
		case "FINISHED", "PUBLISHED", "":
			return nil
		case "ERROR", "EXPIRED":
			return &platform.Error{Kind: platform.Rejected, Code: "media_rejected", Msg: "Threads could not process the post: " + st.ErrorMessage}
		}
		select {
		case <-ctx.Done():
			return &platform.Error{Kind: platform.Transient, Code: "processing", Msg: "Threads was still processing the post", Err: ctx.Err()}
		case <-time.After(a.Poll):
		}
	}
}

// permalink is a post's link; empty if Threads does not say.
func (a *Adapter) permalink(ctx context.Context, tok, mediaID string) string {
	var out struct {
		Permalink string `json:"permalink"`
	}
	q := url.Values{"fields": {"permalink"}, "access_token": {tok}}
	_ = platform.JSON(ctx, a.Client, http.MethodGet, a.Graph+"/v1.0/"+url.PathEscape(mediaID)+"?"+q.Encode(), nil, nil, &out)
	return out.Permalink
}

// Engagement reads each post's insights: views, likes, replies, reposts
// and quotes (https://developers.facebook.com/docs/threads/insights). A
// post Threads no longer has is left out.
func (a *Adapter) Engagement(ctx context.Context, c platform.Credentials, refs []platform.RemoteRef) (map[string]platform.Counts, error) {
	out := map[string]platform.Counts{}
	for _, r := range refs {
		var res struct {
			Data []struct {
				Name   string `json:"name"`
				Values []struct {
					Value int64 `json:"value"`
				} `json:"values"`
				TotalValue struct {
					Value int64 `json:"value"`
				} `json:"total_value"`
			} `json:"data"`
		}
		q := url.Values{"metric": {"views,likes,replies,reposts,quotes"}, "access_token": {c["access_token"]}}
		err := platform.JSON(ctx, a.Client, http.MethodGet, a.Graph+"/v1.0/"+url.PathEscape(r.ID)+"/insights?"+q.Encode(), nil, nil, &res)
		if platform.KindOf(err) == platform.Rejected {
			continue // deleted
		}
		if err != nil {
			return nil, revoked(err)
		}
		var cn platform.Counts
		for _, d := range res.Data {
			v := d.TotalValue.Value
			if len(d.Values) > 0 {
				v = d.Values[0].Value
			}
			switch d.Name {
			case "views":
				views := v
				cn.Views = &views
			case "likes":
				cn.Likes = v
			case "replies":
				cn.Replies = v
			case "reposts":
				cn.Reposts = v
			case "quotes":
				cn.Quotes = v
			}
		}
		out[r.ID] = cn
	}
	return out, nil
}

// revoked reads Meta's error for an expired or revoked token (code 190)
// as AuthRevoked, whatever the HTTP status.
func revoked(err error) error {
	pe, ok := err.(*platform.Error) //nolint:errorlint // platform helpers return *Error directly
	if ok && pe.Kind == platform.Rejected && (strings.Contains(pe.Msg, `"code":190`) || strings.Contains(pe.Msg, "OAuthException")) {
		pe.Kind, pe.Code = platform.AuthRevoked, "token_expired"
	}
	return err
}
