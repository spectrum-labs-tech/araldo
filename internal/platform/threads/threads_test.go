// SPDX-License-Identifier: AGPL-3.0-or-later

package threads

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakeGraph is the Threads API: tokens, profile, containers, publishing,
// permalinks and insights.
type fakeGraph struct {
	mu         sync.Mutex
	containers []url.Values // every container created, in order
	published  []string     // creation_ids published
	polls      map[string]int
	processing int // how many polls a container stays IN_PROGRESS
	expired    bool
}

func (f *fakeGraph) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	if f.expired {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Error validating access token","type":"OAuthException","code":190}}`))
		return
	}
	switch {
	case r.URL.Path == "/oauth/access_token":
		_ = r.ParseForm()
		if r.PostForm.Get("code") != "the-code" || r.PostForm.Get("client_secret") != "secret" || r.PostForm.Get("grant_type") != "authorization_code" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid code","code":100}}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"short","user_id":123}`))
	case r.URL.Path == "/access_token" && q.Get("grant_type") == "th_exchange_token" && q.Get("access_token") == "short":
		_, _ = w.Write([]byte(`{"access_token":"long","token_type":"bearer","expires_in":5184000}`))
	case r.URL.Path == "/refresh_access_token" && q.Get("grant_type") == "th_refresh_token":
		_, _ = w.Write([]byte(`{"access_token":"renewed","expires_in":5184000}`))
	case r.URL.Path == "/v1.0/me":
		_, _ = w.Write([]byte(`{"id":"123","username":"otium.app","name":"Otium"}`))
	case r.URL.Path == "/v1.0/123/threads" && r.Method == http.MethodPost:
		f.containers = append(f.containers, q)
		_, _ = fmt.Fprintf(w, `{"id":"c%d"}`, len(f.containers))
	case r.URL.Path == "/v1.0/123/threads_publish":
		f.published = append(f.published, q.Get("creation_id"))
		_, _ = fmt.Fprintf(w, `{"id":"p%d"}`, len(f.published))
	case strings.HasSuffix(r.URL.Path, "/insights"):
		_, _ = w.Write([]byte(`{"data":[{"name":"views","values":[{"value":900}]},{"name":"likes","values":[{"value":12}]},` +
			`{"name":"replies","values":[{"value":3}]},{"name":"reposts","total_value":{"value":2}},{"name":"quotes","values":[{"value":1}]}]}`))
	case strings.HasPrefix(r.URL.Path, "/v1.0/c") && q.Get("fields") == "status,error_message":
		f.polls[r.URL.Path]++
		status := "FINISHED"
		if f.polls[r.URL.Path] <= f.processing {
			status = "IN_PROGRESS"
		}
		_, _ = fmt.Fprintf(w, `{"status":%q}`, status)
	case strings.HasPrefix(r.URL.Path, "/v1.0/p") && q.Get("fields") == "permalink":
		_, _ = fmt.Fprintf(w, `{"permalink":"https://www.threads.net/@otium.app/post/%s"}`, strings.TrimPrefix(r.URL.Path, "/v1.0/"))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"Unsupported get request","code":100}}`))
	}
}

func setup(t *testing.T) (*fakeGraph, *Adapter) {
	t.Helper()
	f := &fakeGraph{polls: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a := New(srv.Client())
	a.Graph, a.Authorize, a.Poll = srv.URL, "https://threads.net/oauth/authorize", time.Millisecond
	a.Now = func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }
	return f, a
}

var app = platform.App{ClientID: "app-id", ClientSecret: "secret"}

func TestConnect(t *testing.T) {
	t.Parallel()
	_, a := setup(t)
	u, _ := url.Parse(a.AuthorizeURL(app, "https://araldo.test/connect/threads/callback", "st", "ch"))
	q := u.Query()
	if u.Host != "threads.net" || q.Get("client_id") != "app-id" || q.Get("state") != "st" || q.Get("response_type") != "code" ||
		!strings.Contains(q.Get("scope"), "threads_content_publish") || q.Get("redirect_uri") != "https://araldo.test/connect/threads/callback" {
		t.Fatalf("authorize URL %s", u)
	}
	conns, err := a.Exchange(t.Context(), app, "https://araldo.test/connect/threads/callback", "the-code", "")
	if err != nil {
		t.Fatal(err)
	}
	c := conns[0]
	if len(conns) != 1 || c.Account.Handle != "@otium.app" || c.Account.ExternalID != "123" || c.Credentials["access_token"] != "long" ||
		c.Credentials["user_id"] != "123" || c.ExpiresAt == nil || !c.ExpiresAt.Equal(a.Now().Add(60*24*time.Hour)) {
		t.Fatalf("connection %+v", c)
	}
	if _, err := a.Exchange(t.Context(), app, "x", "wrong-code", ""); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("a bad code: %v", err)
	}
	fresh, exp, err := a.Refresh(t.Context(), app, c.Credentials)
	if err != nil || fresh["access_token"] != "renewed" || fresh["user_id"] != "123" || exp == nil {
		t.Fatalf("Refresh = %v, %v, %v", fresh, exp, err)
	}
}

func TestPublishThreadWithImages(t *testing.T) {
	t.Parallel()
	f, a := setup(t)
	f.processing = 2
	creds := platform.Credentials{"access_token": "long", "user_id": "123"}
	one := platform.Media{Type: "image/jpeg", URL: "https://araldo.test/v1/media/media_1/content?sig"}
	res, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"First", "Second"}, Media: []platform.Media{one}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Permalink != "https://www.threads.net/@otium.app/post/p1" || len(res.Parts) != 2 {
		t.Fatalf("result %+v", res)
	}
	first, second := f.containers[0], f.containers[1]
	if first.Get("media_type") != "IMAGE" || first.Get("image_url") != one.URL || first.Get("text") != "First" || first.Get("reply_to_id") != "" {
		t.Fatalf("first container %v", first)
	}
	if second.Get("media_type") != "TEXT" || second.Get("reply_to_id") != "p1" {
		t.Fatalf("second container %v: should reply to the first post", second)
	}
	if f.polls["/v1.0/c1"] != 3 || strings.Join(f.published, ",") != "c1,c2" {
		t.Fatalf("polls %v, published %v", f.polls, f.published)
	}

	two := platform.Media{Type: "image/png", URL: "https://araldo.test/v1/media/media_2/content?sig"}
	if _, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"Carousel"}, Media: []platform.Media{one, two}}, nil); err != nil {
		t.Fatal(err)
	}
	items, carousel := f.containers[2:4], f.containers[4]
	if items[0].Get("is_carousel_item") != "true" || items[1].Get("image_url") != two.URL || carousel.Get("media_type") != "CAROUSEL" ||
		carousel.Get("children") != "c3,c4" {
		t.Fatalf("carousel: items %v, container %v", items, carousel)
	}
}

func TestPublishFailures(t *testing.T) {
	t.Parallel()
	_, a := setup(t)
	creds := platform.Credentials{"access_token": "long", "user_id": "123"}
	_, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"x"}, Media: []platform.Media{{Type: "image/png"}}}, nil)
	if pe, _ := err.(*platform.Error); pe == nil || pe.Kind != platform.Rejected || pe.Code != "media_link_missing" { //nolint:errorlint // the adapter returns *Error
		t.Fatalf("an image without a link: %v", err)
	}
	f, a := setup(t)
	f.expired = true
	if _, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"x"}}, nil); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("an expired token: %v", err)
	}
	if _, _, err := a.Refresh(t.Context(), app, creds); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("refreshing an expired token: %v", err)
	}
	if _, err := a.Publish(t.Context(), platform.Credentials{}, platform.Payload{Parts: []string{"x"}}, nil); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("no token: %v", err)
	}
}

func TestEngagement(t *testing.T) {
	t.Parallel()
	_, a := setup(t)
	got, err := a.Engagement(t.Context(), platform.Credentials{"access_token": "long"}, []platform.RemoteRef{{ID: "p1"}})
	if err != nil {
		t.Fatal(err)
	}
	c := got["p1"]
	if c.Views == nil || *c.Views != 900 || c.Likes != 12 || c.Replies != 3 || c.Reposts != 2 || c.Quotes != 1 {
		t.Fatalf("counts %+v", c)
	}
}

func TestPublishVideo(t *testing.T) {
	t.Parallel()
	f, a := setup(t)
	f.processing = 3
	v := platform.Media{Type: "video/mp4", URL: "https://araldo.test/v1/media/media_9/content?sig"}
	res, err := a.Publish(t.Context(), platform.Credentials{"user_id": "123", "access_token": "long"},
		platform.Payload{Parts: []string{"Range day"}, Media: []platform.Media{v}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := f.containers[0]
	if c.Get("media_type") != "VIDEO" || c.Get("video_url") != v.URL || c.Get("text") != "Range day" || f.polls["/v1.0/c1"] != 4 ||
		len(f.published) != 1 || res.Permalink == "" {
		t.Fatalf("container %v, polls %v, published %v, result %+v", c, f.polls, f.published, res)
	}
}
