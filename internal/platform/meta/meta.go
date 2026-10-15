// SPDX-License-Identifier: AGPL-3.0-or-later

// Package meta is what Facebook Pages and Instagram share: Facebook Login
// through the org's Meta app (ADR 0021), the Graph API, and how its errors
// read.
package meta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Defaults. Meta supports a Graph API version for about two years.
const (
	DefaultGraph  = "https://graph.facebook.com"
	DefaultDialog = "https://www.facebook.com"
	// DefaultVideoGraph takes video uploads.
	DefaultVideoGraph = "https://graph-video.facebook.com"
	Version           = "v23.0"
)

// Client calls the Graph API.
type Client struct {
	HTTP *http.Client
	// Graph, VideoGraph and Dialog are Meta's endpoints; tests replace
	// them.
	Graph, VideoGraph, Dialog string
	// Poll is how long to wait between checks on a container Instagram is
	// still processing, and MaxPolls how many checks to make; tests
	// shorten them.
	Poll     time.Duration
	MaxPolls int
}

// MaxPolls bounds the checks on a container still processing: at the
// 2-second Poll, five minutes, the longest Meta suggests waiting
// (https://developers.facebook.com/docs/instagram-platform/content-publishing#troubleshooting).
const MaxPolls = 150

// New returns a client.
func New(client *http.Client) *Client {
	return &Client{HTTP: client, Graph: DefaultGraph, VideoGraph: DefaultVideoGraph, Dialog: DefaultDialog, Poll: 2 * time.Second, MaxPolls: MaxPolls}
}

func (c *Client) url(path string) string {
	return strings.TrimRight(c.Graph, "/") + "/" + Version + "/" + strings.TrimLeft(path, "/")
}

// The access token goes in an Authorization header, which the Graph API
// reads like the access_token parameter
// (https://developers.facebook.com/docs/graph-api/overview#access-tokens),
// so it never sits in a URL that a proxy or an error message might show.
// Callers pass it as the access_token parameter or field, as Meta
// documents each call; the client moves it.

// token takes the access token out of params, returning the headers that
// carry it instead.
func token(params url.Values) (url.Values, map[string]string) {
	tok := params.Get("access_token")
	if tok == "" {
		return params, nil
	}
	rest := url.Values{}
	for k, v := range params {
		if k != "access_token" {
			rest[k] = v
		}
	}
	return rest, map[string]string{"Authorization": "Bearer " + tok}
}

// fieldToken is token for multipart fields.
func fieldToken(fields [][2]string) ([][2]string, map[string]string) {
	var rest [][2]string
	var h map[string]string
	for _, f := range fields {
		if f[0] == "access_token" {
			h = map[string]string{"Authorization": "Bearer " + f[1]}
			continue
		}
		rest = append(rest, f)
	}
	return rest, h
}

// Get reads path with params (which include the access token).
func (c *Client) Get(ctx context.Context, path string, params url.Values, out any) error {
	params, h := token(params)
	u := c.url(path)
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	return Classify(platform.JSON(ctx, c.HTTP, http.MethodGet, u, h, nil, out))
}

// Post sends params form-encoded to path.
func (c *Client) Post(ctx context.Context, path string, params url.Values, out any) error {
	params, h := token(params)
	return Classify(platform.Send(ctx, c.HTTP, http.MethodPost, c.url(path), h, []byte(params.Encode()), "application/x-www-form-urlencoded", out))
}

// PostMultipart sends fields and a file to path.
func (c *Client) PostMultipart(ctx context.Context, path string, fields [][2]string, file platform.File, out any) error {
	fields, h := fieldToken(fields)
	body, contentType, err := platform.Multipart(fields, []platform.File{file})
	if err != nil {
		return &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
	}
	return Classify(platform.Send(ctx, c.HTTP, http.MethodPost, c.url(path), h, body, contentType, out))
}

// PostVideo sends fields and a video, streamed, to path on the video host
// (https://developers.facebook.com/docs/video-api/guides/publishing).
func (c *Client) PostVideo(ctx context.Context, path string, fields [][2]string, file platform.StreamFile, out any) error {
	fields, h := fieldToken(fields)
	body, contentType, length, err := platform.MultipartStream(fields, file)
	if err != nil {
		return &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
	}
	u := strings.TrimRight(c.VideoGraph, "/") + "/" + Version + "/" + strings.TrimLeft(path, "/")
	return Classify(platform.SendStream(ctx, c.HTTP, http.MethodPost, u, h, body, length, contentType, out))
}

// AuthorizeURL is Facebook Login's dialog for scopes.
func (c *Client) AuthorizeURL(app platform.App, redirectURI, state string, scopes ...string) string {
	q := url.Values{"client_id": {app.ClientID}, "redirect_uri": {redirectURI}, "state": {state}, "response_type": {"code"},
		"scope": {strings.Join(scopes, ",")}}
	return strings.TrimRight(c.Dialog, "/") + "/" + Version + "/dialog/oauth?" + q.Encode()
}

// UserToken trades a code for a long-lived (60-day) user token
// (https://developers.facebook.com/docs/facebook-login/guides/access-tokens/get-long-lived).
// Page tokens derived from it do not expire. These two calls take the app
// secret and the codes as query parameters, the only way Meta documents;
// errors are scrubbed of them (platform.Scrub).
func (c *Client) UserToken(ctx context.Context, app platform.App, redirectURI, code string) (string, error) {
	var short, long struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.Get(ctx, "oauth/access_token", url.Values{"client_id": {app.ClientID}, "client_secret": {app.ClientSecret},
		"redirect_uri": {redirectURI}, "code": {code}}, &short); err != nil {
		return "", err
	}
	if err := c.Get(ctx, "oauth/access_token", url.Values{"grant_type": {"fb_exchange_token"}, "client_id": {app.ClientID},
		"client_secret": {app.ClientSecret}, "fb_exchange_token": {short.AccessToken}}, &long); err != nil {
		return "", err
	}
	return long.AccessToken, nil
}

// Page is a Facebook Page the member manages, with its token and any
// Instagram professional account linked to it.
type Page struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccessToken string `json:"access_token"`
	Instagram   *struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"instagram_business_account"`
}

// Pages lists the Pages a user token manages.
func (c *Client) Pages(ctx context.Context, userToken string) ([]Page, error) {
	var out struct {
		Data []Page `json:"data"`
	}
	err := c.Get(ctx, "me/accounts", url.Values{"fields": {"id,name,access_token,instagram_business_account{id,username,name}"},
		"limit": {"100"}, "access_token": {userToken}}, &out)
	return out.Data, err
}

// Classify reads Graph API errors by their code
// (https://developers.facebook.com/docs/graph-api/guides/error-handling):
// 190 is an expired or revoked token; 4, 17, 32, 613 and 80000-80014 are
// rate limits. Threads reports errors the same way.
func Classify(err error) error {
	pe, ok := err.(*platform.Error) //nolint:errorlint // platform helpers return *Error directly
	if !ok || pe.Kind == platform.Uncertain || pe.Kind == platform.Transient {
		return err
	}
	e := graphError(pe)
	switch code := e.Code; {
	case code == 190 || code == 102 || code == 10 || code == 200 && strings.Contains(strings.ToLower(e.Message), "permission"):
		pe.Kind, pe.Code = platform.AuthRevoked, "token_invalid"
	case code == 4 || code == 17 || code == 32 || code == 613 || code >= 80000 && code <= 80014:
		pe.Kind, pe.Code = platform.RateLimited, "rate_limited"
		if pe.RetryAfter == 0 {
			pe.RetryAfter = 15 * time.Minute
		}
	}
	return pe
}

type graphErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// graphError reads the error object Meta returns, from the response body
// the platform error kept, or from its message when there is no body.
func graphError(pe *platform.Error) graphErr {
	var body struct {
		Error graphErr `json:"error"`
	}
	if len(pe.Body) > 0 && json.Unmarshal(pe.Body, &body) == nil {
		return body.Error
	}
	return graphErr{Code: errorCode(pe.Msg), Message: pe.Msg}
}

// errorCode finds `"code":N` in an error body; 0 if there is none.
func errorCode(msg string) int {
	i := strings.Index(msg, `"code":`)
	if i < 0 {
		return 0
	}
	rest := msg[i+len(`"code":`):]
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(rest)
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}
