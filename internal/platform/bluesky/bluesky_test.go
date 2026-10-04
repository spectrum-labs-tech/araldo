// SPDX-License-Identifier: AGPL-3.0-or-later

package bluesky

import (
	"context"
	"encoding/json"
	"fmt"
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
	long := "https://araldo.dev/brands/10/blue-widget-co?utm_source=bluesky&utm_medium=social"
	text, fs := RichText("héllo https://x.dev/a. " + long + " #tag")
	if want := "héllo x.dev/a. araldo.dev/brands/10/bl... #tag"; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if len(fs) != 3 {
		t.Fatalf("facets %+v", fs)
	}
	for i, want := range []struct{ covers, uri string }{{"x.dev/a", "https://x.dev/a"}, {"araldo.dev/brands/10/bl...", long}} {
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
		platform.Media{Type: "image/png", Width: 1200, Height: 800, Alt: "a lamp on a desk"}.WithData([]byte("png-1")),
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
	if first["alt"] != "a lamp on a desk" || blob["mimeType"] != "image/png" || ratio["width"] != float64(1200) || ratio["height"] != float64(800) {
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

func TestEngagementBatchesPublicReads(t *testing.T) {
	t.Parallel()
	var calls [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/xrpc/app.bsky.feed.getPosts" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		uris := r.URL.Query()["uris"]
		calls = append(calls, uris)
		var posts []map[string]any
		for i, u := range uris {
			if strings.HasSuffix(u, "/gone") {
				continue // deleted posts are left out of the answer
			}
			posts = append(posts, map[string]any{"uri": u, "likeCount": i + 1, "repostCount": 2, "replyCount": 3, "quoteCount": 4})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"posts": posts})
	}))
	defer srv.Close()
	a := New(srv.Client())
	a.AppView = srv.URL
	var refs []platform.RemoteRef
	for i := range 30 {
		refs = append(refs, platform.RemoteRef{ID: fmt.Sprintf("at://did:plc:abc/app.bsky.feed.post/%d", i)})
	}
	refs = append(refs, platform.RemoteRef{ID: "at://did:plc:abc/app.bsky.feed.post/gone"})
	got, err := a.Engagement(t.Context(), nil, refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || len(calls[0]) != 25 || len(calls[1]) != 6 {
		t.Fatalf("calls of %d and %d URIs, want 25 and 6", len(calls[0]), len(calls[len(calls)-1]))
	}
	if len(got) != 30 {
		t.Fatalf("%d posts read, want 30 (the deleted one missing)", len(got))
	}
	if c := got[refs[0].ID]; c != (platform.Counts{Likes: 1, Reposts: 2, Replies: 3, Quotes: 4}) {
		t.Fatalf("counts %+v", c)
	}
}

// fakeVideo is a PDS that grants service tokens and records posts, and
// Bluesky's video service, which processes an upload in polls checks.
type fakeVideo struct {
	mu       sync.Mutex
	aud      string
	upload   string // did:name:type:length:data
	polls    int
	conflict bool
	fail     bool
	record   map[string]any
}

func (f *fakeVideo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	blob := `{"$type":"blob","ref":{"$link":"bafkvideo"},"mimeType":"video/mp4","size":9}`
	switch r.URL.Path {
	case "/xrpc/com.atproto.server.createSession":
		_, _ = w.Write([]byte(`{"did":"did:plc:abc","handle":"araldo.test","accessJwt":"jwt",
			"didDoc":{"service":[{"id":"#atproto_pds","serviceEndpoint":"https://morel.us-east.host.bsky.network"}]}}`))
	case "/xrpc/com.atproto.server.getServiceAuth":
		f.aud = r.URL.Query().Get("aud") + " " + r.URL.Query().Get("lxm")
		_, _ = w.Write([]byte(`{"token":"svc"}`))
	case "/xrpc/app.bsky.video.uploadVideo":
		if r.Header.Get("Authorization") != "Bearer svc" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		data, _ := io.ReadAll(r.Body)
		q := r.URL.Query()
		f.upload = q.Get("did") + ":" + q.Get("name") + ":" + r.Header.Get("Content-Type") + ":" + strconv.FormatInt(r.ContentLength, 10) + ":" + string(data)
		if f.conflict {
			w.WriteHeader(http.StatusConflict)
		}
		_, _ = w.Write([]byte(`{"jobId":"job1","state":"JOB_STATE_CREATED","did":"did:plc:abc"}`))
	case "/xrpc/app.bsky.video.getJobStatus":
		f.polls++
		switch {
		case f.fail:
			_, _ = w.Write([]byte(`{"jobStatus":{"jobId":"job1","state":"JOB_STATE_FAILED","error":"Unsupported","message":"bad codec"}}`))
		case f.polls < 3:
			_, _ = w.Write([]byte(`{"jobStatus":{"jobId":"job1","state":"JOB_STATE_ENCODING"}}`))
		default:
			_, _ = w.Write([]byte(`{"jobStatus":{"jobId":"job1","state":"JOB_STATE_COMPLETED","blob":` + blob + `}}`))
		}
	case "/xrpc/com.atproto.repo.getRecord":
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"RecordNotFound"}`))
	case "/xrpc/com.atproto.repo.createRecord":
		var in struct {
			Rkey   string         `json:"rkey"`
			Record map[string]any `json:"record"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.record = in.Record
		_ = json.NewEncoder(w).Encode(map[string]string{"uri": "at://did:plc:abc/app.bsky.feed.post/" + in.Rkey, "cid": "cid"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestPublishVideo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		conflict, fail bool
	}{
		{"processed", false, false},
		{"already uploaded", true, false},
		{"refused by the video service", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeVideo{conflict: tt.conflict, fail: tt.fail}
			srv := httptest.NewServer(f)
			defer srv.Close()
			a := New(srv.Client())
			a.Video = srv.URL
			a.Sleep = func(context.Context, time.Duration) error { return nil }
			v := platform.Media{Type: "video/mp4", Alt: "Launch day", Width: 1080, Height: 1920}.WithData([]byte("mp4 bytes"))
			_, err := a.Publish(t.Context(), platform.Credentials{"identifier": "araldo.test", "app_password": "app-pass", "service": srv.URL},
				platform.Payload{Key: "ptgt_v", KeyTime: time.Unix(1700000000, 0), Parts: []string{"watch"}, Media: []platform.Media{v}}, nil)
			if tt.fail {
				if platform.KindOf(err) != platform.Rejected || f.record != nil {
					t.Fatalf("a refused video: %v, record %v", err, f.record)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.aud != "did:web:morel.us-east.host.bsky.network com.atproto.repo.uploadBlob" ||
				f.upload != "did:plc:abc:video1.mp4:video/mp4:9:mp4 bytes" || f.polls != 3 {
				t.Fatalf("aud %q, upload %q, polls %d", f.aud, f.upload, f.polls)
			}
			embed, _ := f.record["embed"].(map[string]any)
			ratio, _ := embed["aspectRatio"].(map[string]any)
			if embed["$type"] != "app.bsky.embed.video" || embed["alt"] != "Launch day" || ratio["width"].(float64) != 1080 ||
				embed["video"].(map[string]any)["ref"].(map[string]any)["$link"] != "bafkvideo" {
				t.Fatalf("embed %v", embed)
			}
		})
	}
}
