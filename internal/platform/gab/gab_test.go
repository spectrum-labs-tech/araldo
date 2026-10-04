// SPDX-License-Identifier: AGPL-3.0-or-later

package gab

import (
	"errors"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// stub serves Gab's API shape and records what it was sent.
type stub struct {
	mu       sync.Mutex
	forms    []url.Values
	keys     []string
	auth     []string
	uploads  []string // the alt text of each image uploaded
	statusID int
}

func (s *stub) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/accounts/verify_credentials":
			_, _ = w.Write([]byte(`{"id":"7","acct":"araldodev","username":"araldodev",
				"display_name":"Araldo","url":"https://gab.com/araldodev"}`))
		case r.URL.Path == "/api/v1/media":
			_, contentType, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil {
				t.Errorf("media content type %q: %v", r.Header.Get("Content-Type"), err)
			}
			mr := multipart.NewReader(r.Body, contentType["boundary"])
			form, err := mr.ReadForm(1 << 20)
			if err != nil {
				t.Errorf("media form: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.uploads = append(s.uploads, form.Value["description"][0])
			n := len(s.uploads)
			s.mu.Unlock()
			// v1 returns the attachment ready to use: no processing wait.
			_, _ = w.Write([]byte(`{"id":"att` + string(rune('0'+n)) + `"}`))
		case r.URL.Path == "/api/v1/statuses" && r.Method == http.MethodPost:
			if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
				t.Errorf("statuses content type = %q, want form encoding", ct)
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			s.mu.Lock()
			s.forms = append(s.forms, r.PostForm)
			s.keys = append(s.keys, r.Header.Get("Idempotency-Key"))
			s.statusID++
			id := s.statusID
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"` + string(rune('0'+id)) + `","url":"https://gab.com/araldodev/posts/` + string(rune('0'+id)) + `"}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/statuses/"):
			switch strings.TrimPrefix(r.URL.Path, "/api/v1/statuses/") {
			case "1":
				_, _ = w.Write([]byte(`{"favourites_count":4,"reblogs_count":2,"replies_count":1}`)) //nolint:misspell // the API's spelling
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newAdapter(t *testing.T, s *stub) (*Adapter, platform.Credentials) {
	t.Helper()
	srv := s.server(t)
	a := New(srv.Client())
	a.BaseURL = srv.URL
	return a, platform.Credentials{"access_token": "tok"}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	a, creds := newAdapter(t, &stub{})
	acct, err := a.Verify(t.Context(), creds)
	if err != nil {
		t.Fatal(err)
	}
	// Gab has one domain, so the handle carries no server part.
	if acct.ExternalID != "7" || acct.Handle != "@araldodev" || acct.DisplayName != "Araldo" {
		t.Errorf("Verify = %+v", acct)
	}
	if acct.URL != "https://gab.com/araldodev" {
		t.Errorf("profile URL = %q", acct.URL)
	}
}

func TestVerifyFallsBackToUsernameAndBuildsAProfileURL(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"8","username":"plain"}`))
	}))
	defer srv.Close()
	a := New(srv.Client())
	a.BaseURL = srv.URL

	acct, err := a.Verify(t.Context(), platform.Credentials{"access_token": "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if acct.Handle != "@plain" || acct.DisplayName != "plain" || acct.URL != srv.URL+"/plain" {
		t.Errorf("Verify = %+v", acct)
	}
}

// No token is the platform's answer to give, not a panic or a generic error:
// AuthRevoked is what moves a channel to needs_reauth.
func TestNoTokenNeedsReauth(t *testing.T) {
	t.Parallel()
	a := New(http.DefaultClient)
	for _, creds := range []platform.Credentials{{}, {"access_token": "   "}} {
		if _, err := a.Verify(t.Context(), creds); platform.KindOf(err) != platform.AuthRevoked {
			t.Errorf("Verify(%v) kind = %q, want %q", creds, platform.KindOf(err), platform.AuthRevoked)
		}
		if _, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"hi"}}, nil); platform.KindOf(err) != platform.AuthRevoked {
			t.Errorf("Publish(%v) kind = %q, want %q", creds, platform.KindOf(err), platform.AuthRevoked)
		}
		if _, err := a.Engagement(t.Context(), creds, nil); platform.KindOf(err) != platform.AuthRevoked {
			t.Errorf("Engagement(%v) kind = %q, want %q", creds, platform.KindOf(err), platform.AuthRevoked)
		}
	}
}

func TestBadTokenNeedsReauth(t *testing.T) {
	t.Parallel()
	s := &stub{}
	a, _ := newAdapter(t, s)
	_, err := a.Verify(t.Context(), platform.Credentials{"access_token": "wrong"})
	if platform.KindOf(err) != platform.AuthRevoked {
		t.Errorf("kind = %q, want %q (err %v)", platform.KindOf(err), platform.AuthRevoked, err)
	}
}

func TestPublishThread(t *testing.T) {
	t.Parallel()
	s := &stub{}
	a, creds := newAdapter(t, s)

	res, err := a.Publish(t.Context(), creds, platform.Payload{Key: "ptgt_9", Parts: []string{"first", "second"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Permalink != "https://gab.com/araldodev/posts/1" {
		t.Errorf("permalink = %q, want the first part's", res.Permalink)
	}
	if len(res.Parts) != 2 || res.Parts[0].ID != "1" || res.Parts[1].ID != "2" {
		t.Fatalf("parts = %+v", res.Parts)
	}
	if got := s.forms[0].Get("status"); got != "first" {
		t.Errorf("first status = %q", got)
	}
	if got := s.forms[0].Get("in_reply_to_id"); got != "" {
		t.Errorf("first part replies to %q, want nothing", got)
	}
	if got := s.forms[1].Get("in_reply_to_id"); got != "1" {
		t.Errorf("second part replies to %q, want the first part", got)
	}
	if s.forms[0].Get("visibility") != "public" {
		t.Errorf("visibility = %q", s.forms[0].Get("visibility"))
	}
	// Sent on the chance Gab honors it, even though Idempotent() is false.
	if s.keys[0] != "ptgt_9-0" || s.keys[1] != "ptgt_9-1" {
		t.Errorf("idempotency keys = %q", s.keys)
	}
}

// A retried target resumes after the parts already published rather than
// posting them again -- the protection that matters, since Gab promises no
// idempotency of its own.
func TestPublishResumesAfterPostedParts(t *testing.T) {
	t.Parallel()
	s := &stub{}
	a, creds := newAdapter(t, s)

	res, err := a.Publish(t.Context(), creds, platform.Payload{
		Key:    "ptgt_9",
		Parts:  []string{"first", "second"},
		Posted: []platform.RemoteRef{{ID: "99", URL: "https://gab.com/araldodev/posts/99"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.forms) != 1 || s.forms[0].Get("status") != "second" {
		t.Fatalf("posted %d time(s): %+v; want only the unposted part", len(s.forms), s.forms)
	}
	if s.forms[0].Get("in_reply_to_id") != "99" {
		t.Errorf("replies to %q, want the already-posted part", s.forms[0].Get("in_reply_to_id"))
	}
	if len(res.Parts) != 2 || res.Parts[0].ID != "99" {
		t.Errorf("parts = %+v, want the posted part kept", res.Parts)
	}
	if res.Permalink != "https://gab.com/araldodev/posts/99" {
		t.Errorf("permalink = %q, want the first part's", res.Permalink)
	}
	if s.keys[0] != "ptgt_9-1" {
		t.Errorf("idempotency key = %q, want the resumed part's index", s.keys[0])
	}
}

func TestPublishUploadsMediaOnTheFirstPartOnly(t *testing.T) {
	t.Parallel()
	s := &stub{}
	a, creds := newAdapter(t, s)

	media := []platform.Media{
		platform.Media{Type: "image/png", Alt: "a base"}.WithData([]byte("png-one")),
		platform.Media{Type: "image/jpeg", Alt: "a shade"}.WithData([]byte("jpeg-two")),
	}
	if _, err := a.Publish(t.Context(), creds, platform.Payload{
		Key: "ptgt_9", Parts: []string{"first", "second"}, Media: media,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.uploads) != 2 || s.uploads[0] != "a base" || s.uploads[1] != "a shade" {
		t.Errorf("uploads = %q, want both images with their alt text", s.uploads)
	}
	// Rails reads repeated media_ids[] as an array, in order.
	if got := s.forms[0]["media_ids[]"]; len(got) != 2 || got[0] != "att1" || got[1] != "att2" {
		t.Errorf("first part media_ids = %q", got)
	}
	if got := s.forms[1]["media_ids[]"]; len(got) != 0 {
		t.Errorf("second part carries media %q; it belongs on the first only", got)
	}
}

// Gab can answer 2xx without a post in the body. We cannot tell whether it
// published, and this adapter is not idempotent, so the attempt has to be
// Uncertain -- which parks the target for a person instead of retrying it.
func TestPublishWithoutAPostIsUncertain(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"an empty object":  `{}`,
		"an error message": `{"error":"Text character limit exceeded"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			a := New(srv.Client())
			a.BaseURL = srv.URL

			_, err := a.Publish(t.Context(), platform.Credentials{"access_token": "tok"},
				platform.Payload{Key: "ptgt_9", Parts: []string{"hi"}}, nil)
			if platform.KindOf(err) != platform.Uncertain {
				t.Fatalf("kind = %q, want %q (err %v)", platform.KindOf(err), platform.Uncertain, err)
			}
			var pe *platform.Error
			if !errors.As(err, &pe) || pe.Code != "no_post_returned" {
				t.Errorf("err = %v, want code no_post_returned", err)
			}
		})
	}
}

func TestPublishCallsOnPartForEachPart(t *testing.T) {
	t.Parallel()
	s := &stub{}
	a, creds := newAdapter(t, s)

	var seen []string
	if _, err := a.Publish(t.Context(), creds, platform.Payload{Key: "k", Parts: []string{"a", "b"}},
		func(ref platform.RemoteRef) error {
			seen = append(seen, ref.ID)
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "1" || seen[1] != "2" {
		t.Errorf("onPart saw %q, want each part as it published", seen)
	}
}

func TestEngagementSkipsDeletedPosts(t *testing.T) {
	t.Parallel()
	s := &stub{}
	a, creds := newAdapter(t, s)

	got, err := a.Engagement(t.Context(), creds, []platform.RemoteRef{{ID: "1"}, {ID: "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("counts = %+v, want only the post Gab still has", got)
	}
	c := got["1"]
	// Gab reports no quotes, so they stay zero and the total is the other three.
	if c.Likes != 4 || c.Reposts != 2 || c.Replies != 1 || c.Quotes != 0 {
		t.Errorf("counts = %+v", c)
	}
	if c.Total() != 7 {
		t.Errorf("total = %d, want 7", c.Total())
	}
	if c.Views != nil {
		t.Errorf("views = %v, want unknown", *c.Views)
	}
}

func TestRulesAndFields(t *testing.T) {
	t.Parallel()
	a := New(http.DefaultClient)
	if a.Provider() != platform.Gab {
		t.Errorf("provider = %q", a.Provider())
	}
	r := a.Rules()
	if r.Provider != platform.Gab || r.MaxLength != 3000 {
		t.Fatalf("rules = %+v; Gab allows 3000 characters", r)
	}
	// A Mastodon fork counts every URL as 23, however long.
	long := "see https://araldo.dev/brands/7/red-widget-co?utm_source=gab&utm_medium=social"
	if n := r.Length(long); n != len("see ")+23 {
		t.Errorf("length = %d, want %d: a URL counts as 23", n, len("see ")+23)
	}
	// Unlike Mastodon there is no server to pick, so a token is the whole setup.
	f := a.Fields()
	if len(f) != 1 || f[0].Name != "access_token" || !f[0].Secret {
		t.Errorf("fields = %+v, want one secret access_token", f)
	}
	if a.Idempotent() {
		t.Error("Idempotent() = true; Gab makes no Idempotency-Key promise, so a retry could post twice")
	}
}

func TestBaseURLDefaultsToGab(t *testing.T) {
	t.Parallel()
	if got := New(http.DefaultClient).base(); got != DefaultBaseURL {
		t.Errorf("base = %q, want %q", got, DefaultBaseURL)
	}
	// An empty or trailing-slash override must not produce a double slash.
	if got := (&Adapter{}).base(); got != DefaultBaseURL {
		t.Errorf("base with no URL = %q, want %q", got, DefaultBaseURL)
	}
	if got := (&Adapter{BaseURL: "https://gab.test/"}).base(); got != "https://gab.test" {
		t.Errorf("base = %q", got)
	}
}
