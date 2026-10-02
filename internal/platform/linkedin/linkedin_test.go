// SPDX-License-Identifier: AGPL-3.0-or-later

package linkedin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestCommentary(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"Plain text":                       "Plain text",
		"Launch #Otium today #rest2026":    `Launch {hashtag|\#|Otium} today {hashtag|\#|rest2026}`,
		"#first word":                      `{hashtag|\#|first} word`,
		"(new) [beta] a_b *bold* @someone": `\(new\) \[beta\] a\_b \*bold\* \@someone`,
		"issue#12 isn't a tag":             `issue\#12 isn't a tag`,
		`back\slash | pipe <tag> ~tilde`:   `back\\slash \| pipe \<tag\> \~tilde`,
	}
	for in, want := range tests {
		if got := Commentary(in); got != want {
			t.Errorf("Commentary(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeLinkedIn is LinkedIn's API: userinfo, images and posts.
type fakeLinkedIn struct {
	mu      sync.Mutex
	srv     *httptest.Server
	posts   []map[string]any
	uploads []string
	headers []http.Header
	expired bool
}

func (f *fakeLinkedIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.expired || r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Expired access token"}`))
		return
	}
	switch {
	case r.URL.Path == "/v2/userinfo":
		_, _ = w.Write([]byte(`{"sub":"abc123","name":"Chris Mancini"}`))
	case r.URL.Path == "/rest/images" && r.URL.Query().Get("action") == "initializeUpload":
		n := string(rune('0' + len(f.uploads) + 1))
		_, _ = w.Write([]byte(`{"value":{"uploadUrl":"` + f.srv.URL + `/upload/` + n + `","image":"urn:li:image:I` + n + `"}}`))
	case strings.HasPrefix(r.URL.Path, "/upload/") && r.Method == http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		f.uploads = append(f.uploads, string(data))
		w.WriteHeader(http.StatusCreated)
	case r.URL.Path == "/rest/posts":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.posts = append(f.posts, in)
		f.headers = append(f.headers, r.Header.Clone())
		w.Header().Set("X-Restli-Id", "urn:li:share:7")
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*fakeLinkedIn, *Adapter) {
	t.Helper()
	f := &fakeLinkedIn{}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	a := New(f.srv.Client())
	a.API = f.srv.URL
	return f, a
}

func TestVerifyAndPublish(t *testing.T) {
	t.Parallel()
	f, a := setup(t)
	creds := platform.Credentials{"access_token": "tok"}
	acct, err := a.Verify(t.Context(), creds)
	if err != nil || acct.ExternalID != "abc123" || acct.Handle != "Chris Mancini" {
		t.Fatalf("Verify = %+v, %v", acct, err)
	}
	res, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"Shipped (v2) #Araldo"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Permalink != "https://www.linkedin.com/feed/update/urn:li:share:7/" {
		t.Fatalf("permalink %q", res.Permalink)
	}
	p := f.posts[0]
	dist, _ := p["distribution"].(map[string]any)
	if p["author"] != "urn:li:person:abc123" || p["commentary"] != `Shipped \(v2\) {hashtag|\#|Araldo}` || p["visibility"] != "PUBLIC" ||
		p["lifecycleState"] != "PUBLISHED" || dist["feedDistribution"] != "MAIN_FEED" || p["content"] != nil {
		t.Fatalf("post %v", p)
	}
	if h := f.headers[0]; h.Get("LinkedIn-Version") != DefaultVersion || h.Get("X-Restli-Protocol-Version") != "2.0.0" {
		t.Fatalf("headers %v", h)
	}
	// A channel can name a newer API version.
	if _, err := a.Publish(t.Context(), platform.Credentials{"access_token": "tok", "api_version": "202701"}, platform.Payload{Parts: []string{"x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if v := f.headers[1].Get("LinkedIn-Version"); v != "202701" {
		t.Fatalf("version %q", v)
	}
}

func TestPublishImages(t *testing.T) {
	t.Parallel()
	f, a := setup(t)
	creds := platform.Credentials{"access_token": "tok"}
	one := platform.Media{Type: "image/jpeg", Alt: "The site"}.WithData([]byte("jpg"))
	if _, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"one"}, Media: []platform.Media{one}}, nil); err != nil {
		t.Fatal(err)
	}
	media, _ := f.posts[0]["content"].(map[string]any)["media"].(map[string]any)
	if media["id"] != "urn:li:image:I1" || media["altText"] != "The site" || f.uploads[0] != "jpg" {
		t.Fatalf("one image: %v, uploads %v", f.posts[0]["content"], f.uploads)
	}
	two := platform.Media{Type: "image/png"}.WithData([]byte("png"))
	if _, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"two"}, Media: []platform.Media{one, two}}, nil); err != nil {
		t.Fatal(err)
	}
	multi, _ := f.posts[1]["content"].(map[string]any)["multiImage"].(map[string]any)
	if imgs, _ := multi["images"].([]any); len(imgs) != 2 {
		t.Fatalf("two images: %v", f.posts[1]["content"])
	}
}

func TestExpiredTokenNeedsReconnecting(t *testing.T) {
	t.Parallel()
	f, a := setup(t)
	f.expired = true
	_, err := a.Publish(t.Context(), platform.Credentials{"access_token": "tok"}, platform.Payload{Parts: []string{"x"}}, nil)
	if platform.KindOf(err) != platform.AuthRevoked || !strings.Contains(err.Error(), "60 days") {
		t.Fatalf("expired token: %v", err)
	}
	if _, err := a.Verify(t.Context(), platform.Credentials{}); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("no token: %v", err)
	}
}
