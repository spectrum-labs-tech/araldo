// SPDX-License-Identifier: AGPL-3.0-or-later

package x

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// TestSignatureMatchesX checks the signer against the worked example in
// X's documentation ("Creating a signature").
func TestSignatureMatchesX(t *testing.T) {
	t.Parallel()
	oauth := map[string]string{
		"oauth_consumer_key":     "xvz1evFS4wEEPTGEFPHBog",
		"oauth_nonce":            "kYjzVBB8Y0ZFabxSWbWovY3uYSQ2pTgmZeNu2VS4cg",
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        "1318622958",
		"oauth_token":            "370773112-GmHxMAgYyLbNEtIKZeRNFsMKPR9EyMZeS9weJAEb",
		"oauth_version":          "1.0",
	}
	form := url.Values{"status": {"Hello Ladies + Gentlemen, a signed OAuth request!"}}
	got := signature(http.MethodPost, "https://api.twitter.com/1.1/statuses/update.json?include_entities=true", oauth, form,
		"kAcSOqF21Fu85e7zjz7ZN2U4ZRhfV3WpwPAoE3Z7kBw", "LswwdoUaIvS8ltyTt5jkRh4J50vUPVVHtR2YPi5kE")
	if want := "hCtSmYh+iHYCEqBWrE7C7hYmtUk="; got != want {
		t.Fatalf("signature %q, want %q", got, want)
	}
}

func TestEncode(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"Ladies + Gentlemen": "Ladies%20%2B%20Gentlemen", "a~b-c._d": "a~b-c._d", "é!": "%C3%A9%21"} {
		if got := encode(in); got != want {
			t.Errorf("encode(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeX is X's API: users/me, tweets, media upload and metadata.
type fakeX struct {
	mu       sync.Mutex
	tweets   []map[string]any
	uploads  []string // type:name:data
	alt      []string
	auth     []string
	status   int // answer tweets with this status, when set
	response string
}

func (f *fakeX) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	switch r.URL.Path {
	case "/2/oauth2/token":
		_ = r.ParseForm()
		user, pass, _ := r.BasicAuth()
		switch {
		case user != "id" || pass != "secret":
			w.WriteHeader(http.StatusUnauthorized)
		case r.PostForm.Get("grant_type") == "authorization_code" && r.PostForm.Get("code") == "c" && r.PostForm.Get("code_verifier") == "v":
			_, _ = w.Write([]byte(`{"token_type":"bearer","access_token":"bearer1","refresh_token":"r1","expires_in":7200}`))
		case r.PostForm.Get("grant_type") == "refresh_token" && r.PostForm.Get("refresh_token") == "r1":
			_, _ = w.Write([]byte(`{"token_type":"bearer","access_token":"bearer2","refresh_token":"r2","expires_in":7200}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_request","error_description":"Value passed for the token was invalid."}`))
		}
	case "/2/users/me":
		_, _ = w.Write([]byte(`{"data":{"id":"42","name":"AR15.build","username":"ar15build"}}`))
	case "/2/media/upload":
		file, hdr, err := r.FormFile("media")
		if err != nil || r.FormValue("media_category") != "tweet_image" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(file)
		f.uploads = append(f.uploads, hdr.Header.Get("Content-Type")+":"+hdr.Filename+":"+string(data))
		_, _ = w.Write([]byte(`{"data":{"id":"m` + string(rune('0'+len(f.uploads))) + `"}}`))
	case "/2/media/metadata":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		alt, _ := in["metadata"].(map[string]any)["alt_text"].(map[string]any)
		f.alt = append(f.alt, in["id"].(string)+"="+alt["text"].(string))
		_, _ = w.Write([]byte(`{}`))
	case "/2/tweets":
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.response))
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.tweets = append(f.tweets, in)
		_, _ = w.Write([]byte(`{"data":{"id":"t` + string(rune('0'+len(f.tweets))) + `","text":"x"}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*fakeX, *Adapter, platform.Credentials) {
	t.Helper()
	f := &fakeX{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a := New(srv.Client())
	a.API = srv.URL
	return f, a, platform.Credentials{"api_key": "ck", "api_secret": "cs", "access_token": "at", "access_token_secret": "ats"}
}

func TestVerifyAndThread(t *testing.T) {
	t.Parallel()
	f, a, creds := setup(t)
	acct, err := a.Verify(t.Context(), creds)
	if err != nil || acct.Handle != "@ar15build" || acct.ExternalID != "42" || acct.URL != "https://x.com/ar15build" {
		t.Fatalf("Verify = %+v, %v", acct, err)
	}
	p := platform.Payload{Parts: []string{"New build", "Details"}, Media: []platform.Media{
		platform.Media{Type: "image/png", Alt: "Front view"}.WithData([]byte("png")),
	}}
	res, err := a.Publish(t.Context(), creds, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Permalink != "https://x.com/i/web/status/t1" || len(res.Parts) != 2 {
		t.Fatalf("result %+v", res)
	}
	if strings.Join(f.uploads, ",") != "image/png:image1.png:png" || strings.Join(f.alt, ",") != "m1=Front view" {
		t.Fatalf("uploads %v, alt %v", f.uploads, f.alt)
	}
	media, _ := f.tweets[0]["media"].(map[string]any)
	if f.tweets[0]["text"] != "New build" || len(media["media_ids"].([]any)) != 1 {
		t.Fatalf("first tweet %v", f.tweets[0])
	}
	reply, _ := f.tweets[1]["reply"].(map[string]any)
	if reply["in_reply_to_tweet_id"] != "t1" || f.tweets[1]["media"] != nil {
		t.Fatalf("second tweet %v: should reply to the first, without media", f.tweets[1])
	}
	for _, h := range f.auth {
		if !strings.HasPrefix(h, `OAuth oauth_consumer_key="ck"`) || !strings.Contains(h, `oauth_token="at"`) || !strings.Contains(h, "oauth_signature=") {
			t.Fatalf("authorization %q", h)
		}
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		body   string
		kind   platform.Kind
		code   string
	}{
		{429, `{"title":"Too Many Requests"}`, platform.RateLimited, "rate_limited"},
		{401, `{"title":"Unauthorized"}`, platform.AuthRevoked, "unauthorized"},
		{403, `{"detail":"You are not allowed to create a Tweet with duplicate content."}`, platform.Rejected, "duplicate"},
		{403, `{"detail":"You are not permitted to perform this action."}`, platform.Rejected, "rejected"},
	}
	for _, tt := range tests {
		f, a, creds := setup(t)
		f.status, f.response = tt.status, tt.body
		_, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"x"}}, nil)
		pe, _ := err.(*platform.Error) //nolint:errorlint // the adapter returns *Error
		if pe == nil || pe.Kind != tt.kind || pe.Code != tt.code {
			t.Errorf("%d %s: %v, want %s %s", tt.status, tt.body, err, tt.kind, tt.code)
		}
	}
	_, a, _ := setup(t)
	if _, err := a.Publish(t.Context(), platform.Credentials{"api_key": "k"}, platform.Payload{Parts: []string{"x"}}, nil); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("missing credentials: %v", err)
	}
	if a.Idempotent() {
		t.Fatal("X has no idempotency key; the adapter must not claim to be idempotent")
	}
}

func TestAuthorizationIsSigned(t *testing.T) {
	t.Parallel()
	a := New(http.DefaultClient)
	a.Now = func() time.Time { return time.Unix(1318622958, 0) }
	a.Nonce = func() string { return "n" }
	h := a.authorization(credentials{key: "ck", secret: "cs", token: "at", tokenSecret: "ats"}, http.MethodPost, "https://api.x.com/2/tweets", nil)
	want := signature(http.MethodPost, "https://api.x.com/2/tweets", map[string]string{"oauth_consumer_key": "ck", "oauth_nonce": "n",
		"oauth_signature_method": "HMAC-SHA1", "oauth_timestamp": "1318622958", "oauth_token": "at", "oauth_version": "1.0"}, nil, "cs", "ats")
	if !strings.Contains(h, `oauth_signature="`+encode(want)+`"`) || !strings.Contains(h, `oauth_timestamp="1318622958"`) {
		t.Fatalf("header %q, want signature %q", h, want)
	}
}

func TestSignInAndRefresh(t *testing.T) {
	t.Parallel()
	f, a, _ := setup(t)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	app := platform.App{ClientID: "id", ClientSecret: "secret"}

	u, _ := url.Parse(a.AuthorizeURL(app, "https://araldo.test/connect/x/callback", "st", "chal"))
	q := u.Query()
	if q.Get("code_challenge") != "chal" || q.Get("code_challenge_method") != "S256" || q.Get("state") != "st" ||
		!strings.Contains(q.Get("scope"), "media.write") || !strings.Contains(q.Get("scope"), "offline.access") {
		t.Fatalf("authorize URL %s", u)
	}
	conns, err := a.Exchange(t.Context(), app, "https://araldo.test/connect/x/callback", "c", "v")
	if err != nil || len(conns) != 1 {
		t.Fatalf("Exchange = %+v, %v", conns, err)
	}
	c := conns[0]
	if c.Account.Handle != "@ar15build" || c.Credentials["access_token"] != "bearer1" || c.Credentials["refresh_token"] != "r1" ||
		c.ExpiresAt == nil || !c.ExpiresAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("connection %+v", c)
	}

	// A signed-in channel posts with its bearer token.
	if _, err := a.Publish(t.Context(), c.Credentials, platform.Payload{Parts: []string{"Signed in"}}, nil); err != nil {
		t.Fatal(err)
	}
	if last := f.auth[len(f.auth)-1]; last != "Bearer bearer1" {
		t.Fatalf("authorization %q", last)
	}

	// Each renewal replaces the refresh token; the old one is then refused,
	// which means signing in again.
	fresh, exp, err := a.Refresh(t.Context(), app, c.Credentials)
	if err != nil || fresh["access_token"] != "bearer2" || fresh["refresh_token"] != "r2" || exp == nil {
		t.Fatalf("Refresh = %v, %v, %v", fresh, exp, err)
	}
	_, _, err = a.Refresh(t.Context(), app, fresh)
	if pe, _ := err.(*platform.Error); pe == nil || pe.Kind != platform.AuthRevoked { //nolint:errorlint // the adapter returns *Error
		t.Fatalf("refresh with a spent token: %v, want auth_revoked", err)
	}
	// Pasted OAuth 1.0a keys have nothing to renew.
	if _, _, err := a.Refresh(t.Context(), app, platform.Credentials{"api_key": "ck"}); !errors.Is(err, platform.ErrNoRefresh) {
		t.Fatalf("refreshing pasted keys: %v", err)
	}
	if _, err := a.Exchange(t.Context(), platform.App{ClientID: "id", ClientSecret: "wrong"}, "x", "c", "v"); err == nil {
		t.Fatal("a wrong app secret should fail the exchange")
	}
}

// fakeXVideo takes chunked video uploads that need statusPolls status
// checks, or fail, before they can be posted.
type fakeXVideo struct {
	mu          sync.Mutex
	init        map[string]any
	segments    map[int]int // segment index: bytes
	finalized   bool
	statusPolls int
	polls       int
	fail        bool
	tweets      []map[string]any
}

func (f *fakeXVideo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	processing := func(state string) string {
		return `{"data":{"id":"v1","processing_info":{"state":"` + state + `","check_after_secs":1}}}`
	}
	switch {
	case r.URL.Path == "/2/media/upload/initialize":
		_ = json.NewDecoder(r.Body).Decode(&f.init)
		_, _ = w.Write([]byte(`{"data":{"id":"v1","expires_after_secs":86400}}`))
	case r.URL.Path == "/2/media/upload/v1/append":
		file, _, err := r.FormFile("media")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(file)
		idx, _ := strconv.Atoi(r.FormValue("segment_index"))
		f.segments[idx] = len(data)
	case r.URL.Path == "/2/media/upload/v1/finalize":
		f.finalized = true
		_, _ = w.Write([]byte(processing("pending")))
	case r.URL.Path == "/2/media/upload" && r.URL.Query().Get("command") == "STATUS":
		f.polls++
		switch {
		case f.fail:
			_, _ = w.Write([]byte(`{"data":{"processing_info":{"state":"failed","error":{"message":"InvalidMedia"}}}}`))
		case f.polls < f.statusPolls:
			_, _ = w.Write([]byte(processing("in_progress")))
		default:
			_, _ = w.Write([]byte(processing("succeeded")))
		}
	case r.URL.Path == "/2/tweets":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.tweets = append(f.tweets, in)
		_, _ = w.Write([]byte(`{"data":{"id":"t1","text":"x"}}`))
	case r.URL.Path == "/2/media/metadata":
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestPublishVideo(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		f := &fakeXVideo{segments: map[int]int{}, statusPolls: 3, fail: fail}
		srv := httptest.NewServer(f)
		a := New(srv.Client())
		a.API = srv.URL
		var slept []time.Duration
		a.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
		data := make([]byte, chunkSize+10)
		v := platform.Media{Type: "video/mp4", Alt: "Range day"}.WithData(data)
		_, err := a.Publish(t.Context(), platform.Credentials{"access_token": "bearer"},
			platform.Payload{Parts: []string{"watch"}, Media: []platform.Media{v}}, nil)
		srv.Close()
		if fail {
			if platform.KindOf(err) != platform.Rejected || len(f.tweets) != 0 {
				t.Fatalf("a video X failed to process: %v, %d tweets", err, len(f.tweets))
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if f.init["media_category"] != "tweet_video" || f.init["media_type"] != "video/mp4" || f.init["total_bytes"].(float64) != float64(len(data)) ||
			f.segments[0] != chunkSize || f.segments[1] != 10 || !f.finalized || f.polls != 3 || len(slept) != 3 || slept[0] != time.Second {
			t.Fatalf("init %v, segments %v, polls %d, slept %v", f.init, f.segments, f.polls, slept)
		}
		ids, _ := f.tweets[0]["media"].(map[string]any)["media_ids"].([]any)
		if len(ids) != 1 || ids[0] != "v1" {
			t.Fatalf("tweet %v", f.tweets[0])
		}
	}
}
