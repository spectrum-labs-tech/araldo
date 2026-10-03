// SPDX-License-Identifier: AGPL-3.0-or-later

package instagram

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/meta"
)

// fakeIG is an Instagram account on the Graph API.
type fakeIG struct {
	mu         sync.Mutex
	containers []map[string]string
	published  []string
	polls      int
	processing int
}

func (f *fakeIG) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/"+meta.Version)
	q := r.URL.Query()
	switch {
	case path == "/ig1/media" && r.Method == http.MethodPost:
		_ = r.ParseForm()
		c := map[string]string{}
		for k := range r.PostForm {
			c[k] = r.PostForm.Get(k)
		}
		f.containers = append(f.containers, c)
		_, _ = fmt.Fprintf(w, `{"id":"c%d"}`, len(f.containers))
	case path == "/ig1/media_publish":
		_ = r.ParseForm()
		f.published = append(f.published, r.PostForm.Get("creation_id"))
		_, _ = fmt.Fprintf(w, `{"id":"m%d"}`, len(f.published))
	case strings.HasPrefix(path, "/c") && q.Get("fields") == "status_code":
		f.polls++
		status := "FINISHED"
		if f.polls <= f.processing {
			status = "IN_PROGRESS"
		}
		_, _ = fmt.Fprintf(w, `{"status_code":%q}`, status)
	case strings.HasPrefix(path, "/m") && q.Get("fields") == "permalink":
		_, _ = fmt.Fprintf(w, `{"permalink":"https://www.instagram.com/p%s/"}`, path)
	case strings.HasPrefix(path, "/m"):
		_, _ = w.Write([]byte(`{"like_count":31,"comments_count":5}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*fakeIG, *Adapter, platform.Credentials) {
	t.Helper()
	f := &fakeIG{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a := New(srv.Client())
	a.Meta.Graph, a.Meta.Poll = srv.URL, time.Millisecond
	return f, a, platform.Credentials{"ig_user_id": "ig1", "access_token": "pt"}
}

func TestPublishPhotoAndCarousel(t *testing.T) {
	t.Parallel()
	f, a, creds := setup(t)
	f.processing = 2
	photo := platform.Media{Type: "image/jpeg", Alt: "The build", URL: "https://araldo.test/v1/media/media_1/content?sig"}
	res, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"New build #ar15"}, Media: []platform.Media{photo}}, nil)
	if err != nil || res.Permalink != "https://www.instagram.com/p/m1/" {
		t.Fatalf("photo: %+v, %v", res, err)
	}
	if c := f.containers[0]; c["image_url"] != photo.URL || c["caption"] != "New build #ar15" || c["alt_text"] != "The build" || f.polls != 3 {
		t.Fatalf("container %v after %d polls", c, f.polls)
	}

	second := platform.Media{Type: "image/jpeg", URL: "https://araldo.test/v1/media/media_2/content?sig"}
	if _, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"Carousel"}, Media: []platform.Media{photo, second}}, nil); err != nil {
		t.Fatal(err)
	}
	items, carousel := f.containers[1:3], f.containers[3]
	if items[0]["is_carousel_item"] != "true" || items[1]["image_url"] != second.URL || carousel["media_type"] != "CAROUSEL" ||
		carousel["children"] != "c2,c3" || carousel["caption"] != "Carousel" || f.published[1] != "c4" {
		t.Fatalf("carousel: items %v, container %v, published %v", items, carousel, f.published)
	}
}

func TestPublishNeedsAnImageAndALink(t *testing.T) {
	t.Parallel()
	_, a, creds := setup(t)
	codes := map[string][]platform.Media{
		"media_required":     nil,
		"media_link_missing": {{Type: "image/jpeg"}},
	}
	for want, media := range codes {
		_, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"x"}, Media: media}, nil)
		if pe, _ := err.(*platform.Error); pe == nil || pe.Kind != platform.Rejected || pe.Code != want { //nolint:errorlint // the adapter returns *Error
			t.Errorf("%s: %v", want, err)
		}
	}
}

func TestExchangeListsLinkedAccounts(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/"+meta.Version) {
		case "/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"user"}`))
		case "/me/accounts":
			_, _ = w.Write([]byte(`{"data":[{"id":"p1","name":"Otium","access_token":"pt1","instagram_business_account":{"id":"ig1","username":"otium.app","name":"Otium"}},` +
				`{"id":"p2","name":"No Instagram","access_token":"pt2"}]}`))
		}
	}))
	defer srv.Close()
	a := New(srv.Client())
	a.Meta.Graph = srv.URL
	conns, err := a.Exchange(t.Context(), platform.App{ClientID: "id", ClientSecret: "s"}, "https://araldo.test/cb", "code", "")
	if err != nil || len(conns) != 1 || conns[0].Account.Handle != "@otium.app" || conns[0].Credentials["ig_user_id"] != "ig1" ||
		conns[0].Credentials["access_token"] != "pt1" {
		t.Fatalf("Exchange = %+v, %v (only Pages with an Instagram account)", conns, err)
	}
}

func TestEngagement(t *testing.T) {
	t.Parallel()
	_, a, creds := setup(t)
	got, err := a.Engagement(t.Context(), creds, []platform.RemoteRef{{ID: "m1"}})
	if err != nil || got["m1"] != (platform.Counts{Likes: 31, Replies: 5}) {
		t.Fatalf("engagement %+v, %v", got, err)
	}
}

func TestPublishReel(t *testing.T) {
	t.Parallel()
	f, a, creds := setup(t)
	f.processing = 4
	v := platform.Media{Type: "video/mp4", URL: "https://araldo.test/v1/media/media_9/content?sig"}
	res, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"Range day"}, Media: []platform.Media{v}}, nil)
	if err != nil || res.Permalink != "https://www.instagram.com/p/m1/" {
		t.Fatalf("reel: %+v, %v", res, err)
	}
	c := f.containers[0]
	if c["media_type"] != "REELS" || c["video_url"] != v.URL || c["share_to_feed"] != "true" || c["caption"] != "Range day" ||
		c["image_url"] != "" || f.polls != 5 || f.published[0] != "c1" {
		t.Fatalf("container %v after %d polls, published %v", c, f.polls, f.published)
	}
}
