// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// ErrNoRefresh is what a Refresher returns for a token the platform gives
// no way to renew (a LinkedIn token without a refresh token): the member
// must sign in again before it expires. It is not AuthRevoked, because the
// token still works until then.
var ErrNoRefresh = errors.New("platform: the token cannot be renewed; sign in again before it expires")

// Token is an OAuth 2.0 token response (RFC 6749 section 5.1).
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// ExpiresIn is the access token's lifetime in seconds; zero if the
	// platform did not say.
	ExpiresIn int64  `json:"expires_in"`
	Scope     string `json:"scope"`
}

// Expiry is when the access token expires, from now, or nil if it does not
// say.
func (t Token) Expiry(now time.Time) *time.Time {
	if t.ExpiresIn <= 0 {
		return nil
	}
	at := now.Add(time.Duration(t.ExpiresIn) * time.Second)
	return &at
}

// RequestToken posts form to an OAuth 2.0 token endpoint. With basic, the
// app authenticates with HTTP Basic (RFC 6749 section 2.3.1) instead of
// sending its secret in the form. A refresh the platform refuses is
// AuthRevoked: the member must sign in again.
func RequestToken(ctx context.Context, client *http.Client, endpoint string, form url.Values, basic *App) (Token, error) {
	var headers map[string]string
	if basic != nil {
		cred := url.QueryEscape(basic.ClientID) + ":" + url.QueryEscape(basic.ClientSecret)
		headers = map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(cred))}
	}
	var t Token
	err := Send(ctx, client, http.MethodPost, endpoint, headers, []byte(form.Encode()), "application/x-www-form-urlencoded", &t)
	var pe *Error
	if errors.As(err, &pe) && pe.Kind == Rejected && form.Get("grant_type") == "refresh_token" {
		pe.Kind, pe.Code = AuthRevoked, "refresh_refused"
	}
	if err != nil {
		return Token{}, err
	}
	if t.AccessToken == "" {
		return Token{}, &Error{Kind: Rejected, Code: "token_missing", Msg: "the token endpoint answered without an access token"}
	}
	return t, nil
}
