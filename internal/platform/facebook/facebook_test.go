// SPDX-License-Identifier: AGPL-3.0-or-later

package facebook

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/meta"
)

// fakePage is a Page on the Graph API.
type fakePage struct {
	mu     sync.Mutex
	feed   []map[string]string
	photos []map[string]string // fields, plus "file"
}

func (f *fakePage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/"+meta.Version)
	switch {
	case path == "/p1/feed" && r.Method == http.MethodPost:
		_ = r.ParseForm()
		post := map[string]string{}
		for k := range r.PostForm {
			post[k] = r.PostForm.Get(k)
		}
		f.feed = append(f.feed, post)
		_, _ = fmt.Fprintf(w, `{"id":"p1_%d"}`, len(f.feed))
	case path == "/p1/photos":
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		photo := map[string]string{}
		for k, v := range r.MultipartForm.Value {
			photo[k] = v[0]
		}
		if fh := r.MultipartForm.File["source"]; len(fh) == 1 {
			file, _ := fh[0].Open()
			data, _ := io.ReadAll(file)
			photo["file"] = string(data)
		}
		f.photos = append(f.photos, photo)
		n := len(f.photos)
		_, _ = fmt.Fprintf(w, `{"id":"photo%d","post_id":"p1_photo%d"}`, n, n)
	case strings.HasPrefix(path, "/p1_") && r.URL.Query().Get("fields") == "permalink_url":
		_, _ = fmt.Fprintf(w, `{"permalink_url":"https://www.facebook.com%s"}`, path)
	case strings.HasPrefix(path, "/p1_"):
		_, _ = w.Write([]byte(`{"reactions":{"data":[],"summary":{"total_count":20}},"comments":{"data":[],"summary":{"total_count":4}},"shares":{"count":3}}`))
	case path == "/gone":
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Unsupported get request","code":100}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*fakePage, *Adapter, platform.Credentials) {
	t.Helper()
	f := &fakePage{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a := New(srv.Client())
	a.Meta.Graph = srv.URL
	return f, a, platform.Credentials{"page_id": "p1", "access_token": "pt"}
}

func TestPublish(t *testing.T) {
	t.Parallel()
	f, a, creds := setup(t)
	res, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"Text only"}}, nil)
	if err != nil || res.Permalink != "https://www.facebook.com/p1_1" || f.feed[0]["message"] != "Text only" {
		t.Fatalf("text: %+v, %v, feed %v", res, err, f.feed)
	}

	png := platform.Media{Type: "image/png", Alt: "Front"}.WithData([]byte("png"))
	res, err = a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"One photo"}, Media: []platform.Media{png}}, nil)
	if err != nil || res.Parts[0].ID != "p1_photo1" {
		t.Fatalf("one photo: %+v, %v", res, err)
	}
	if p := f.photos[0]; p["message"] != "One photo" || p["published"] != "true" || p["file"] != "png" || p["alt_text_custom"] != "Front" {
		t.Fatalf("photo %v", p)
	}

	jpg := platform.Media{Type: "image/jpeg"}.WithData([]byte("jpg"))
	res, err = a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"Two photos"}, Media: []platform.Media{png, jpg}}, nil)
	if err != nil || res.Parts[0].ID != "p1_2" {
		t.Fatalf("two photos: %+v, %v", res, err)
	}
	post := f.feed[1]
	if f.photos[1]["published"] != "false" || f.photos[2]["published"] != "false" || post["message"] != "Two photos" ||
		post["attached_media[0]"] != `{"media_fbid":"photo2"}` || post["attached_media[1]"] != `{"media_fbid":"photo3"}` {
		t.Fatalf("multi-photo post %v, photos %v", post, f.photos)
	}
}

func TestEngagement(t *testing.T) {
	t.Parallel()
	_, a, creds := setup(t)
	got, err := a.Engagement(t.Context(), creds, []platform.RemoteRef{{ID: "p1_1"}, {ID: "gone"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["p1_1"] != (platform.Counts{Likes: 20, Replies: 4, Reposts: 3}) {
		t.Fatalf("engagement %+v (a deleted post should be left out)", got)
	}
}

func TestExchangeListsPages(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/"+meta.Version) {
		case "/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"user"}`))
		case "/me/accounts":
			_, _ = w.Write([]byte(`{"data":[{"id":"p1","name":"Otium","access_token":"pt1"},{"id":"p2","name":"VCDS","access_token":"pt2"}]}`))
		}
	}))
	defer srv.Close()
	a := New(srv.Client())
	a.Meta.Graph = srv.URL
	conns, err := a.Exchange(t.Context(), platform.App{ClientID: "id", ClientSecret: "s"}, "https://araldo.test/cb", "code", "")
	if err != nil || len(conns) != 2 || conns[1].Account.DisplayName != "VCDS" || conns[1].Credentials["access_token"] != "pt2" || conns[0].ExpiresAt != nil {
		t.Fatalf("Exchange = %+v, %v", conns, err)
	}
}
