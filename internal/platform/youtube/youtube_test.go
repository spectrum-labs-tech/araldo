// SPDX-License-Identifier: AGPL-3.0-or-later

package youtube

import (
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

// fakeGoogle is Google's token endpoint and the YouTube Data API for one
// account with channel UC1.
type fakeGoogle struct {
	mu       sync.Mutex
	srv      *httptest.Server
	meta     map[string]any
	uploaded string // content type:length:data
	headers  http.Header
	quota    bool
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/token" {
		_ = r.ParseForm()
		switch {
		case r.Form.Get("client_secret") != "secret":
			w.WriteHeader(http.StatusUnauthorized)
		case r.Form.Get("grant_type") == "authorization_code" && r.Form.Get("code") == "c" && r.Form.Get("code_verifier") == "v":
			_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3599,"token_type":"Bearer"}`))
		case r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") == "rt":
			_, _ = w.Write([]byte(`{"access_token":"at2","expires_in":3599,"token_type":"Bearer"}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		}
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer at") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/youtube/v3/channels":
		_, _ = w.Write([]byte(`{"items":[{"id":"UC1","snippet":{"title":"AR15.build","customUrl":"@ar15build"}}]}`))
	case r.URL.Path == "/upload/youtube/v3/videos" && r.Method == http.MethodPost:
		if f.quota {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"errors":[{"reason":"quotaExceeded"}]}}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&f.meta)
		f.headers = r.Header.Clone()
		w.Header().Set("Location", f.srv.URL+"/session/1")
	case r.URL.Path == "/session/1" && r.Method == http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		f.uploaded = r.Header.Get("Content-Type") + ":" + r.Header.Get("Content-Length") + ":" + string(data)
		_, _ = w.Write([]byte(`{"id":"vid1","snippet":{"title":"x"}}`))
	case r.URL.Path == "/youtube/v3/videos":
		_, _ = w.Write([]byte(`{"items":[{"id":"vid1","statistics":{"viewCount":"1234","likeCount":"56","commentCount":"7"}}]}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*fakeGoogle, *Adapter) {
	t.Helper()
	f := &fakeGoogle{}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	a := New(f.srv.Client())
	a.API, a.Token = f.srv.URL, f.srv.URL+"/token"
	a.Now = func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
	return f, a
}

var app = platform.App{ClientID: "id", ClientSecret: "secret"}

func TestSignIn(t *testing.T) {
	t.Parallel()
	_, a := setup(t)
	u, _ := url.Parse(New(nil).AuthorizeURL(app, "https://araldo.example/cb", "s", "ch"))
	q := u.Query()
	if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("code_challenge") != "ch" || !strings.Contains(q.Get("scope"), "youtube.upload") {
		t.Fatalf("authorize URL %s", u)
	}
	conns, err := a.Exchange(t.Context(), app, "https://araldo.example/cb", "c", "v")
	if err != nil {
		t.Fatal(err)
	}
	c := conns[0]
	if len(conns) != 1 || c.Account.ExternalID != "UC1" || c.Account.Handle != "@ar15build" || c.Credentials["channel_id"] != "UC1" ||
		c.Credentials["refresh_token"] != "rt" || c.ExpiresAt == nil {
		t.Fatalf("connections %+v", conns)
	}
	fresh, _, err := a.Refresh(t.Context(), app, c.Credentials)
	if err != nil || fresh["access_token"] != "at2" || fresh["refresh_token"] != "rt" || fresh["channel_id"] != "UC1" {
		t.Fatalf("refresh %v, %v", fresh, err)
	}
	if acct, err := a.Verify(t.Context(), c.Credentials); err != nil || acct.ExternalID != "UC1" {
		t.Fatalf("verify %+v, %v", acct, err)
	}
	if _, err := a.Verify(t.Context(), platform.Credentials{"access_token": "at", "channel_id": "UC9"}); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("a channel the account lost: %v", err)
	}
	if _, _, err := a.Refresh(t.Context(), app, platform.Credentials{"refresh_token": "revoked"}); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("a revoked refresh token: %v", err)
	}
}

func TestPublishUploadsResumably(t *testing.T) {
	t.Parallel()
	f, a := setup(t)
	v := platform.Media{Type: "video/mp4", Size: 9}.WithData([]byte("mp4 bytes"))
	res, err := a.Publish(t.Context(), platform.Credentials{"access_token": "at", "channel_id": "UC1", "privacy": "unlisted"},
		platform.Payload{Parts: []string{"Building a <light> rifle\nParts list and range notes."}, Media: []platform.Media{v}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snip, _ := f.meta["snippet"].(map[string]any)
	st, _ := f.meta["status"].(map[string]any)
	if res.Permalink != "https://www.youtube.com/watch?v=vid1" || snip["title"] != "Building a ‹light› rifle" ||
		snip["description"] != "Parts list and range notes." || st["privacyStatus"] != "unlisted" ||
		f.headers.Get("X-Upload-Content-Length") != "9" || f.uploaded != "video/mp4:9:mp4 bytes" {
		t.Fatalf("result %+v, meta %v, uploaded %q", res, f.meta, f.uploaded)
	}
	f.quota = true
	_, err = a.Publish(t.Context(), platform.Credentials{"access_token": "at"}, platform.Payload{Parts: []string{"x"}, Media: []platform.Media{v}}, nil)
	var pe *platform.Error
	if !errors.As(err, &pe) || pe.Kind != platform.RateLimited || pe.Code != "quota_exceeded" || pe.RetryAfter == 0 {
		t.Fatalf("a spent quota: %v", err)
	}
	if _, err := a.Publish(t.Context(), platform.Credentials{"access_token": "at"}, platform.Payload{Parts: []string{"x"}}, nil); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("a post without a video: %v", err)
	}
}

func TestEngagement(t *testing.T) {
	t.Parallel()
	_, a := setup(t)
	got, err := a.Engagement(t.Context(), platform.Credentials{"access_token": "at"}, []platform.RemoteRef{{ID: "vid1"}})
	if err != nil || got["vid1"].Likes != 56 || got["vid1"].Replies != 7 || *got["vid1"].Views != 1234 {
		t.Fatalf("engagement %+v, %v", got, err)
	}
}

func TestSplit(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 120)
	tests := []struct {
		text, alt, title, description string
	}{
		{"Title\nBody", "", "Title", "Body"},
		{"Only a title", "", "Only a title", ""},
		{"", "Range day", "Range day", ""},
		{"", "", "Video", ""},
		{long + "\nBody", "", strings.Repeat("x", 99) + "…", long + "\nBody"},
	}
	for _, tt := range tests {
		if title, desc := Split(tt.text, tt.alt); title != tt.title || desc != tt.description {
			t.Errorf("Split(%q, %q) = %q, %q; want %q, %q", tt.text, tt.alt, title, desc, tt.title, tt.description)
		}
	}
}
