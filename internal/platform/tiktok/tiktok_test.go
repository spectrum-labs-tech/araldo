// SPDX-License-Identifier: AGPL-3.0-or-later

package tiktok

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakeTikTok is TikTok's OAuth and Content Posting API for one creator.
type fakeTikTok struct {
	mu       sync.Mutex
	srv      *httptest.Server
	privacy  []string
	maxSecs  int
	init     map[string]any
	ranges   []string
	data     []string
	polls    int
	errCode  string
	failPost bool
}

func (f *fakeTikTok) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(data string) { _, _ = w.Write([]byte(`{"data":` + data + `,"error":{"code":"ok","message":""}}`)) }
	if r.URL.Path == "/v2/oauth/token/" {
		_ = r.ParseForm()
		switch {
		case r.Form.Get("client_secret") != "secret":
			w.WriteHeader(http.StatusUnauthorized)
		case r.Form.Get("grant_type") == "authorization_code" && r.Form.Get("code") == "c" && r.Form.Get("code_verifier") == "v":
			_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":86400,"open_id":"o1","token_type":"Bearer"}`))
		case r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") == "rt":
			_, _ = w.Write([]byte(`{"access_token":"at2","refresh_token":"rt2","expires_in":86400,"open_id":"o1"}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Refresh token is invalid"}`))
		}
		return
	}
	if r.URL.Path == "/upload" {
		data, _ := io.ReadAll(r.Body)
		f.ranges = append(f.ranges, r.Header.Get("Content-Range"))
		f.data = append(f.data, string(data))
		w.WriteHeader(http.StatusCreated)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer at") {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"access_token_invalid","message":"The access token is invalid or not found in the request."}}`))
		return
	}
	switch r.URL.Path {
	case "/v2/user/info/":
		ok(`{"user":{"open_id":"o1","display_name":"AR15.build","username":"ar15build"}}`)
	case "/v2/post/publish/creator_info/query/":
		b, _ := json.Marshal(map[string]any{"privacy_level_options": f.privacy, "max_video_post_duration_sec": f.maxSecs})
		ok(string(b))
	case "/v2/post/publish/video/init/":
		if f.errCode != "" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"` + f.errCode + `","message":"no"}}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&f.init)
		ok(`{"publish_id":"pub1","upload_url":"` + f.srv.URL + `/upload"}`)
	case "/v2/post/publish/status/fetch/":
		f.polls++
		switch {
		case f.failPost:
			ok(`{"status":"FAILED","fail_reason":"duration_check"}`)
		case f.polls < 3:
			ok(`{"status":"PROCESSING_UPLOAD"}`)
		default:
			ok(`{"status":"PUBLISH_COMPLETE","publicaly_available_post_id":["7300000000000000001"]}`) //nolint:misspell // TikTok's spelling
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*fakeTikTok, *Adapter) {
	t.Helper()
	f := &fakeTikTok{privacy: []string{"PUBLIC_TO_EVERYONE", "SELF_ONLY"}, maxSecs: 600}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	a := New(f.srv.Client())
	a.API = f.srv.URL
	a.Sleep = func(context.Context, time.Duration) error { return nil }
	a.Now = func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
	return f, a
}

var app = platform.App{ClientID: "key", ClientSecret: "secret"}

func TestSignIn(t *testing.T) {
	t.Parallel()
	_, a := setup(t)
	u, _ := url.Parse(New(nil).AuthorizeURL(app, "https://araldo.example/cb", "s", "ch"))
	if q := u.Query(); q.Get("client_key") != "key" || q.Get("scope") != scopes || q.Get("code_challenge") != "ch" {
		t.Fatalf("authorize URL %s", u)
	}
	conns, err := a.Exchange(t.Context(), app, "https://araldo.example/cb", "c", "v")
	if err != nil {
		t.Fatal(err)
	}
	c := conns[0]
	if c.Account.ExternalID != "o1" || c.Account.Handle != "@ar15build" || c.Credentials["username"] != "ar15build" ||
		c.Credentials["refresh_token"] != "rt" || !c.ExpiresAt.Equal(a.Now().Add(24*time.Hour)) {
		t.Fatalf("connection %+v", c)
	}
	fresh, _, err := a.Refresh(t.Context(), app, c.Credentials)
	if err != nil || fresh["access_token"] != "at2" || fresh["refresh_token"] != "rt2" || fresh["username"] != "ar15build" {
		t.Fatalf("refresh %v, %v", fresh, err)
	}
	if _, _, err := a.Refresh(t.Context(), app, platform.Credentials{"refresh_token": "old"}); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("a refused refresh: %v", err)
	}
	if _, err := a.Verify(t.Context(), platform.Credentials{"access_token": "nope"}); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("a bad token: %v", err)
	}
}

func TestPublishUploadsInParts(t *testing.T) {
	t.Parallel()
	f, a := setup(t)
	a.ChunkSize = 4
	data := strings.Repeat("v", 10)
	v := platform.Media{Type: "video/mp4", Size: 10, Duration: 30 * time.Second}.WithData([]byte(data))
	creds := platform.Credentials{"access_token": "at", "username": "ar15build"}
	res, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"Range day #ar15"}, Media: []platform.Media{v}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	post, _ := f.init["post_info"].(map[string]any)
	src, _ := f.init["source_info"].(map[string]any)
	// The video is smaller than TikTok's 5 MB minimum part: one part.
	if post["title"] != "Range day #ar15" || post["privacy_level"] != "PUBLIC_TO_EVERYONE" || src["source"] != "FILE_UPLOAD" ||
		src["total_chunk_count"].(float64) != 1 || strings.Join(f.ranges, ",") != "bytes 0-9/10" ||
		res.Permalink != "https://www.tiktok.com/@ar15build/video/7300000000000000001" || f.polls != 3 {
		t.Fatalf("init %v, ranges %v, result %+v, polls %d", f.init, f.ranges, res, f.polls)
	}
}

func TestUploadsPartsInOrder(t *testing.T) {
	t.Parallel()
	f, a := setup(t)
	v := platform.Media{Type: "video/mp4", Size: 10}.WithData([]byte("0123456789"))
	if err := a.upload(t.Context(), f.srv.URL+"/upload", v, 4, 2); err != nil {
		t.Fatal(err)
	}
	// Two parts: 4 bytes, and the last takes the rest.
	if strings.Join(f.ranges, ",") != "bytes 0-3/10,bytes 4-9/10" || strings.Join(f.data, "|") != "0123|456789" {
		t.Fatalf("ranges %v, data %v", f.ranges, f.data)
	}
}

func TestPublishRules(t *testing.T) {
	t.Parallel()
	v := platform.Media{Type: "video/mp4", Size: 3, Duration: 90 * time.Second}.WithData([]byte("mp4"))
	creds := platform.Credentials{"access_token": "at"}
	pay := platform.Payload{Parts: []string{"x"}, Media: []platform.Media{v}}

	f, a := setup(t)
	f.privacy = []string{"SELF_ONLY"}
	res, err := a.Publish(t.Context(), creds, pay, nil)
	if err != nil || f.init["post_info"].(map[string]any)["privacy_level"] != "SELF_ONLY" || res.Parts[0].ID == "" {
		t.Fatalf("an unaudited app posts privately: %v, %v", f.init, err)
	}

	f, a = setup(t)
	f.maxSecs = 60
	if _, err := a.Publish(t.Context(), creds, pay, nil); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("longer than the creator may post: %v", err)
	}

	f, a = setup(t)
	f.errCode = "spam_risk_too_many_posts"
	var pe *platform.Error
	if _, err := a.Publish(t.Context(), creds, pay, nil); !errors.As(err, &pe) || pe.Kind != platform.RateLimited {
		t.Fatalf("too many posts: %v", err)
	}

	f, a = setup(t)
	f.errCode = "unaudited_client_can_only_post_to_private_accounts"
	if _, err := a.Publish(t.Context(), creds, pay, nil); !errors.As(err, &pe) || pe.Kind != platform.Rejected || !strings.Contains(pe.Msg, "audit") {
		t.Fatalf("unaudited, public account: %v", err)
	}

	f, a = setup(t)
	f.failPost = true
	if _, err := a.Publish(t.Context(), creds, pay, nil); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("a post TikTok failed: %v", err)
	}
	if _, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"no video"}}, nil); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("a post without a video: %v", err)
	}
}
