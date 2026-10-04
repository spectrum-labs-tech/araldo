// SPDX-License-Identifier: AGPL-3.0-or-later

package meta

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		body string
		want platform.Kind
	}{
		{`{"error":{"message":"Error validating access token","type":"OAuthException","code":190}}`, platform.AuthRevoked},
		{`{"error":{"message":"Application request limit reached","code":4}}`, platform.RateLimited},
		{`{"error":{"message":"(#32) Page request limit reached","code":32}}`, platform.RateLimited},
		{`{"error":{"message":"There have been too many calls","code":80001}}`, platform.RateLimited},
		{`{"error":{"message":"Invalid parameter","code":100}}`, platform.Rejected},
	}
	for _, tt := range tests {
		err := Classify(&platform.Error{Kind: platform.Rejected, Code: "rejected", Msg: "HTTP 400: " + tt.body})
		if platform.KindOf(err) != tt.want {
			t.Errorf("%s: %s, want %s", tt.body, platform.KindOf(err), tt.want)
		}
	}
	if err := Classify(nil); err != nil {
		t.Fatalf("Classify(nil) = %v", err)
	}
	transient := &platform.Error{Kind: platform.Transient, Msg: `"code":190`}
	if platform.KindOf(Classify(transient)) != platform.Transient {
		t.Fatal("a transport failure should stay as it is")
	}
}

func TestLogin(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/"+Version+"/oauth/access_token" && q.Get("code") == "c" && q.Get("client_secret") == "s":
			_, _ = w.Write([]byte(`{"access_token":"short"}`))
		case r.URL.Path == "/"+Version+"/oauth/access_token" && q.Get("grant_type") == "fb_exchange_token" && q.Get("fb_exchange_token") == "short":
			_, _ = w.Write([]byte(`{"access_token":"long","expires_in":5183944}`))
		case r.URL.Path == "/"+Version+"/me/accounts" && q.Get("access_token") == "long":
			_, _ = w.Write([]byte(`{"data":[{"id":"p1","name":"Open B00KS","access_token":"pt1","instagram_business_account":{"id":"ig1","username":"openb00ks"}},` +
				`{"id":"p2","name":"Araldo","access_token":"pt2"}]}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"bad","code":100}}`))
		}
	}))
	defer srv.Close()
	c := New(srv.Client())
	c.Graph = srv.URL
	app := platform.App{ClientID: "id", ClientSecret: "s"}
	u, _ := url.Parse(c.AuthorizeURL(app, "https://araldo.test/cb", "st", "a", "b"))
	if !strings.HasSuffix(u.Path, "/"+Version+"/dialog/oauth") || u.Query().Get("scope") != "a,b" || u.Query().Get("state") != "st" {
		t.Fatalf("authorize URL %s", u)
	}
	tok, err := c.UserToken(t.Context(), app, "https://araldo.test/cb", "c")
	if err != nil || tok != "long" {
		t.Fatalf("UserToken = %q, %v", tok, err)
	}
	pages, err := c.Pages(t.Context(), tok)
	if err != nil || len(pages) != 2 || pages[0].Instagram == nil || pages[0].Instagram.Username != "openb00ks" || pages[1].Instagram != nil {
		t.Fatalf("Pages = %+v, %v", pages, err)
	}
	if _, err := c.UserToken(t.Context(), app, "x", "wrong"); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("a wrong code: %v", err)
	}
}
