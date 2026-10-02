// SPDX-License-Identifier: AGPL-3.0-or-later

package mastodon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestPublishThreadWithIdempotencyKeys(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var keys, replies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/accounts/verify_credentials":
			_, _ = w.Write([]byte(`{"id":"1","acct":"araldo","display_name":"Araldo","url":"https://m.test/@araldo"}`))
		case "/api/v1/statuses":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			mu.Lock()
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			reply, _ := in["in_reply_to_id"].(string)
			replies = append(replies, reply)
			n := len(keys)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"id": string(rune('0' + n)), "url": "https://m.test/@araldo/" + string(rune('0'+n))})
		}
	}))
	defer srv.Close()
	a := New(srv.Client())
	creds := platform.Credentials{"instance": srv.URL, "access_token": "tok"}

	acct, err := a.Verify(t.Context(), creds)
	if err != nil || acct.DisplayName != "Araldo" || acct.ExternalID != "1" {
		t.Fatalf("Verify = %+v, %v", acct, err)
	}
	res, err := a.Publish(t.Context(), creds, platform.Payload{Key: "ptgt_9", Parts: []string{"a", "b"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Permalink != "https://m.test/@araldo/1" {
		t.Fatalf("permalink %q", res.Permalink)
	}
	if keys[0] != "ptgt_9-0" || keys[1] != "ptgt_9-1" {
		t.Fatalf("idempotency keys %q", keys)
	}
	if replies[0] != "" || replies[1] != "1" {
		t.Fatalf("in_reply_to %q, want the second part to reply to the first", replies)
	}
}

func TestBadTokenNeedsReauth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	a := New(srv.Client())
	if _, err := a.Verify(t.Context(), platform.Credentials{"instance": srv.URL, "access_token": "bad"}); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("Verify = %v, want auth_revoked", err)
	}
	if _, err := a.Verify(t.Context(), platform.Credentials{"instance": "::::", "access_token": "x"}); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("Verify(bad URL) = %v", err)
	}
}

// fakeServer accepts statuses and media uploads that need processPolls
// checks before they are ready.
type fakeServer struct {
	mu           sync.Mutex
	processPolls int
	polls        map[string]int
	uploads      []map[string]string // description, filename, type, data
	statuses     []map[string]any
	forbidMedia  bool
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api/v2/media":
		if f.forbidMedia {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"This action is outside the authorized scopes"}`))
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		data, _ := io.ReadAll(file)
		f.uploads = append(f.uploads, map[string]string{"description": r.FormValue("description"), "filename": hdr.Filename,
			"type": hdr.Header.Get("Content-Type"), "data": string(data)})
		id := "m" + strconv.Itoa(len(f.uploads))
		if f.processPolls > 0 {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"` + id + `","url":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + id + `","url":"https://m.test/media/` + id + `"}`))
	case strings.HasPrefix(r.URL.Path, "/api/v1/media/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/media/")
		f.polls[id]++
		if f.polls[id] < f.processPolls {
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(`{"id":"` + id + `","url":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + id + `","url":"https://m.test/media/` + id + `"}`))
	case r.URL.Path == "/api/v1/statuses":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		for _, id := range asStrings(in["media_ids"]) {
			if f.processPolls > 0 && f.polls[id] < f.processPolls {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"error":"Cannot attach files that have not finished processing"}`))
				return
			}
		}
		f.statuses = append(f.statuses, in)
		n := strconv.Itoa(len(f.statuses))
		_, _ = w.Write([]byte(`{"id":"` + n + `","url":"https://m.test/@araldo/` + n + `"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func asStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func TestPublishImages(t *testing.T) {
	t.Parallel()
	for _, polls := range []int{0, 3} {
		t.Run("processing polls "+strconv.Itoa(polls), func(t *testing.T) {
			t.Parallel()
			f := &fakeServer{processPolls: polls, polls: map[string]int{}}
			srv := httptest.NewServer(f)
			defer srv.Close()
			a := New(srv.Client())
			a.Poll = time.Millisecond
			p := platform.Payload{Key: "ptgt_m", Parts: []string{"", "second"}, Media: []platform.Media{
				platform.Media{Type: "image/jpeg", Alt: "the build"}.WithData([]byte("jpg")),
				platform.Media{Type: "image/png"}.WithData([]byte("png")),
			}}
			if _, err := a.Publish(t.Context(), platform.Credentials{"instance": srv.URL, "access_token": "tok"}, p, nil); err != nil {
				t.Fatal(err)
			}
			if len(f.uploads) != 2 || f.uploads[0]["description"] != "the build" || f.uploads[0]["data"] != "jpg" ||
				f.uploads[0]["filename"] != "image1.jpg" || f.uploads[1]["type"] != "image/png" {
				t.Fatalf("uploads %v", f.uploads)
			}
			if got := asStrings(f.statuses[0]["media_ids"]); strings.Join(got, ",") != "m1,m2" || f.statuses[0]["status"] != "" {
				t.Fatalf("first status %v", f.statuses[0])
			}
			if _, ok := f.statuses[1]["media_ids"]; ok {
				t.Fatal("images belong on the first part only")
			}
		})
	}
}

func TestMediaScopeMissing(t *testing.T) {
	t.Parallel()
	f := &fakeServer{polls: map[string]int{}, forbidMedia: true}
	srv := httptest.NewServer(f)
	defer srv.Close()
	p := platform.Payload{Key: "ptgt_s", Parts: []string{"hi"}, Media: []platform.Media{platform.Media{Type: "image/png"}.WithData([]byte("png"))}}
	_, err := New(srv.Client()).Publish(t.Context(), platform.Credentials{"instance": srv.URL, "access_token": "tok"}, p, nil)
	var pe *platform.Error
	if !errors.As(err, &pe) || pe.Kind != platform.Rejected || pe.Code != "scope_missing" || !strings.Contains(pe.Msg, "write:media") {
		t.Fatalf("Publish = %v, want a rejection that names the write:media scope", err)
	}
	if len(f.statuses) != 0 {
		t.Fatal("posted without the image")
	}
}
