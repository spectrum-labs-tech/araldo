// SPDX-License-Identifier: AGPL-3.0-or-later

package bluesky

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakePDS is a minimal AT Protocol server: sessions and post records.
type fakePDS struct {
	mu      sync.Mutex
	records map[string]map[string]any // rkey → record
	creates int
	badAuth bool
	blobs   []string // uploaded bytes, in order
}

func (f *fakePDS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/xrpc/com.atproto.server.createSession":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if f.badAuth || in["password"] != "app-pass" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"AuthenticationRequired"}`))
			return
		}
		_, _ = w.Write([]byte(`{"did":"did:plc:abc","handle":"araldo.test","accessJwt":"jwt"}`))
	case "/xrpc/com.atproto.repo.getRecord":
		rkey := r.URL.Query().Get("rkey")
		if _, ok := f.records[rkey]; !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"RecordNotFound"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"uri": "at://did:plc:abc/app.bsky.feed.post/" + rkey, "cid": "cid-" + rkey})
	case "/xrpc/com.atproto.repo.uploadBlob":
		if r.Header.Get("Authorization") != "Bearer jwt" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.blobs = append(f.blobs, string(b))
		_ = json.NewEncoder(w).Encode(map[string]any{"blob": map[string]any{"$type": "blob", "ref": map[string]string{"$link": "bafk" + string(b)},
			"mimeType": r.Header.Get("Content-Type"), "size": len(b)}})
	case "/xrpc/com.atproto.repo.createRecord":
		if r.Header.Get("Authorization") != "Bearer jwt" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var in struct {
			Rkey   string         `json:"rkey"`
			Record map[string]any `json:"record"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.records[in.Rkey] = in.Record
		f.creates++
		_ = json.NewEncoder(w).Encode(map[string]string{"uri": "at://did:plc:abc/app.bsky.feed.post/" + in.Rkey, "cid": "cid-" + in.Rkey})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*fakePDS, *Adapter, platform.Credentials) {
	t.Helper()
	f := &fakePDS{records: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, New(srv.Client()), platform.Credentials{"identifier": "araldo.test", "app_password": "app-pass", "service": srv.URL}
}

func TestPublishThread(t *testing.T) {
	t.Parallel()
	f, a, creds := setup(t)
	when := time.Unix(1_700_000_000, 0)
	res, err := a.Publish(t.Context(), creds, platform.Payload{Key: "ptgt_1", KeyTime: when, Parts: []string{"first https://araldo.dev #launch", "second"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Parts) != 2 || f.creates != 2 {
		t.Fatalf("parts %d, creates %d", len(res.Parts), f.creates)
	}
	if !strings.HasPrefix(res.Permalink, "https://bsky.app/profile/araldo.test/post/") {
		t.Fatalf("permalink %q", res.Permalink)
	}
	second := f.records[TID(when, "ptgt_1", 1)]
	reply, _ := second["reply"].(map[string]any)
	root, _ := reply["root"].(map[string]any)
	if root["uri"] != res.Parts[0].Extra["uri"] {
		t.Fatalf("second part does not reply to the first: %v", second["reply"])
	}
	first := f.records[TID(when, "ptgt_1", 0)]
	if facets, _ := first["facets"].([]any); len(facets) != 2 {
		t.Fatalf("facets = %v, want a link and a tag", first["facets"])
	}
}

func TestRetryDoesNotDuplicate(t *testing.T) {
	t.Parallel()
	f, a, creds := setup(t)
	p := platform.Payload{Key: "ptgt_2", KeyTime: time.Unix(1_700_000_000, 0), Parts: []string{"once"}}
	if _, err := a.Publish(t.Context(), creds, p, nil); err != nil {
		t.Fatal(err)
	}
	// The first attempt's answer was lost; the publisher retries.
	p.Attempt = 2
	res, err := a.Publish(t.Context(), creds, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || len(res.Parts) != 1 {
		t.Fatalf("creates %d, want 1 (the retry must find the existing record)", f.creates)
	}
	if !a.Idempotent() {
		t.Fatal("Bluesky adapter should be idempotent")
	}
}

func TestBadCredentialsNeedReauth(t *testing.T) {
	t.Parallel()
	f, a, creds := setup(t)
	f.badAuth = true
	if _, err := a.Verify(t.Context(), creds); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("Verify = %v, want auth_revoked", err)
	}
	if _, err := a.Publish(t.Context(), platform.Credentials{"service": creds["service"]}, platform.Payload{Parts: []string{"x"}}, nil); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("Publish without credentials = %v", err)
	}
}

func TestTID(t *testing.T) {
	t.Parallel()
	when := time.Unix(1_700_000_000, 0)
	a, b := TID(when, "k", 0), TID(when, "k", 1)
	if len(a) != 13 || a == b || a > b {
		t.Fatalf("TIDs %q %q: want 13 chars, distinct, increasing", a, b)
	}
	if TID(when, "k", 0) != a {
		t.Fatal("TID is not deterministic")
	}
	if strings.IndexByte(tidAlphabet, a[0]) >= 16 {
		t.Fatalf("TID %q has the top bit set", a)
	}
}

func TestRichText(t *testing.T) {
	t.Parallel()
	long := "https://ar15.build/brands/10/bear-creek-arsenal?utm_source=bluesky&utm_medium=social"
	text, fs := RichText("héllo https://x.dev/a. " + long + " #tag")
	if want := "héllo x.dev/a. ar15.build/brands/10/be... #tag"; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if len(fs) != 3 {
		t.Fatalf("facets %+v", fs)
	}
	for i, want := range []struct{ covers, uri string }{{"x.dev/a", "https://x.dev/a"}, {"ar15.build/brands/10/be...", long}} {
		f := fs[i]
		if got := text[f.Index.ByteStart:f.Index.ByteEnd]; got != want.covers || f.Features[0].URI != want.uri {
			t.Errorf("link facet %d covers %q opening %q, want %q opening %q", i, got, f.Features[0].URI, want.covers, want.uri)
		}
	}
	tag := fs[2]
	if got := text[tag.Index.ByteStart:tag.Index.ByteEnd]; got != "#tag" || tag.Features[0].Tag != "tag" {
		t.Fatalf("tag facet covers %q (%+v)", got, tag)
	}
}

func TestPublishImages(t *testing.T) {
	t.Parallel()
	f, a, creds := setup(t)
	when := time.Unix(1_700_000_000, 0)
	media := []platform.Media{
		platform.Media{Type: "image/png", Width: 1200, Height: 800, Alt: "a rifle on a bench"}.WithData([]byte("png-1")),
		platform.Media{Type: "image/jpeg", Width: 10, Height: 10}.WithData([]byte("jpg-2")),
	}
	p := platform.Payload{Key: "ptgt_img", KeyTime: when, Parts: []string{"look", "and more"}, Media: media}
	if _, err := a.Publish(t.Context(), creds, p, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.blobs, ",") != "png-1,jpg-2" {
		t.Fatalf("uploaded %q", f.blobs)
	}
	embed, _ := f.records[TID(when, "ptgt_img", 0)]["embed"].(map[string]any)
	imgs, _ := embed["images"].([]any)
	if embed["$type"] != "app.bsky.embed.images" || len(imgs) != 2 {
		t.Fatalf("first part's embed = %v", embed)
	}
	first, _ := imgs[0].(map[string]any)
	blob, _ := first["image"].(map[string]any)
	ratio, _ := first["aspectRatio"].(map[string]any)
	if first["alt"] != "a rifle on a bench" || blob["mimeType"] != "image/png" || ratio["width"] != float64(1200) || ratio["height"] != float64(800) {
		t.Fatalf("first image = %v", first)
	}
	if _, ok := f.records[TID(when, "ptgt_img", 1)]["embed"]; ok {
		t.Fatal("images belong on the first part only")
	}

	// A retry finds both records and uploads nothing again.
	p.Attempt = 2
	if _, err := a.Publish(t.Context(), creds, p, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.blobs) != 2 || f.creates != 2 {
		t.Fatalf("after a retry: %d uploads, %d creates; want 2 and 2", len(f.blobs), f.creates)
	}
}

func TestUnreadableImageIsTransient(t *testing.T) {
	t.Parallel()
	f, a, creds := setup(t)
	p := platform.Payload{Key: "ptgt_lost", KeyTime: time.Unix(1_700_000_000, 0), Parts: []string{"x"},
		Media: []platform.Media{{Type: "image/png", Size: 5}}}
	_, err := a.Publish(t.Context(), creds, p, nil)
	if platform.KindOf(err) != platform.Transient || f.creates != 0 {
		t.Fatalf("Publish = %v with %d creates; want a transient error and no post", err, f.creates)
	}
}
