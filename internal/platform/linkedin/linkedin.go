// SPDX-License-Identifier: AGPL-3.0-or-later

// Package linkedin posts to a member's LinkedIn feed. The org's LinkedIn
// app needs the products "Sign In with LinkedIn using OpenID Connect" and
// "Share on LinkedIn" (scopes openid, profile, w_member_social). A channel
// connects one of two ways:
//
//   - a sign-in through that app (ADR 0021);
//   - an access token pasted from the app's OAuth token tools.
//
// Tokens last 60 days. LinkedIn issues refresh tokens only to apps it has
// approved for them; with one, the token is renewed, and without one the
// member signs in again before it expires (Araldo says when, a week ahead).
//
// LinkedIn has no idempotency key, so the adapter is not idempotent.
package linkedin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Defaults.
const (
	DefaultAPI = "https://api.linkedin.com"
	// DefaultVersion is the Posts API version sent as LinkedIn-Version.
	// LinkedIn retires a version about a year after it ships; a channel can
	// name a newer one in its api_version setting.
	DefaultVersion = "202606"
	// DefaultAuth is where members sign in and tokens are issued.
	DefaultAuth = "https://www.linkedin.com/oauth/v2"
	scopes      = "openid profile w_member_social"
)

// Adapter posts to LinkedIn.
type Adapter struct {
	Client *http.Client
	// API and Auth are LinkedIn's endpoints; tests replace them.
	API, Auth string
	// Now is the clock for token expiry; tests replace it.
	Now func() time.Time
	// Sleep waits while LinkedIn processes a video; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// New returns a LinkedIn adapter.
func New(client *http.Client) *Adapter {
	return &Adapter{Client: client, API: DefaultAPI, Auth: DefaultAuth, Now: time.Now, Sleep: sleep}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (a *Adapter) Provider() platform.Provider { return platform.LinkedIn }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.LinkedIn)
	return r
}

func (a *Adapter) Fields() []platform.Field {
	return []platform.Field{
		{Name: "access_token", Label: "Access token", Secret: true,
			Help: "linkedin.com/developers → your app → Docs and tools → OAuth token tools, with openid, profile and w_member_social. It lasts 60 days."},
		{Name: "api_version", Label: "API version", Optional: true, Default: DefaultVersion,
			Help: "LinkedIn-Version (YYYYMM); change it when LinkedIn retires this one"},
	}
}

func (a *Adapter) Idempotent() bool { return false }

func (a *Adapter) AuthorizeURL(app platform.App, redirectURI, state, _ string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {app.ClientID}, "redirect_uri": {redirectURI}, "scope": {scopes}, "state": {state}}
	return a.Auth + "/authorization?" + q.Encode()
}

// Exchange trades the code for a token and reads the member
// (https://learn.microsoft.com/en-us/linkedin/shared/authentication/authorization-code-flow).
func (a *Adapter) Exchange(ctx context.Context, app platform.App, redirectURI, code, _ string) ([]platform.Connection, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"client_id": {app.ClientID}, "client_secret": {app.ClientSecret}}
	t, err := platform.RequestToken(ctx, a.Client, a.Auth+"/accessToken", form, nil)
	if err != nil {
		return nil, err
	}
	c := platform.Credentials{"access_token": t.AccessToken}
	if t.RefreshToken != "" {
		c["refresh_token"] = t.RefreshToken
	}
	acct, err := a.Verify(ctx, c)
	if err != nil {
		return nil, err
	}
	return []platform.Connection{{Account: acct, Credentials: c, ExpiresAt: t.Expiry(a.Now())}}, nil
}

// Refresh renews the token when LinkedIn gave a refresh token
// (https://learn.microsoft.com/en-us/linkedin/shared/authentication/programmatic-refresh-tokens),
// and otherwise says the member must sign in again.
func (a *Adapter) Refresh(ctx context.Context, app platform.App, c platform.Credentials) (platform.Credentials, *time.Time, error) {
	if c["refresh_token"] == "" {
		return nil, nil, platform.ErrNoRefresh
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c["refresh_token"]},
		"client_id": {app.ClientID}, "client_secret": {app.ClientSecret}}
	t, err := platform.RequestToken(ctx, a.Client, a.Auth+"/accessToken", form, nil)
	if err != nil {
		return nil, nil, err
	}
	fresh := platform.Credentials{"access_token": t.AccessToken, "refresh_token": t.RefreshToken}
	if fresh["refresh_token"] == "" {
		fresh["refresh_token"] = c["refresh_token"]
	}
	return fresh, t.Expiry(a.Now()), nil
}

func (a *Adapter) headers(c platform.Credentials) (map[string]string, error) {
	token := strings.TrimSpace(c["access_token"])
	if token == "" {
		return nil, platform.Errorf(platform.AuthRevoked, "an access token is required")
	}
	version := strings.TrimSpace(c["api_version"])
	if version == "" {
		version = DefaultVersion
	}
	return map[string]string{"Authorization": "Bearer " + token, "LinkedIn-Version": version, "X-Restli-Protocol-Version": "2.0.0"}, nil
}

// me is the member the token belongs to (OpenID Connect userinfo).
func (a *Adapter) me(ctx context.Context, h map[string]string) (string, string, error) {
	var info struct {
		Sub  string `json:"sub"`
		Name string `json:"name"`
	}
	if err := platform.JSON(ctx, a.Client, http.MethodGet, a.API+"/v2/userinfo", map[string]string{"Authorization": h["Authorization"]}, nil, &info); err != nil {
		return "", "", expired(err)
	}
	if info.Sub == "" {
		return "", "", platform.Errorf(platform.AuthRevoked, "the token does not identify a member: create it with the openid and profile scopes")
	}
	return info.Sub, info.Name, nil
}

func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	h, err := a.headers(c)
	if err != nil {
		return platform.Account{}, err
	}
	sub, name, err := a.me(ctx, h)
	if err != nil {
		return platform.Account{}, err
	}
	return platform.Account{ExternalID: sub, Handle: name, DisplayName: name + " (LinkedIn)"}, nil
}

type post struct {
	Author                    string       `json:"author"`
	Commentary                string       `json:"commentary"`
	Visibility                string       `json:"visibility"`
	Distribution              distribution `json:"distribution"`
	Content                   *content     `json:"content,omitempty"`
	LifecycleState            string       `json:"lifecycleState"`
	IsReshareDisabledByAuthor bool         `json:"isReshareDisabledByAuthor"`
}

type distribution struct {
	FeedDistribution               string   `json:"feedDistribution"`
	TargetEntities                 []string `json:"targetEntities"`
	ThirdPartyDistributionChannels []string `json:"thirdPartyDistributionChannels"`
}

type content struct {
	Media      *image      `json:"media,omitempty"`
	MultiImage *multiImage `json:"multiImage,omitempty"`
}

// videoWait is the wait between checks on a processing video.
const videoWait = 3 * time.Second

type image struct {
	ID      string `json:"id"`
	AltText string `json:"altText,omitempty"`
}

type multiImage struct {
	Images []image `json:"images"`
}

// Publish posts the single part (LinkedIn has no threads) with any images
// (https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/posts-api).
func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	return a.publish(ctx, c, p, onPart, func(h map[string]string) (string, error) {
		sub, _, err := a.me(ctx, h)
		if err != nil {
			return "", err
		}
		return "urn:li:person:" + sub, nil
	})
}

// publish posts as the author authorOf names: a member, or a Page.
func (a *Adapter) publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error,
	authorOf func(h map[string]string) (string, error)) (platform.Result, error) {
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	if len(p.Posted) >= len(p.Parts) {
		return res, nil
	}
	h, err := a.headers(c)
	if err != nil {
		return res, err
	}
	author, err := authorOf(h)
	if err != nil {
		return res, err
	}
	body := post{Author: author, Commentary: Commentary(p.Parts[0]), Visibility: "PUBLIC", LifecycleState: "PUBLISHED",
		Distribution: distribution{FeedDistribution: "MAIN_FEED", TargetEntities: []string{}, ThirdPartyDistributionChannels: []string{}}}
	switch {
	case len(p.Media) == 1 && p.Media[0].IsVideo():
		urn, err := a.uploadVideo(ctx, h, author, p.Media[0])
		if err != nil {
			return res, err
		}
		body.Content = &content{Media: &image{ID: urn}}
	case len(p.Media) > 0:
		imgs, err := a.upload(ctx, h, author, p.Media)
		if err != nil {
			return res, err
		}
		if len(imgs) == 1 {
			body.Content = &content{Media: &imgs[0]}
		} else {
			body.Content = &content{MultiImage: &multiImage{Images: imgs}}
		}
	}
	urn, err := a.create(ctx, h, body)
	if err != nil {
		return res, err
	}
	ref := platform.RemoteRef{ID: urn, URL: "https://www.linkedin.com/feed/update/" + urn + "/"}
	if onPart != nil {
		if err := onPart(ref); err != nil {
			return res, err
		}
	}
	res.Parts = append(res.Parts, ref)
	res.Permalink = ref.URL
	return res, nil
}

// create posts body and returns the new post's URN, which LinkedIn sends
// in the x-restli-id header of an empty 201.
func (a *Adapter) create(ctx context.Context, h map[string]string, body post) (string, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return "", &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.API+"/rest/posts", bytes.NewReader(b))
	if err != nil {
		return "", &platform.Error{Kind: platform.Rejected, Code: "request", Err: err}
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := platform.Do(a.Client, req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", expired(platform.Classify(resp, raw))
	}
	urn := resp.Header.Get("X-Restli-Id")
	if urn == "" {
		// Accepted, but we cannot tell which post: whether it is visible
		// is for a person to check.
		return "", platform.Errorf(platform.Uncertain, "LinkedIn accepted the post but did not say which it was")
	}
	return urn, nil
}

// upload sends each image through the Images API: initialize, then PUT
// the bytes to the URL LinkedIn returns
// (https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/images-api).
func (a *Adapter) upload(ctx context.Context, h map[string]string, owner string, media []platform.Media) ([]image, error) {
	out := make([]image, 0, len(media))
	for _, m := range media {
		data, err := m.Read(ctx)
		if err != nil {
			return nil, err
		}
		var init struct {
			Value struct {
				UploadURL string `json:"uploadUrl"`
				Image     string `json:"image"`
			} `json:"value"`
		}
		err = platform.JSON(ctx, a.Client, http.MethodPost, a.API+"/rest/images?action=initializeUpload", h,
			map[string]any{"initializeUploadRequest": map[string]string{"owner": owner}}, &init)
		if err != nil {
			return nil, expired(err)
		}
		if init.Value.UploadURL == "" || init.Value.Image == "" {
			return nil, platform.Errorf(platform.Transient, "LinkedIn returned no upload URL")
		}
		if err := platform.Send(ctx, a.Client, http.MethodPut, init.Value.UploadURL, map[string]string{"Authorization": h["Authorization"]},
			data, m.Type, nil); err != nil {
			return nil, expired(err)
		}
		out = append(out, image{ID: init.Value.Image, AltText: m.Alt})
	}
	return out, nil
}

// expired explains a 401: LinkedIn's member tokens last 60 days.
func expired(err error) error {
	pe, ok := err.(*platform.Error) //nolint:errorlint // platform helpers return *Error directly
	if ok && pe.Kind == platform.AuthRevoked {
		pe.Msg = "the access token expired or was revoked (LinkedIn tokens last 60 days): sign in again or paste a new one. " + pe.Msg
	}
	return err
}

var hashtagRE = regexp.MustCompile(`(^|\s)#([\p{L}\p{N}]+)`)

// reserved are the characters LinkedIn's "little text" format gives
// meaning to; as text they must be escaped
// (https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/little-text-format).
const reserved = `\|{}@[]()<>#*_~`

// Commentary writes text in LinkedIn's little text format: reserved
// characters escaped, and hashtags as hashtag templates so they stay links.
func Commentary(text string) string {
	var b strings.Builder
	last := 0
	for _, m := range hashtagRE.FindAllStringSubmatchIndex(text, -1) {
		tagStart := m[4] - 1 // the '#'
		b.WriteString(escape(text[last:tagStart]))
		b.WriteString(`{hashtag|\#|` + text[m[4]:m[5]] + `}`)
		last = m[5]
	}
	b.WriteString(escape(text[last:]))
	return b.String()
}

func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(reserved, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// uploadVideo sends a video through the Videos API: initialize with its
// size, put each part LinkedIn asks for, read from the stream in order,
// finalize with the parts' ETags, and wait until LinkedIn has processed it
// (https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/videos-api).
func (a *Adapter) uploadVideo(ctx context.Context, h map[string]string, owner string, m platform.Media) (string, error) {
	var init struct {
		Value struct {
			Video        string `json:"video"`
			UploadToken  string `json:"uploadToken"`
			Instructions []struct {
				UploadURL string `json:"uploadUrl"`
				FirstByte int64  `json:"firstByte"`
				LastByte  int64  `json:"lastByte"`
			} `json:"uploadInstructions"`
		} `json:"value"`
	}
	err := platform.JSON(ctx, a.Client, http.MethodPost, a.API+"/rest/videos?action=initializeUpload", h,
		map[string]any{"initializeUploadRequest": map[string]any{"owner": owner, "fileSizeBytes": m.Size, "uploadCaptions": false, "uploadThumbnail": false}},
		&init)
	if err != nil {
		return "", expired(err)
	}
	v := init.Value
	if v.Video == "" || len(v.Instructions) == 0 {
		return "", platform.Errorf(platform.Transient, "LinkedIn returned no upload instructions")
	}
	rc, err := platform.OpenVideo(ctx, m)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	etags := make([]string, 0, len(v.Instructions))
	var read int64
	for _, in := range v.Instructions {
		n := in.LastByte - in.FirstByte + 1
		if in.FirstByte != read || n <= 0 {
			return "", platform.Errorf(platform.Rejected, "LinkedIn asked for bytes %d to %d out of order", in.FirstByte, in.LastByte)
		}
		part := make([]byte, n)
		if _, err := io.ReadFull(rc, part); err != nil {
			return "", &platform.Error{Kind: platform.Transient, Code: "media_unreadable", Msg: "reading the video", Err: err}
		}
		read += n
		etag, err := a.putPart(ctx, h, in.UploadURL, part)
		if err != nil {
			return "", err
		}
		etags = append(etags, etag)
	}
	err = platform.JSON(ctx, a.Client, http.MethodPost, a.API+"/rest/videos?action=finalizeUpload", h,
		map[string]any{"finalizeUploadRequest": map[string]any{"video": v.Video, "uploadToken": v.UploadToken, "uploadedPartIds": etags}}, nil)
	if err != nil {
		return "", expired(err)
	}
	for {
		var st struct {
			Status string `json:"status"`
		}
		if err := platform.JSON(ctx, a.Client, http.MethodGet, a.API+"/rest/videos/"+url.QueryEscape(v.Video), h, nil, &st); err != nil {
			return "", expired(err)
		}
		switch st.Status {
		case "AVAILABLE":
			return v.Video, nil
		case "PROCESSING_FAILED":
			return "", &platform.Error{Kind: platform.Rejected, Code: "video_rejected", Msg: "LinkedIn could not process the video"}
		}
		if err := a.Sleep(ctx, videoWait); err != nil {
			return "", &platform.Error{Kind: platform.Transient, Code: "media_processing", Msg: "LinkedIn was still processing the video", Err: err}
		}
	}
}

// putPart uploads one part and returns its ETag.
func (a *Adapter) putPart(ctx context.Context, h map[string]string, uploadURL string, part []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(part))
	if err != nil {
		return "", &platform.Error{Kind: platform.Rejected, Code: "request", Err: err}
	}
	req.Header.Set("Authorization", h["Authorization"])
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := platform.Do(a.Client, req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return "", expired(platform.Classify(resp, raw))
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		return "", platform.Errorf(platform.Transient, "LinkedIn accepted a video part without an ETag")
	}
	return etag, nil
}
