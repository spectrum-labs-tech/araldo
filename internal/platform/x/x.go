// SPDX-License-Identifier: AGPL-3.0-or-later

// Package x posts to X. A channel connects one of two ways:
//
//   - a sign-in (OAuth 2.0 with PKCE, ADR 0021) through the org's X app,
//     with scopes tweet.read, tweet.write, users.read, media.write and
//     offline.access. The access token lasts two hours and is renewed with
//     a refresh token, which X replaces on every renewal;
//   - the account's own OAuth 1.0a credentials, pasted from the X developer
//     portal (the app's API key and secret and the account's access token
//     and secret, from an app with read and write permission). They do not
//     expire.
//
// X has no idempotency key, so the adapter is not idempotent: an uncertain
// attempt waits for a person (ADR 0011).
package x

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // SHA-1 is what OAuth 1.0a's HMAC-SHA1 signature method is (RFC 5849)
	"encoding/base64"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Defaults.
const (
	DefaultAPI       = "https://api.x.com"
	DefaultAuthorize = "https://x.com/i/oauth2/authorize"
	scopes           = "tweet.read tweet.write users.read media.write offline.access"
)

// Adapter posts to X.
type Adapter struct {
	Client *http.Client
	// API and Authorize are X's endpoints; tests replace them.
	API, Authorize string
	// Now and Nonce make signatures; tests replace them.
	Now   func() time.Time
	Nonce func() string
}

// New returns an X adapter.
func New(client *http.Client) *Adapter {
	return &Adapter{Client: client, API: DefaultAPI, Authorize: DefaultAuthorize, Now: time.Now, Nonce: nonce}
}

func (a *Adapter) Provider() platform.Provider { return platform.X }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.X)
	return r
}

func (a *Adapter) Fields() []platform.Field {
	const where = "developer.x.com → your app (read and write) → Keys and tokens"
	return []platform.Field{
		{Name: "api_key", Label: "API key", Help: where + " → API Key and Secret"},
		{Name: "api_secret", Label: "API key secret", Secret: true},
		{Name: "access_token", Label: "Access token", Help: where + " → Access Token and Secret, for the account to post as"},
		{Name: "access_token_secret", Label: "Access token secret", Secret: true},
	}
}

func (a *Adapter) Idempotent() bool { return false }

func (a *Adapter) AuthorizeURL(app platform.App, redirectURI, state, challenge string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {app.ClientID}, "redirect_uri": {redirectURI}, "scope": {scopes},
		"state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	return a.Authorize + "?" + q.Encode()
}

// Exchange trades the code for a token pair and reads the account
// (https://docs.x.com/resources/fundamentals/authentication/oauth-2-0/user-access-token).
// The app authenticates with HTTP Basic, as X requires of confidential
// clients.
func (a *Adapter) Exchange(ctx context.Context, app platform.App, redirectURI, code, verifier string) ([]platform.Connection, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"code_verifier": {verifier}, "client_id": {app.ClientID}}
	t, err := platform.RequestToken(ctx, a.Client, strings.TrimRight(a.API, "/")+"/2/oauth2/token", form, &app)
	if err != nil {
		return nil, err
	}
	c := platform.Credentials{"access_token": t.AccessToken, "refresh_token": t.RefreshToken}
	acct, err := a.Verify(ctx, c)
	if err != nil {
		return nil, err
	}
	return []platform.Connection{{Account: acct, Credentials: c, ExpiresAt: t.Expiry(a.Now())}}, nil
}

// Refresh renews a signed-in channel's token. X replaces the refresh token
// each time and the old one stops working, so the caller must store the
// new pair before anything else refreshes.
func (a *Adapter) Refresh(ctx context.Context, app platform.App, c platform.Credentials) (platform.Credentials, *time.Time, error) {
	if c["refresh_token"] == "" {
		return nil, nil, platform.ErrNoRefresh
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c["refresh_token"]}, "client_id": {app.ClientID}}
	t, err := platform.RequestToken(ctx, a.Client, strings.TrimRight(a.API, "/")+"/2/oauth2/token", form, &app)
	if err != nil {
		return nil, nil, err
	}
	fresh := platform.Credentials{"access_token": t.AccessToken, "refresh_token": t.RefreshToken}
	if fresh["refresh_token"] == "" {
		fresh["refresh_token"] = c["refresh_token"]
	}
	return fresh, t.Expiry(a.Now()), nil
}

// credentials sign requests: a signed-in channel's bearer token, or the
// four OAuth 1.0a values of a channel connected with pasted keys.
type credentials struct{ bearer, key, secret, token, tokenSecret string }

func creds(c platform.Credentials) (credentials, error) {
	if c["api_key"] == "" && c["access_token"] != "" && c["access_token_secret"] == "" {
		return credentials{bearer: c["access_token"]}, nil
	}
	cr := credentials{key: c["api_key"], secret: c["api_secret"], token: c["access_token"], tokenSecret: c["access_token_secret"]}
	if cr.key == "" || cr.secret == "" || cr.token == "" || cr.tokenSecret == "" {
		return cr, platform.Errorf(platform.AuthRevoked, "the API key and secret and the access token and secret are all required")
	}
	return cr, nil
}

// call sends a signed JSON request. The JSON body is not part of the
// signature; query parameters are.
func (a *Adapter) call(ctx context.Context, cr credentials, method, path string, in, out any) error {
	u := strings.TrimRight(a.API, "/") + path
	return platform.JSON(ctx, a.Client, method, u, map[string]string{"Authorization": a.authorization(cr, method, u, nil)}, in, out)
}

func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	cr, err := creds(c)
	if err != nil {
		return platform.Account{}, err
	}
	var me struct {
		Data struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := a.call(ctx, cr, http.MethodGet, "/2/users/me", nil, &me); err != nil {
		return platform.Account{}, classify(err)
	}
	return platform.Account{ExternalID: me.Data.ID, Handle: "@" + me.Data.Username, DisplayName: me.Data.Name,
		URL: "https://x.com/" + me.Data.Username}, nil
}

type tweet struct {
	Text  string    `json:"text"`
	Reply *reply    `json:"reply,omitempty"`
	Media *tweetMed `json:"media,omitempty"`
}

type reply struct {
	InReplyTo string `json:"in_reply_to_tweet_id"`
}

type tweetMed struct {
	IDs []string `json:"media_ids"`
}

func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	cr, err := creds(c)
	if err != nil {
		return platform.Result{}, err
	}
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	replyTo := ""
	if len(p.Posted) > 0 {
		replyTo = p.Posted[len(p.Posted)-1].ID
	}
	for i := len(p.Posted); i < len(p.Parts); i++ {
		tw := tweet{Text: p.Parts[i]}
		if replyTo != "" {
			tw.Reply = &reply{InReplyTo: replyTo}
		}
		if i == 0 && len(p.Media) > 0 {
			ids, err := a.upload(ctx, cr, p.Media)
			if err != nil {
				return res, err
			}
			tw.Media = &tweetMed{IDs: ids}
		}
		var out struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := a.call(ctx, cr, http.MethodPost, "/2/tweets", tw, &out); err != nil {
			return res, classify(err)
		}
		ref := platform.RemoteRef{ID: out.Data.ID, URL: "https://x.com/i/web/status/" + out.Data.ID}
		if onPart != nil {
			if err := onPart(ref); err != nil {
				return res, err
			}
		}
		res.Parts = append(res.Parts, ref)
		replyTo = out.Data.ID
	}
	if len(res.Parts) > 0 {
		res.Permalink = res.Parts[0].URL
	}
	return res, nil
}

// upload sends each image (https://docs.x.com/x-api/media/upload-media) and
// sets its alt text. Media no post uses expires, so an upload before a
// failed post costs nothing.
func (a *Adapter) upload(ctx context.Context, cr credentials, media []platform.Media) ([]string, error) {
	ids := make([]string, 0, len(media))
	for i, m := range media {
		data, err := m.Read(ctx)
		if err != nil {
			return nil, err
		}
		body, contentType, err := platform.Multipart([][2]string{{"media_category", "tweet_image"}},
			[]platform.File{{Field: "media", Name: m.Filename(i), Type: m.Type, Data: data}})
		if err != nil {
			return nil, &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
		}
		u := strings.TrimRight(a.API, "/") + "/2/media/upload"
		var out struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		err = platform.Send(ctx, a.Client, http.MethodPost, u, map[string]string{"Authorization": a.authorization(cr, http.MethodPost, u, nil)},
			body, contentType, &out)
		if err != nil {
			return nil, classify(err)
		}
		if out.Data.ID == "" {
			return nil, platform.Errorf(platform.Transient, "the media upload returned no ID")
		}
		if m.Alt != "" {
			// Alt text is a courtesy the post does not depend on: a failure
			// here should not stop the post.
			_ = a.call(ctx, cr, http.MethodPost, "/2/media/metadata",
				map[string]any{"id": out.Data.ID, "metadata": map[string]any{"alt_text": map[string]string{"text": m.Alt}}}, nil)
		}
		ids = append(ids, out.Data.ID)
	}
	return ids, nil
}

// classify sharpens X's errors: a 403 is a refusal of this post (duplicate
// text, a policy), not of the credentials.
func classify(err error) error {
	pe, ok := err.(*platform.Error) //nolint:errorlint // platform.JSON returns *Error directly
	if !ok || pe.Kind != platform.Rejected {
		return err
	}
	if strings.HasPrefix(pe.Msg, "HTTP 403") && strings.Contains(strings.ToLower(pe.Msg), "duplicate") {
		pe.Code = "duplicate"
	}
	return pe
}

// authorization is the header for a request: the bearer token, or an OAuth
// 1.0a signature (RFC 5849, HMAC-SHA1). form holds a form-encoded body's
// parameters, which are signed; JSON and multipart bodies are not.
func (a *Adapter) authorization(cr credentials, method, rawURL string, form url.Values) string {
	if cr.bearer != "" {
		return "Bearer " + cr.bearer
	}
	oauth := map[string]string{
		"oauth_consumer_key":     cr.key,
		"oauth_nonce":            a.Nonce(),
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        strconv.FormatInt(a.Now().Unix(), 10),
		"oauth_token":            cr.token,
		"oauth_version":          "1.0",
	}
	oauth["oauth_signature"] = signature(method, rawURL, oauth, form, cr.secret, cr.tokenSecret)
	parts := make([]string, 0, len(oauth))
	for k, v := range oauth {
		parts = append(parts, encode(k)+`="`+encode(v)+`"`)
	}
	sort.Strings(parts)
	return "OAuth " + strings.Join(parts, ", ")
}

// signature is the HMAC-SHA1 signature of a request: its method, URL
// without the query, and every parameter (OAuth, query and form), sorted.
func signature(method, rawURL string, oauth map[string]string, form url.Values, consumerSecret, tokenSecret string) string {
	u, _ := url.Parse(rawURL)
	var params []string
	for k, v := range oauth {
		params = append(params, encode(k)+"="+encode(v))
	}
	for _, vals := range []url.Values{u.Query(), form} {
		for k, vs := range vals {
			for _, v := range vs {
				params = append(params, encode(k)+"="+encode(v))
			}
		}
	}
	sort.Strings(params)
	base := url.URL{Scheme: strings.ToLower(u.Scheme), Host: strings.ToLower(u.Host), Path: u.Path}
	text := strings.ToUpper(method) + "&" + encode(base.String()) + "&" + encode(strings.Join(params, "&"))
	mac := hmac.New(sha1.New, []byte(encode(consumerSecret)+"&"+encode(tokenSecret)))
	mac.Write([]byte(text))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// encode is RFC 3986 percent-encoding, as OAuth 1.0a requires: every byte
// but the unreserved characters.
func encode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(strconv.FormatUint(uint64(c)>>4, 16)+strconv.FormatUint(uint64(c)&15, 16)))
		}
	}
	return b.String()
}

func nonce() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
