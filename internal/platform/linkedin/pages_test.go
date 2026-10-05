// SPDX-License-Identifier: AGPL-3.0-or-later

package linkedin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakePages is LinkedIn's API as a Pages app sees it: the token endpoint,
// the member's administered Pages, the Pages themselves, and posts.
type fakePages struct {
	srv   *httptest.Server
	posts []map[string]any
	acls  string
}

func (f *fakePages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/oauth/accessToken" {
		_ = r.ParseForm()
		if r.PostForm.Get("code") != "the-code" || r.PostForm.Get("client_secret") != "s3cret" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":5184000}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Expired access token"}`))
		return
	}
	switch r.URL.Path {
	case "/rest/organizationAcls":
		q := r.URL.Query()
		if q.Get("q") != "roleAssignee" || q.Get("role") != "ADMINISTRATOR" || q.Get("state") != "APPROVED" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(f.acls))
	case "/rest/organizations/1001":
		_, _ = w.Write([]byte(`{"id":1001,"localizedName":"Otium","vanityName":"otium"}`))
	case "/rest/organizations/2002":
		_, _ = w.Write([]byte(`{"id":2002,"localizedName":"Spectrum Labs"}`))
	case "/rest/posts":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.posts = append(f.posts, in)
		w.Header().Set("X-Restli-Id", "urn:li:share:9")
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setupPages(t *testing.T) (*fakePages, *Pages) {
	t.Helper()
	f := &fakePages{acls: `{"elements":[{"organization":"urn:li:organization:1001","role":"ADMINISTRATOR","state":"APPROVED"},` +
		`{"organization":"urn:li:organization:2002","role":"ADMINISTRATOR","state":"APPROVED"}]}`}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	p := NewPages(f.srv.Client())
	p.API, p.Auth = f.srv.URL, f.srv.URL+"/oauth"
	return f, p
}

// TestPagesSignIn checks the sign-in asks for the Community Management
// API's scopes only (the Pages app has no OpenID Connect), and offers each
// Page the member administers as a channel, named and linked.
func TestPagesSignIn(t *testing.T) {
	t.Parallel()
	f, p := setupPages(t)
	if p.Provider() != platform.LinkedInPages {
		t.Fatalf("provider %q", p.Provider())
	}
	u, err := url.Parse(p.AuthorizeURL(platform.App{ClientID: "client"}, "https://araldo.test/connect/linkedin_pages/callback", "st", ""))
	if err != nil || u.Query().Get("scope") != "r_organization_admin w_organization_social" || strings.Contains(u.RawQuery, "openid") {
		t.Fatalf("authorize URL %v", u)
	}
	conns, err := p.Exchange(t.Context(), platform.App{ClientID: "client", ClientSecret: "s3cret"}, "https://araldo.test/cb", "the-code", "")
	if err != nil || len(conns) != 2 {
		t.Fatalf("Exchange: %d connections, %v", len(conns), err)
	}
	otium := conns[0]
	if otium.Account.ExternalID != "urn:li:organization:1001" || otium.Account.DisplayName != "Otium (LinkedIn Page)" ||
		otium.Account.URL != "https://www.linkedin.com/company/otium/" || otium.Credentials["organization"] != "1001" ||
		otium.Credentials["access_token"] != "tok" || otium.ExpiresAt == nil {
		t.Fatalf("the Otium Page: %+v", otium)
	}
	if conns[1].Account.DisplayName != "Spectrum Labs (LinkedIn Page)" || conns[1].Account.URL != "" {
		t.Fatalf("a Page without a vanity name: %+v", conns[1].Account)
	}
	// Someone who administers no Page is told so, rather than offered nothing.
	f.acls = `{"elements":[]}`
	if _, err := p.Exchange(t.Context(), platform.App{ClientID: "client", ClientSecret: "s3cret"}, "https://araldo.test/cb", "the-code", ""); platform.KindOf(err) != platform.Rejected ||
		!strings.Contains(err.Error(), "administers no Pages") {
		t.Fatalf("no Pages: %v", err)
	}
}

// TestPagesPublish checks a post is written with the Page as its author.
func TestPagesPublish(t *testing.T) {
	t.Parallel()
	f, p := setupPages(t)
	creds := platform.Credentials{"access_token": "tok", "organization": "1001"}
	acct, err := p.Verify(t.Context(), creds)
	if err != nil || acct.DisplayName != "Otium (LinkedIn Page)" {
		t.Fatalf("Verify = %+v, %v", acct, err)
	}
	res, err := p.Publish(t.Context(), creds, platform.Payload{Parts: []string{"Batch inference, cheaper #Otium"}}, nil)
	if err != nil || res.Permalink != "https://www.linkedin.com/feed/update/urn:li:share:9/" {
		t.Fatalf("Publish = %+v, %v", res, err)
	}
	if f.posts[0]["author"] != "urn:li:organization:1001" || f.posts[0]["commentary"] != `Batch inference, cheaper {hashtag|\#|Otium}` {
		t.Fatalf("the post: %v", f.posts[0])
	}
	// The Page ID is a number; a URN works too.
	for _, id := range []string{"", "otium", "10a1"} {
		if _, err := p.Verify(t.Context(), platform.Credentials{"access_token": "tok", "organization": id}); platform.KindOf(err) != platform.Rejected {
			t.Errorf("Page ID %q: %v", id, err)
		}
	}
	if _, err := p.Verify(t.Context(), platform.Credentials{"access_token": "tok", "organization": "urn:li:organization:2002"}); err != nil {
		t.Errorf("a Page URN: %v", err)
	}
}
