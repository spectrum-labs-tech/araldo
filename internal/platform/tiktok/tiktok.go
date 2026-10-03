// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tiktok posts videos to TikTok through the Content Posting API
// (https://developers.tiktok.com/doc/content-posting-api-get-started),
// ADR 0027. A channel connects with a sign-in through the org's TikTok app,
// which needs the Login Kit and Content Posting API products and the
// scopes user.info.basic and video.publish. Access tokens last a day and
// are renewed with the refresh token, which lasts a year.
//
// Until TikTok audits the app, every post it makes is private to the
// creator's own account (TikTok's rule for unaudited clients). A post is
// one video, its text the caption. TikTok has no idempotency key, so the
// adapter is not idempotent.
package tiktok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Defaults.
const (
	DefaultAPI  = "https://open.tiktokapis.com"
	DefaultAuth = "https://www.tiktok.com/v2/auth/authorize/"
	scopes      = "user.info.basic,video.publish"
	// chunkSize is each uploaded part but the last, which takes the rest;
	// TikTok takes parts of 5 to 64 MB.
	chunkSize = 10 << 20
	// minChunk is the smallest part TikTok takes, unless the video is
	// smaller.
	minChunk = 5 << 20
	// statusWait is the wait between checks on a post being processed.
	statusWait = 5 * time.Second
)

// Adapter posts to TikTok.
type Adapter struct {
	Client *http.Client
	// API and Auth are TikTok's endpoints; tests replace them.
	API, Auth string
	// Now is the clock for token expiry, and Sleep waits while TikTok
	// processes a post; tests replace them.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	// ChunkSize is the upload's part size; tests make it small.
	ChunkSize int64
}

// New returns a TikTok adapter.
func New(client *http.Client) *Adapter {
	return &Adapter{Client: client, API: DefaultAPI, Auth: DefaultAuth, Now: time.Now, Sleep: sleep, ChunkSize: chunkSize}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (a *Adapter) Provider() platform.Provider { return platform.TikTok }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.TikTok)
	return r
}

// Fields are none: a channel connects only by signing in.
func (a *Adapter) Fields() []platform.Field { return nil }

func (a *Adapter) Idempotent() bool { return false }

func (a *Adapter) AuthorizeURL(app platform.App, redirectURI, state, challenge string) string {
	q := url.Values{"client_key": {app.ClientID}, "redirect_uri": {redirectURI}, "response_type": {"code"}, "scope": {scopes},
		"state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	return a.Auth + "?" + q.Encode()
}

// token is TikTok's token answer, which names the account too.
type token struct {
	platform.Token
	OpenID string `json:"open_id"`
}

func (a *Adapter) token(ctx context.Context, form url.Values) (token, error) {
	var t token
	err := platform.Send(ctx, a.Client, http.MethodPost, a.API+"/v2/oauth/token/", nil, []byte(form.Encode()), "application/x-www-form-urlencoded", &t)
	if err != nil {
		if form.Get("grant_type") == "refresh_token" && platform.KindOf(err) == platform.Rejected {
			return t, &platform.Error{Kind: platform.AuthRevoked, Code: "refresh_refused", Msg: "TikTok refused to renew the token: sign in again"}
		}
		return t, err
	}
	if t.AccessToken == "" {
		return t, &platform.Error{Kind: platform.AuthRevoked, Code: "token_missing", Msg: "TikTok answered without a token: sign in again"}
	}
	return t, nil
}

// Exchange trades the code for tokens and names the account
// (https://developers.tiktok.com/doc/oauth-user-access-token-management).
func (a *Adapter) Exchange(ctx context.Context, app platform.App, redirectURI, code, verifier string) ([]platform.Connection, error) {
	t, err := a.token(ctx, url.Values{"client_key": {app.ClientID}, "client_secret": {app.ClientSecret}, "code": {code},
		"grant_type": {"authorization_code"}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}})
	if err != nil {
		return nil, err
	}
	c := platform.Credentials{"access_token": t.AccessToken, "refresh_token": t.RefreshToken, "open_id": t.OpenID}
	acct, err := a.Verify(ctx, c)
	if err != nil {
		return nil, err
	}
	c["username"] = strings.TrimPrefix(acct.Handle, "@")
	return []platform.Connection{{Account: acct, Credentials: c, ExpiresAt: t.Expiry(a.Now())}}, nil
}

// Refresh renews the access token, and the refresh token with it.
func (a *Adapter) Refresh(ctx context.Context, app platform.App, c platform.Credentials) (platform.Credentials, *time.Time, error) {
	if c["refresh_token"] == "" {
		return nil, nil, platform.ErrNoRefresh
	}
	t, err := a.token(ctx, url.Values{"client_key": {app.ClientID}, "client_secret": {app.ClientSecret},
		"grant_type": {"refresh_token"}, "refresh_token": {c["refresh_token"]}})
	if err != nil {
		return nil, nil, err
	}
	fresh := platform.Credentials{"access_token": t.AccessToken, "refresh_token": t.RefreshToken, "open_id": c["open_id"], "username": c["username"]}
	if fresh["refresh_token"] == "" {
		fresh["refresh_token"] = c["refresh_token"]
	}
	return fresh, t.Expiry(a.Now()), nil
}

// apiError is the error TikTok puts in every answer; "ok" is success.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// call sends a request to the API and reads its data, mapping TikTok's
// error codes (https://developers.tiktok.com/doc/content-posting-api-reference-direct-post).
func (a *Adapter) call(ctx context.Context, c platform.Credentials, method, path string, in, data any) error {
	tok := strings.TrimSpace(c["access_token"])
	if tok == "" {
		return platform.Errorf(platform.AuthRevoked, "an access token is required: sign in again")
	}
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, a.API+path, strings.NewReader(string(body)))
	if err != nil {
		return &platform.Error{Kind: platform.Rejected, Code: "request", Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp, err := platform.Do(a.Client, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &platform.Error{Kind: platform.Uncertain, Code: "network", Err: err}
	}
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error apiError        `json:"error"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return platform.Classify(resp, raw)
	}
	if env.Error.Code != "" && env.Error.Code != "ok" {
		return classify(env.Error, resp)
	}
	if resp.StatusCode/100 != 2 {
		return platform.Classify(resp, raw)
	}
	if data != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, data); err != nil {
			return &platform.Error{Kind: platform.Uncertain, Code: "decode", Err: err}
		}
	}
	return nil
}

func classify(e apiError, resp *http.Response) error {
	msg := e.Code + ": " + e.Message
	switch {
	case e.Code == "access_token_invalid", e.Code == "scope_not_authorized", e.Code == "scope_permission_missed":
		return &platform.Error{Kind: platform.AuthRevoked, Code: e.Code, Msg: msg}
	case e.Code == "rate_limit_exceeded", e.Code == "spam_risk_too_many_posts", e.Code == "spam_risk_user_banned_from_posting":
		return &platform.Error{Kind: platform.RateLimited, Code: e.Code, Msg: msg, RetryAfter: platform.RetryAfter(resp.Header, time.Now())}
	case e.Code == "unaudited_client_can_only_post_to_private_accounts":
		return &platform.Error{Kind: platform.Rejected, Code: e.Code,
			Msg: "TikTok has not audited this app, so it can post only to a private account: make the account private, or have TikTok audit the app. " + msg}
	case resp.StatusCode >= 500:
		return &platform.Error{Kind: platform.Transient, Code: e.Code, Msg: msg}
	}
	return &platform.Error{Kind: platform.Rejected, Code: e.Code, Msg: msg}
}

func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	var out struct {
		User struct {
			OpenID      string `json:"open_id"`
			DisplayName string `json:"display_name"`
			Username    string `json:"username"`
		} `json:"user"`
	}
	if err := a.call(ctx, c, http.MethodGet, "/v2/user/info/?fields=open_id,display_name,username", nil, &out); err != nil {
		return platform.Account{}, err
	}
	u := out.User
	acct := platform.Account{ExternalID: u.OpenID, Handle: "@" + u.Username, DisplayName: u.DisplayName + " (TikTok)"}
	if u.Username != "" {
		acct.URL = "https://www.tiktok.com/@" + u.Username
	}
	return acct, nil
}

// creator is what TikTok lets the account post now.
type creator struct {
	PrivacyLevels        []string `json:"privacy_level_options"`
	MaxVideoDurationSecs int      `json:"max_video_post_duration_sec"`
	CommentDisabled      bool     `json:"comment_disabled"`
	DuetDisabled         bool     `json:"duet_disabled"`
	StitchDisabled       bool     `json:"stitch_disabled"`
}

// Publish posts the video: it asks what the creator may post, starts a
// file upload, sends the video in parts, and waits for TikTok to post it
// (https://developers.tiktok.com/doc/content-posting-api-media-transfer-guide).
func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	if len(p.Posted) >= len(p.Parts) {
		return res, nil
	}
	if len(p.Media) != 1 || !p.Media[0].IsVideo() {
		return res, &platform.Error{Kind: platform.Rejected, Code: "media_required", Msg: "a TikTok post is one video"}
	}
	v := p.Media[0]
	var cr creator
	if err := a.call(ctx, c, http.MethodPost, "/v2/post/publish/creator_info/query/", nil, &cr); err != nil {
		return res, err
	}
	if cr.MaxVideoDurationSecs > 0 && v.Duration > time.Duration(cr.MaxVideoDurationSecs)*time.Second {
		return res, &platform.Error{Kind: platform.Rejected, Code: "video_too_long",
			Msg: fmt.Sprintf("this TikTok account can post videos up to %d seconds", cr.MaxVideoDurationSecs)}
	}
	privacy := "PUBLIC_TO_EVERYONE"
	if !slices.Contains(cr.PrivacyLevels, privacy) {
		if len(cr.PrivacyLevels) == 0 {
			return res, platform.Errorf(platform.Rejected, "TikTok allows this account no way to post now")
		}
		privacy = cr.PrivacyLevels[0]
	}
	size := a.ChunkSize
	if v.Size < minChunk || size <= 0 || v.Size < size {
		size = v.Size
	}
	count := v.Size / size
	var init struct {
		PublishID string `json:"publish_id"`
		UploadURL string `json:"upload_url"`
	}
	err := a.call(ctx, c, http.MethodPost, "/v2/post/publish/video/init/", map[string]any{
		"post_info": map[string]any{"title": p.Parts[0], "privacy_level": privacy, "disable_comment": cr.CommentDisabled,
			"disable_duet": cr.DuetDisabled, "disable_stitch": cr.StitchDisabled},
		"source_info": map[string]any{"source": "FILE_UPLOAD", "video_size": v.Size, "chunk_size": size, "total_chunk_count": count},
	}, &init)
	if err != nil {
		return res, err
	}
	if init.PublishID == "" || init.UploadURL == "" {
		return res, platform.Errorf(platform.Transient, "TikTok started no upload")
	}
	if err := a.upload(ctx, init.UploadURL, v, size, count); err != nil {
		return res, err
	}
	id, err := a.wait(ctx, c, init.PublishID)
	if err != nil {
		return res, err
	}
	// A private post has no public ID; it is known by its publish ID.
	ref := platform.RemoteRef{ID: init.PublishID}
	if id != "" {
		ref.ID = id
		if c["username"] != "" {
			ref.URL = "https://www.tiktok.com/@" + c["username"] + "/video/" + id
		}
	}
	if onPart != nil {
		if err := onPart(ref); err != nil {
			return res, err
		}
	}
	res.Parts = append(res.Parts, ref)
	res.Permalink = ref.URL
	return res, nil
}

// upload puts the video in count parts of size bytes, the last taking the
// rest, each with its Content-Range.
func (a *Adapter) upload(ctx context.Context, uploadURL string, v platform.Media, size, count int64) error {
	rc, err := platform.OpenVideo(ctx, v)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	for i := int64(0); i < count; i++ {
		first := i * size
		n := size
		if i == count-1 {
			n = v.Size - first
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, io.LimitReader(rc, n))
		if err != nil {
			return &platform.Error{Kind: platform.Rejected, Code: "request", Err: err}
		}
		req.ContentLength = n
		req.Header.Set("Content-Type", v.Type)
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, first+n-1, v.Size))
		resp, err := platform.Do(a.Client, req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return platform.Classify(resp, raw)
		}
	}
	return nil
}

// wait checks the post until TikTok has published it, and returns its ID
// when TikTok gives one (it does not for a private post).
func (a *Adapter) wait(ctx context.Context, c platform.Credentials, publishID string) (string, error) {
	for {
		var st struct {
			Status     string   `json:"status"`
			FailReason string   `json:"fail_reason"`
			PostIDs    []string `json:"publicaly_available_post_id"` //nolint:misspell // TikTok's spelling
		}
		if err := a.call(ctx, c, http.MethodPost, "/v2/post/publish/status/fetch/", map[string]string{"publish_id": publishID}, &st); err != nil {
			return "", err
		}
		switch st.Status {
		case "PUBLISH_COMPLETE":
			if len(st.PostIDs) > 0 {
				return st.PostIDs[0], nil
			}
			return "", nil
		case "FAILED":
			return "", &platform.Error{Kind: platform.Rejected, Code: "video_rejected", Msg: "TikTok could not post the video: " + st.FailReason}
		}
		if err := a.Sleep(ctx, statusWait); err != nil {
			// The upload is done and TikTok may yet post it: whether it did
			// is for a person to check.
			return "", &platform.Error{Kind: platform.Uncertain, Code: "processing", Msg: "TikTok was still processing the post", Err: err}
		}
	}
}
