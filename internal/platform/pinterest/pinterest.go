// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pinterest pins images to a Pinterest board through the API v5
// (https://developers.pinterest.com/docs/api/v5/). The org's Pinterest
// app, made at developers.pinterest.com/apps, asks for boards:read,
// pins:read, pins:write and user_accounts:read. A channel is one board,
// and connects one of two ways:
//
//   - a sign-in through that app, which offers each of the member's
//     boards (ADR 0021);
//   - an access token from the app's page, with the board's ID.
//
// Tokens last 30 days and are renewed with the refresh token, which lasts
// a year.
//
// A pin is one image, which Pinterest requires. The first link in the
// text becomes the pin's destination, where its clicks go; a short first
// line, followed by more text, becomes its title. Pinterest has no
// idempotency key, so the adapter is not idempotent.
package pinterest

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Defaults.
const (
	DefaultAPI = "https://api.pinterest.com/v5"
	// DefaultAuth is where members sign in.
	DefaultAuth = "https://www.pinterest.com/oauth/"
	scopes      = "boards:read,pins:read,pins:write,user_accounts:read"
	maxTitle    = 100
	maxAlt      = 500
	boardsPage  = 250
)

// Adapter pins to Pinterest.
type Adapter struct {
	Client *http.Client
	// API and Auth are Pinterest's endpoints; tests replace them.
	API, Auth string
	// Now is the clock for token expiry; tests replace it.
	Now func() time.Time
}

// New returns a Pinterest adapter.
func New(client *http.Client) *Adapter {
	return &Adapter{Client: client, API: DefaultAPI, Auth: DefaultAuth, Now: time.Now}
}

func (a *Adapter) Provider() platform.Provider { return platform.Pinterest }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Pinterest)
	return r
}

func (a *Adapter) Fields() []platform.Field {
	return []platform.Field{
		{Name: "access_token", Label: "Access token", Secret: true,
			Help: "developers.pinterest.com/apps → your app → generate a token with boards:read, pins:read, pins:write and user_accounts:read"},
		{Name: "board_id", Label: "Board ID", Help: "The number at the end of the board's address when you edit it, or from GET /v5/boards"},
	}
}

func (a *Adapter) Idempotent() bool { return false }

func (a *Adapter) AuthorizeURL(app platform.App, redirectURI, state, _ string) string {
	q := url.Values{"client_id": {app.ClientID}, "redirect_uri": {redirectURI}, "response_type": {"code"}, "scope": {scopes}, "state": {state}}
	return a.Auth + "?" + q.Encode()
}

// Exchange trades the code for a token (with the app's credentials as
// HTTP Basic) and offers each of the member's boards
// (https://developers.pinterest.com/docs/getting-started/set-up-authentication-and-authorization/).
func (a *Adapter) Exchange(ctx context.Context, app platform.App, redirectURI, code, _ string) ([]platform.Connection, error) {
	t, err := platform.RequestToken(ctx, a.Client, a.API+"/oauth/token",
		url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}}, &app)
	if err != nil {
		return nil, err
	}
	c := platform.Credentials{"access_token": t.AccessToken, "refresh_token": t.RefreshToken}
	user, err := a.user(ctx, c)
	if err != nil {
		return nil, err
	}
	boards, err := a.boards(ctx, c)
	if err != nil {
		return nil, err
	}
	expires := t.Expiry(a.Now())
	out := make([]platform.Connection, 0, len(boards))
	for _, b := range boards {
		creds := platform.Credentials{"access_token": t.AccessToken, "refresh_token": t.RefreshToken, "board_id": b.ID}
		out = append(out, platform.Connection{Account: account(user, b), Credentials: creds, ExpiresAt: expires})
	}
	if len(out) == 0 {
		return nil, platform.Errorf(platform.Rejected, "the Pinterest account %s has no boards: make one, then connect again", user.Username)
	}
	return out, nil
}

// Refresh renews the token. Pinterest may send a new refresh token; if not,
// the old one keeps working until it expires.
func (a *Adapter) Refresh(ctx context.Context, app platform.App, c platform.Credentials) (platform.Credentials, *time.Time, error) {
	if c["refresh_token"] == "" {
		return nil, nil, platform.ErrNoRefresh
	}
	t, err := platform.RequestToken(ctx, a.Client, a.API+"/oauth/token",
		url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c["refresh_token"]}}, &app)
	if err != nil {
		return nil, nil, err
	}
	fresh := platform.Credentials{"access_token": t.AccessToken, "refresh_token": t.RefreshToken, "board_id": c["board_id"]}
	if fresh["refresh_token"] == "" {
		fresh["refresh_token"] = c["refresh_token"]
	}
	return fresh, t.Expiry(a.Now()), nil
}

func headers(c platform.Credentials) (map[string]string, error) {
	tok := strings.TrimSpace(c["access_token"])
	if tok == "" {
		return nil, platform.Errorf(platform.AuthRevoked, "an access token is required")
	}
	return map[string]string{"Authorization": "Bearer " + tok}, nil
}

type userAccount struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type board struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func account(u userAccount, b board) platform.Account {
	owner := u.ID
	if owner == "" {
		owner = u.Username
	}
	return platform.Account{ExternalID: owner + ":" + b.ID, Handle: u.Username, DisplayName: b.Name + " (Pinterest)",
		URL: "https://www.pinterest.com/" + url.PathEscape(u.Username) + "/"}
}

func (a *Adapter) user(ctx context.Context, c platform.Credentials) (userAccount, error) {
	h, err := headers(c)
	if err != nil {
		return userAccount{}, err
	}
	var u userAccount
	if err := platform.JSON(ctx, a.Client, http.MethodGet, a.API+"/user_account", h, nil, &u); err != nil {
		return u, err
	}
	if u.Username == "" {
		return u, platform.Errorf(platform.AuthRevoked, "the token does not identify an account: grant user_accounts:read")
	}
	return u, nil
}

// boards lists every board the member owns or collaborates on.
func (a *Adapter) boards(ctx context.Context, c platform.Credentials) ([]board, error) {
	h, err := headers(c)
	if err != nil {
		return nil, err
	}
	var out []board
	for bookmark := ""; ; {
		q := url.Values{"page_size": {"250"}}
		if bookmark != "" {
			q.Set("bookmark", bookmark)
		}
		var page struct {
			Items    []board `json:"items"`
			Bookmark string  `json:"bookmark"`
		}
		if err := platform.JSON(ctx, a.Client, http.MethodGet, a.API+"/boards?"+q.Encode(), h, nil, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.Bookmark == "" || len(page.Items) < boardsPage {
			return out, nil
		}
		bookmark = page.Bookmark
	}
}

// Verify checks the token and that the board is one it can pin to.
func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	u, err := a.user(ctx, c)
	if err != nil {
		return platform.Account{}, err
	}
	id := strings.TrimSpace(c["board_id"])
	if id == "" {
		return platform.Account{}, &platform.Error{Kind: platform.Rejected, Code: "board_missing", Msg: "a board ID is required"}
	}
	h, _ := headers(c)
	var b board
	if err := platform.JSON(ctx, a.Client, http.MethodGet, a.API+"/boards/"+url.PathEscape(id), h, nil, &b); err != nil {
		return platform.Account{}, err
	}
	return account(u, b), nil
}

type mediaSource struct {
	SourceType  string `json:"source_type"`
	ContentType string `json:"content_type,omitempty"`
	Data        string `json:"data,omitempty"`
}

type pin struct {
	BoardID     string      `json:"board_id"`
	Title       string      `json:"title,omitempty"`
	Description string      `json:"description,omitempty"`
	Link        string      `json:"link,omitempty"`
	AltText     string      `json:"alt_text,omitempty"`
	MediaSource mediaSource `json:"media_source"`
}

// Publish creates one pin from the text and the first image
// (https://developers.pinterest.com/docs/api/v5/pins-create).
func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	if len(p.Posted) >= len(p.Parts) {
		return res, nil
	}
	h, err := headers(c)
	if err != nil {
		return res, err
	}
	if len(p.Media) == 0 {
		return res, &platform.Error{Kind: platform.Rejected, Code: "media_required", Msg: "a pin needs an image"}
	}
	m := p.Media[0]
	data, err := m.Read(ctx)
	if err != nil {
		return res, err
	}
	title, description := Split(p.Parts[0])
	body := pin{BoardID: c["board_id"], Title: title, Description: description, Link: FirstLink(p.Parts[0]), AltText: truncate(m.Alt, maxAlt),
		MediaSource: mediaSource{SourceType: "image_base64", ContentType: m.Type, Data: base64.StdEncoding.EncodeToString(data)}}
	var out struct {
		ID string `json:"id"`
	}
	if err := platform.JSON(ctx, a.Client, http.MethodPost, a.API+"/pins", h, body, &out); err != nil {
		return res, err
	}
	if out.ID == "" {
		return res, platform.Errorf(platform.Uncertain, "Pinterest accepted the pin but did not say which it was")
	}
	ref := platform.RemoteRef{ID: out.ID, URL: "https://www.pinterest.com/pin/" + out.ID + "/"}
	if onPart != nil {
		if err := onPart(ref); err != nil {
			return res, err
		}
	}
	res.Parts = append(res.Parts, ref)
	res.Permalink = ref.URL
	return res, nil
}

// Split makes a pin's title and description from text: a first line of up
// to 100 characters, followed by more text, is the title; otherwise there
// is no title and the text is the description.
func Split(text string) (title, description string) {
	text = strings.TrimSpace(text)
	first, rest, ok := strings.Cut(text, "\n")
	first, rest = strings.TrimSpace(first), strings.TrimSpace(rest)
	if !ok || rest == "" || first == "" || utf8.RuneCountInString(first) > maxTitle {
		return "", text
	}
	return first, rest
}

// FirstLink is the first link in text, or "".
func FirstLink(text string) string { return platform.LinkRE.FindString(text) }

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
