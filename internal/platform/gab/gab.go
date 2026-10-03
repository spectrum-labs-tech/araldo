// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gab publishes to Gab with an access token the account owner
// creates (gab.com -> Settings -> Development).
//
// Gab Social is a fork of Mastodon and serves the same API shape, so this
// adapter is close to the Mastodon one. Three things differ, and together they
// are why Gab is a provider of its own rather than a Mastodon channel:
//
//   - Gab is one hosted service, so there is no server to choose: the only
//     credential is a token.
//   - Posts are up to 3000 characters, not Mastodon's 500.
//   - The fork predates parts of the modern Mastodon API. It uses the v1 media
//     endpoint, which every Mastodon version has served, rather than v2 with
//     its processing wait, and it makes no promise about Idempotency-Key --
//     see Idempotent.
package gab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// DefaultBaseURL is Gab's API root. Gab is a single hosted service, unlike
// Mastodon, so this is not a channel setting.
const DefaultBaseURL = "https://gab.com"

// Adapter publishes to Gab.
type Adapter struct {
	Client *http.Client
	// BaseURL is where the API lives; tests point it at a stub.
	BaseURL string
}

// New returns a Gab adapter.
func New(client *http.Client) *Adapter { return &Adapter{Client: client, BaseURL: DefaultBaseURL} }

func (a *Adapter) Provider() platform.Provider { return platform.Gab }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Gab)
	return r
}

func (a *Adapter) Fields() []platform.Field {
	return []platform.Field{
		{Name: "access_token", Label: "Access token",
			Help:   "gab.com -> Settings -> Development -> New application (write:statuses, write:media, read:accounts)",
			Secret: true},
	}
}

// Idempotent reports false. Mastodon honors Idempotency-Key on statuses and so
// can be retried safely, but Gab's fork is older than that behavior and
// documents nothing about it. This adapter sends the header anyway -- it costs
// nothing and helps if Gab does honor it -- but claiming an idempotency we
// cannot verify would let the publisher retry an uncertain attempt and post
// twice. Saying false leaves such an attempt for a person instead (ADR 0011).
func (a *Adapter) Idempotent() bool { return false }

func (a *Adapter) base() string {
	if a.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(a.BaseURL, "/")
}

// auth returns the request headers, or AuthRevoked when there is no token to
// send -- the same answer the platform itself would give.
func (a *Adapter) auth(c platform.Credentials) (map[string]string, error) {
	tok := strings.TrimSpace(c["access_token"])
	if tok == "" {
		return nil, platform.Errorf(platform.AuthRevoked, "access token is required")
	}
	return map[string]string{"Authorization": "Bearer " + tok}, nil
}

type account struct {
	ID          string `json:"id"`
	Acct        string `json:"acct"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	URL         string `json:"url"`
}

func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	h, err := a.auth(c)
	if err != nil {
		return platform.Account{}, err
	}
	var acct account
	if err := platform.JSON(ctx, a.Client, http.MethodGet, a.base()+"/api/v1/accounts/verify_credentials", h, nil, &acct); err != nil {
		return platform.Account{}, err
	}
	handle := acct.Acct
	if handle == "" {
		handle = acct.Username
	}
	name := acct.DisplayName
	if name == "" {
		name = handle
	}
	profile := acct.URL
	if profile == "" && handle != "" {
		profile = a.base() + "/" + handle
	}
	// Gab has one domain, so a bare @handle is unambiguous -- unlike Mastodon,
	// where the server is part of the identity.
	return platform.Account{ExternalID: acct.ID, Handle: "@" + handle, DisplayName: name, URL: profile}, nil
}

type status struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Error string `json:"error"`
}

// Publish posts each part, replying to the one before it so a multi-part post
// reads as a thread. The body is form-encoded, which is what Gab's fork is
// known to take.
func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	h, err := a.auth(c)
	if err != nil {
		return platform.Result{}, err
	}
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	replyTo := ""
	if len(p.Posted) > 0 {
		replyTo = p.Posted[len(p.Posted)-1].ID
	}
	for i := len(p.Posted); i < len(p.Parts); i++ {
		form := url.Values{}
		form.Set("status", p.Parts[i])
		form.Set("visibility", "public")
		if replyTo != "" {
			form.Set("in_reply_to_id", replyTo)
		}
		// Media goes on the first part only (ADR 0017); a thread resumed after
		// the first part has already published it.
		if i == 0 && len(p.Media) > 0 {
			ids, err := a.upload(ctx, h, p.Media)
			if err != nil {
				return res, err
			}
			for _, id := range ids {
				form.Add("media_ids[]", id)
			}
		}
		headers := map[string]string{"Idempotency-Key": fmt.Sprintf("%s-%d", p.Key, i)}
		for k, v := range h {
			headers[k] = v
		}
		var st status
		err := platform.Send(ctx, a.Client, http.MethodPost, a.base()+"/api/v1/statuses", headers,
			[]byte(form.Encode()), "application/x-www-form-urlencoded", &st)
		if err != nil {
			return res, err
		}
		// A 2xx with no post in it: we cannot tell whether it published, and
		// this adapter is not idempotent, so a person decides (ADR 0011).
		if st.ID == "" {
			msg := st.Error
			if msg == "" {
				msg = "Gab accepted the request but returned no post"
			}
			return res, &platform.Error{Kind: platform.Uncertain, Code: "no_post_returned", Msg: msg}
		}
		ref := platform.RemoteRef{ID: st.ID, URL: st.URL}
		if onPart != nil {
			if err := onPart(ref); err != nil {
				return res, err
			}
		}
		res.Parts = append(res.Parts, ref)
		replyTo = st.ID
	}
	if len(res.Parts) > 0 {
		res.Permalink = res.Parts[0].URL
	}
	return res, nil
}

type attachment struct {
	ID string `json:"id"`
}

// upload attaches each image through the v1 media endpoint, which returns the
// attachment ready to use. Mastodon's v2 endpoint can answer before an image
// is processed and then has to be polled; v1 does not, and every Mastodon
// version has served it, so it is the safer of the two against a fork.
func (a *Adapter) upload(ctx context.Context, headers map[string]string, media []platform.Media) ([]string, error) {
	ids := make([]string, 0, len(media))
	for i, m := range media {
		data, err := m.Read(ctx)
		if err != nil {
			return nil, err
		}
		body, contentType, err := platform.Multipart([][2]string{{"description", m.Alt}},
			[]platform.File{{Field: "file", Name: m.Filename(i), Type: m.Type, Data: data}})
		if err != nil {
			return nil, &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
		}
		var att attachment
		err = platform.Send(ctx, a.Client, http.MethodPost, a.base()+"/api/v1/media", headers, body, contentType, &att)
		var pe *platform.Error
		if errors.As(err, &pe) && pe.Kind == platform.Rejected && strings.HasPrefix(pe.Msg, "HTTP 403") {
			pe.Code = "scope_missing"
			pe.Msg = "the access token cannot upload images: create one with the write:media scope and reconnect (" + pe.Msg + ")"
		}
		if err != nil {
			return nil, err
		}
		if att.ID == "" {
			return nil, &platform.Error{Kind: platform.Rejected, Code: "media_rejected", Msg: "Gab returned no attachment for an image"}
		}
		ids = append(ids, att.ID)
	}
	return ids, nil
}

// Engagement reads each post's like, repost and reply counts. Gab serves
// Mastodon's Status entity, so the field names are Mastodon's; it reports no
// quotes, which stay zero. A post Gab answers 404 for was deleted.
func (a *Adapter) Engagement(ctx context.Context, c platform.Credentials, refs []platform.RemoteRef) (map[string]platform.Counts, error) {
	h, err := a.auth(c)
	if err != nil {
		return nil, err
	}
	out := map[string]platform.Counts{}
	for _, r := range refs {
		var st struct {
			Likes   int64 `json:"favourites_count"` //nolint:misspell // the API's spelling
			Reblogs int64 `json:"reblogs_count"`
			Replies int64 `json:"replies_count"`
		}
		err := platform.JSON(ctx, a.Client, http.MethodGet, a.base()+"/api/v1/statuses/"+url.PathEscape(r.ID), h, nil, &st)
		var pe *platform.Error
		if errors.As(err, &pe) && pe.Kind == platform.Rejected && strings.HasPrefix(pe.Msg, "HTTP 404") {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[r.ID] = platform.Counts{Likes: st.Likes, Reposts: st.Reblogs, Replies: st.Replies}
	}
	return out, nil
}
