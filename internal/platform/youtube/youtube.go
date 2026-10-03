// SPDX-License-Identifier: AGPL-3.0-or-later

// Package youtube uploads videos to a YouTube channel through the YouTube
// Data API v3 (ADR 0027). A channel connects with a sign-in through the
// org's Google app: an OAuth client of type "Web application" in a Google
// Cloud project with the YouTube Data API v3 enabled, asking for
// youtube.upload and youtube.readonly. Access tokens last an hour and are
// renewed with the refresh token. While the app's consent screen is in
// testing, Google expires refresh tokens after seven days: publish the app
// to keep channels connected.
//
// A post is one video: the text's first line is its title (YouTube needs
// one, of up to 100 characters) and the rest its description. Each upload
// costs 1,600 units of the project's daily quota of 10,000. YouTube has no
// idempotency key, so the adapter is not idempotent.
package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Defaults.
const (
	DefaultAPI    = "https://www.googleapis.com"
	DefaultAuth   = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultToken  = "https://oauth2.googleapis.com/token" //nolint:gosec // G101: an endpoint, not a credential
	scopes        = "https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.readonly"
	maxTitle      = 100
	maxDesc       = 5000
	statsPageSize = 50
)

// Adapter uploads to YouTube.
type Adapter struct {
	Client *http.Client
	// API, Auth and Token are Google's endpoints; tests replace them.
	API, Auth, Token string
	// Now is the clock for token expiry; tests replace it.
	Now func() time.Time
}

// New returns a YouTube adapter.
func New(client *http.Client) *Adapter {
	return &Adapter{Client: client, API: DefaultAPI, Auth: DefaultAuth, Token: DefaultToken, Now: time.Now}
}

func (a *Adapter) Provider() platform.Provider { return platform.YouTube }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.YouTube)
	return r
}

// Fields are none: a channel connects only by signing in. Its videos are
// public; a "privacy" credential of unlisted or private changes that.
func (a *Adapter) Fields() []platform.Field { return nil }

func (a *Adapter) Idempotent() bool { return false }

func (a *Adapter) AuthorizeURL(app platform.App, redirectURI, state, challenge string) string {
	q := url.Values{"client_id": {app.ClientID}, "redirect_uri": {redirectURI}, "response_type": {"code"}, "scope": {scopes},
		"access_type": {"offline"}, "prompt": {"consent"}, "include_granted_scopes": {"true"}, "state": {state},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	return a.Auth + "?" + q.Encode()
}

// Exchange trades the code for tokens and offers each YouTube channel the
// account manages (https://developers.google.com/identity/protocols/oauth2/web-server).
func (a *Adapter) Exchange(ctx context.Context, app platform.App, redirectURI, code, verifier string) ([]platform.Connection, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {verifier},
		"client_id": {app.ClientID}, "client_secret": {app.ClientSecret}}
	t, err := platform.RequestToken(ctx, a.Client, a.Token, form, nil)
	if err != nil {
		return nil, err
	}
	if t.RefreshToken == "" {
		return nil, platform.Errorf(platform.Rejected, "Google sent no refresh token: remove Araldo's access in your Google account's security settings, then connect again")
	}
	c := platform.Credentials{"access_token": t.AccessToken, "refresh_token": t.RefreshToken}
	chans, err := a.channels(ctx, c)
	if err != nil {
		return nil, err
	}
	if len(chans) == 0 {
		return nil, platform.Errorf(platform.Rejected, "this Google account has no YouTube channel: create one at youtube.com, then connect again")
	}
	out := make([]platform.Connection, 0, len(chans))
	for _, ch := range chans {
		creds := platform.Credentials{"access_token": t.AccessToken, "refresh_token": t.RefreshToken, "channel_id": ch.ID, "privacy": "public"}
		out = append(out, platform.Connection{Account: ch.account(), Credentials: creds, ExpiresAt: t.Expiry(a.Now())})
	}
	return out, nil
}

// Refresh renews the access token; the refresh token stays the same.
func (a *Adapter) Refresh(ctx context.Context, app platform.App, c platform.Credentials) (platform.Credentials, *time.Time, error) {
	if c["refresh_token"] == "" {
		return nil, nil, platform.ErrNoRefresh
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c["refresh_token"]}, "client_id": {app.ClientID},
		"client_secret": {app.ClientSecret}}
	t, err := platform.RequestToken(ctx, a.Client, a.Token, form, nil)
	if err != nil {
		return nil, nil, err
	}
	fresh := platform.Credentials{}
	for k, v := range c {
		fresh[k] = v
	}
	fresh["access_token"] = t.AccessToken
	if t.RefreshToken != "" {
		fresh["refresh_token"] = t.RefreshToken
	}
	return fresh, t.Expiry(a.Now()), nil
}

func headers(c platform.Credentials) (map[string]string, error) {
	tok := strings.TrimSpace(c["access_token"])
	if tok == "" {
		return nil, platform.Errorf(platform.AuthRevoked, "an access token is required: sign in again")
	}
	return map[string]string{"Authorization": "Bearer " + tok}, nil
}

type channel struct {
	ID      string `json:"id"`
	Snippet struct {
		Title     string `json:"title"`
		CustomURL string `json:"customUrl"`
	} `json:"snippet"`
}

func (ch channel) account() platform.Account {
	acct := platform.Account{ExternalID: ch.ID, Handle: ch.Snippet.CustomURL, DisplayName: ch.Snippet.Title + " (YouTube)",
		URL: "https://www.youtube.com/channel/" + ch.ID}
	if acct.Handle == "" {
		acct.Handle = ch.Snippet.Title
	}
	return acct
}

func (a *Adapter) channels(ctx context.Context, c platform.Credentials) ([]channel, error) {
	h, err := headers(c)
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []channel `json:"items"`
	}
	if err := platform.JSON(ctx, a.Client, http.MethodGet, a.API+"/youtube/v3/channels?part=snippet&mine=true", h, nil, &out); err != nil {
		return nil, quota(err)
	}
	return out.Items, nil
}

// Verify checks the token still reaches the channel.
func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	chans, err := a.channels(ctx, c)
	if err != nil {
		return platform.Account{}, err
	}
	for _, ch := range chans {
		if ch.ID == c["channel_id"] {
			return ch.account(), nil
		}
	}
	return platform.Account{}, platform.Errorf(platform.AuthRevoked, "the Google account no longer manages this YouTube channel")
}

type snippet struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	CategoryID  string `json:"categoryId"`
}

type status struct {
	PrivacyStatus           string `json:"privacyStatus"`
	SelfDeclaredMadeForKids bool   `json:"selfDeclaredMadeForKids"`
}

// Publish uploads the post's video, streamed, with a resumable upload: a
// request describing it, which answers with where to send it, then the
// video itself (https://developers.google.com/youtube/v3/guides/using_resumable_upload_protocol).
func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	if len(p.Posted) >= len(p.Parts) {
		return res, nil
	}
	h, err := headers(c)
	if err != nil {
		return res, err
	}
	if len(p.Media) != 1 || !p.Media[0].IsVideo() {
		return res, &platform.Error{Kind: platform.Rejected, Code: "media_required", Msg: "a YouTube post is one video"}
	}
	v := p.Media[0]
	title, description := Split(p.Parts[0], v.Alt)
	privacy := c["privacy"]
	if privacy != "unlisted" && privacy != "private" {
		privacy = "public"
	}
	meta := map[string]any{"snippet": snippet{Title: title, Description: description, CategoryID: "22"},
		"status": status{PrivacyStatus: privacy}}
	body, _ := json.Marshal(meta)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.API+"/upload/youtube/v3/videos?uploadType=resumable&part=snippet,status", strings.NewReader(string(body)))
	if err != nil {
		return res, &platform.Error{Kind: platform.Rejected, Code: "request", Err: err}
	}
	req.Header.Set("Authorization", h["Authorization"])
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(v.Size, 10))
	req.Header.Set("X-Upload-Content-Type", v.Type)
	resp, err := platform.Do(a.Client, req)
	if err != nil {
		return res, err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return res, quota(platform.Classify(resp, raw))
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return res, platform.Errorf(platform.Transient, "YouTube started no upload")
	}
	rc, err := platform.OpenVideo(ctx, v)
	if err != nil {
		return res, err
	}
	defer func() { _ = rc.Close() }()
	var out struct {
		ID string `json:"id"`
	}
	if err := platform.SendStream(ctx, a.Client, http.MethodPut, location, h, rc, v.Size, v.Type, &out); err != nil {
		return res, quota(err)
	}
	if out.ID == "" {
		return res, platform.Errorf(platform.Uncertain, "YouTube took the video but did not say which it was")
	}
	ref := platform.RemoteRef{ID: out.ID, URL: "https://www.youtube.com/watch?v=" + out.ID}
	if onPart != nil {
		if err := onPart(ref); err != nil {
			return res, err
		}
	}
	res.Parts = append(res.Parts, ref)
	res.Permalink = ref.URL
	return res, nil
}

// Engagement reads each video's likes, comments (as replies) and views,
// fifty at a time (https://developers.google.com/youtube/v3/docs/videos/list).
func (a *Adapter) Engagement(ctx context.Context, c platform.Credentials, refs []platform.RemoteRef) (map[string]platform.Counts, error) {
	h, err := headers(c)
	if err != nil {
		return nil, err
	}
	out := map[string]platform.Counts{}
	for start := 0; start < len(refs); start += statsPageSize {
		var ids []string
		for _, r := range refs[start:min(start+statsPageSize, len(refs))] {
			ids = append(ids, r.ID)
		}
		var page struct {
			Items []struct {
				ID         string `json:"id"`
				Statistics struct {
					Views    string `json:"viewCount"`
					Likes    string `json:"likeCount"`
					Comments string `json:"commentCount"`
				} `json:"statistics"`
			} `json:"items"`
		}
		q := url.Values{"part": {"statistics"}, "id": {strings.Join(ids, ",")}}
		if err := platform.JSON(ctx, a.Client, http.MethodGet, a.API+"/youtube/v3/videos?"+q.Encode(), h, nil, &page); err != nil {
			return nil, quota(err)
		}
		for _, it := range page.Items {
			views := count(it.Statistics.Views)
			out[it.ID] = platform.Counts{Likes: count(it.Statistics.Likes), Replies: count(it.Statistics.Comments), Views: &views}
		}
	}
	return out, nil
}

// Split makes a video's title and description from the post's text: the
// first line, cut to 100 characters, is the title, and the rest the
// description. Without text, the alt text names it.
func Split(text, alt string) (title, description string) {
	text = strings.TrimSpace(text)
	first, rest, _ := strings.Cut(text, "\n")
	title, description = strings.TrimSpace(first), strings.TrimSpace(rest)
	if title == "" {
		title = strings.TrimSpace(alt)
	}
	if title == "" {
		title = "Video"
	}
	// YouTube refuses angle brackets in titles.
	title = strings.NewReplacer("<", "‹", ">", "›").Replace(title)
	if utf8.RuneCountInString(title) > maxTitle {
		r := []rune(title)
		title = string(r[:maxTitle-1]) + "…"
		description = strings.TrimSpace(text)
	}
	if len(description) > maxDesc {
		description = description[:maxDesc]
		for !utf8.ValidString(description) {
			description = description[:len(description)-1]
		}
	}
	return title, description
}

// quota explains the error YouTube answers when the project's daily quota
// is spent: a 403 that lifts at midnight Pacific time.
func quota(err error) error {
	var pe *platform.Error
	if errors.As(err, &pe) && strings.Contains(pe.Msg, "quotaExceeded") {
		pe.Kind, pe.Code = platform.RateLimited, "quota_exceeded"
		pe.Msg = "the Google project's YouTube quota for today is spent; it resets at midnight Pacific time. " + pe.Msg
		if pe.RetryAfter == 0 {
			pe.RetryAfter = time.Hour
		}
	}
	return err
}

// count reads one of YouTube's counts, which it sends as strings.
func count(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
