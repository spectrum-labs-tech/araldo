// SPDX-License-Identifier: AGPL-3.0-or-later

package x

import (
	"encoding/json"
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
	h := a.authorization(credentials{"ck", "cs", "at", "ats"}, http.MethodPost, "https://api.x.com/2/tweets", nil)
	want := signature(http.MethodPost, "https://api.x.com/2/tweets", map[string]string{"oauth_consumer_key": "ck", "oauth_nonce": "n",
		"oauth_signature_method": "HMAC-SHA1", "oauth_timestamp": "1318622958", "oauth_token": "at", "oauth_version": "1.0"}, nil, "cs", "ats")
	if !strings.Contains(h, `oauth_signature="`+encode(want)+`"`) || !strings.Contains(h, `oauth_timestamp="1318622958"`) {
		t.Fatalf("header %q, want signature %q", h, want)
	}
}
