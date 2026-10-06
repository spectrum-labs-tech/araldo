// SPDX-License-Identifier: AGPL-3.0-or-later

package pinterest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakePinterest knows app "app"/"secret", code "c" and refresh token "r",
// which give token "t"; account "builder" with 260 boards (two pages).
type fakePinterest struct {
	mu   sync.Mutex
	pins []pin
	// refreshNoRotate leaves the refresh token out of a refresh.
	refreshNoRotate bool
}

func (f *fakePinterest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/oauth/token" {
		id, secret, ok := r.BasicAuth()
		_ = r.ParseForm()
		grant := r.Form.Get("grant_type")
		if !ok || id != "app" || secret != "secret" ||
			grant == "authorization_code" && r.Form.Get("code") != "c" || grant == "refresh_token" && r.Form.Get("refresh_token") != "r" {
			w.WriteHeader(http.StatusBadRequest)
			write(map[string]any{"code": 1, "message": "Invalid grant"})
			return
		}
		out := map[string]any{"access_token": "t", "token_type": "bearer", "expires_in": 2592000, "refresh_token": "r2"}
		if grant == "refresh_token" && f.refreshNoRotate {
			delete(out, "refresh_token")
		}
		write(out)
		return
	}
	if r.Header.Get("Authorization") != "Bearer t" {
		w.WriteHeader(http.StatusUnauthorized)
		write(map[string]any{"code": 2, "message": "Authentication failed."})
		return
	}
	switch {
	case r.URL.Path == "/user_account":
		write(map[string]any{"id": "u1", "username": "builder"})
	case r.URL.Path == "/boards":
		start := 0
		if b := r.URL.Query().Get("bookmark"); b != "" {
			_, _ = fmt.Sscan(b, &start)
		}
		var items []board
		for i := start; i < min(start+boardsPage, 260); i++ {
			items = append(items, board{ID: fmt.Sprint(1000 + i), Name: fmt.Sprintf("Board %d", i)})
		}
		out := map[string]any{"items": items}
		if start+boardsPage < 260 {
			out["bookmark"] = fmt.Sprint(start + boardsPage)
		}
		write(out)
	case r.URL.Path == "/boards/1000":
		write(board{ID: "1000", Name: "Builds"})
	case strings.HasPrefix(r.URL.Path, "/boards/"):
		w.WriteHeader(http.StatusNotFound)
		write(map[string]any{"code": 40, "message": "Board not found."})
	case r.URL.Path == "/pins/1" && r.URL.Query().Get("pin_metrics") == "true":
		write(map[string]any{"id": "1", "pin_metrics": map[string]any{
			"90d":              map[string]any{"impression": 40, "pin_click": 3},
			"lifetime_metrics": map[string]any{"impression": 120, "reaction": 9, "comment": 2, "pin_click": 5},
		}})
	case r.URL.Path == "/pins/2" && r.URL.Query().Get("pin_metrics") == "true": // made before lifetime metrics
		write(map[string]any{"id": "2", "pin_metrics": map[string]any{"90d": map[string]any{"impression": 15}}})
	case r.URL.Path == "/pins/3" && r.URL.Query().Get("pin_metrics") == "true": // no metrics at all
		write(map[string]any{"id": "3", "pin_metrics": nil})
	case r.URL.Path == "/pins/500":
		w.WriteHeader(http.StatusInternalServerError)
	case r.URL.Path == "/pins" && r.Method == http.MethodPost:
		var p pin
		_ = json.NewDecoder(r.Body).Decode(&p)
		f.pins = append(f.pins, p)
		w.WriteHeader(http.StatusCreated)
		write(map[string]any{"id": "987654"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*fakePinterest, *Adapter) {
	t.Helper()
	f := &fakePinterest{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a := New(srv.Client())
	a.API = srv.URL
	a.Now = func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
	return f, a
}

var app = platform.App{ClientID: "app", ClientSecret: "secret"}

func TestAuthorizeURL(t *testing.T) {
	t.Parallel()
	u, err := url.Parse(New(nil).AuthorizeURL(app, "https://araldo.example/connect/pinterest/callback", "s1", ""))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "www.pinterest.com" || q.Get("client_id") != "app" || q.Get("scope") != scopes || q.Get("state") != "s1" || q.Get("response_type") != "code" {
		t.Fatalf("authorize URL %s", u)
	}
}

func TestExchangeOffersEveryBoard(t *testing.T) {
	t.Parallel()
	_, a := setup(t)
	conns, err := a.Exchange(t.Context(), app, "https://araldo.example/cb", "c", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 260 {
		t.Fatalf("%d boards, want 260 over two pages", len(conns))
	}
	c := conns[0]
	if c.Account.ExternalID != "u1:1000" || c.Account.Handle != "builder" || c.Account.DisplayName != "Board 0 (Pinterest)" ||
		c.Credentials["board_id"] != "1000" || c.Credentials["access_token"] != "t" || c.Credentials["refresh_token"] != "r2" ||
		c.ExpiresAt == nil || !c.ExpiresAt.Equal(a.Now().Add(30*24*time.Hour)) {
		t.Fatalf("first connection %+v", c)
	}
	if _, err := a.Exchange(t.Context(), app, "https://araldo.example/cb", "wrong", ""); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("a bad code: %v", err)
	}
}

func TestRefresh(t *testing.T) {
	t.Parallel()
	f, a := setup(t)
	c := platform.Credentials{"access_token": "old", "refresh_token": "r", "board_id": "1000"}
	fresh, exp, err := a.Refresh(t.Context(), app, c)
	if err != nil || fresh["access_token"] != "t" || fresh["refresh_token"] != "r2" || fresh["board_id"] != "1000" || exp == nil {
		t.Fatalf("refresh: %v, %v, %v", fresh, exp, err)
	}
	f.refreshNoRotate = true
	if fresh, _, _ = a.Refresh(t.Context(), app, c); fresh["refresh_token"] != "r" {
		t.Fatalf("without a new refresh token the old one is kept: %v", fresh)
	}
	if _, _, err := a.Refresh(t.Context(), app, platform.Credentials{"refresh_token": "revoked"}); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("a refused refresh: %v", err)
	}
	if _, _, err := a.Refresh(t.Context(), app, platform.Credentials{}); !errors.Is(err, platform.ErrNoRefresh) {
		t.Fatalf("no refresh token: %v", err)
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	_, a := setup(t)
	tests := []struct {
		name  string
		creds platform.Credentials
		kind  platform.Kind
	}{
		{"a board of the account", platform.Credentials{"access_token": "t", "board_id": "1000"}, ""},
		{"no board", platform.Credentials{"access_token": "t"}, platform.Rejected},
		{"someone else's board", platform.Credentials{"access_token": "t", "board_id": "1"}, platform.Rejected},
		{"a bad token", platform.Credentials{"access_token": "x", "board_id": "1000"}, platform.AuthRevoked},
		{"no token", platform.Credentials{"board_id": "1000"}, platform.AuthRevoked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			acct, err := a.Verify(t.Context(), tt.creds)
			if tt.kind == "" {
				if err != nil || acct.ExternalID != "u1:1000" || acct.DisplayName != "Builds (Pinterest)" {
					t.Fatalf("verify: %+v, %v", acct, err)
				}
				return
			}
			if platform.KindOf(err) != tt.kind {
				t.Fatalf("got %v, want %s", err, tt.kind)
			}
		})
	}
}

func TestPublish(t *testing.T) {
	t.Parallel()
	f, a := setup(t)
	img := []byte("\x89PNG fake")
	m := platform.Media{Type: "image/png", Alt: strings.Repeat("a", 600)}.WithData(img)
	var parts []platform.RemoteRef
	res, err := a.Publish(t.Context(), platform.Credentials{"access_token": "t", "board_id": "1000"},
		platform.Payload{Parts: []string{"Atlas build of the week\nA 16\" upper on a light lower: https://araldo.dev/b/1?utm_source=pinterest."},
			Media: []platform.Media{m}},
		func(r platform.RemoteRef) error { parts = append(parts, r); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.Permalink != "https://www.pinterest.com/pin/987654/" || len(parts) != 1 {
		t.Fatalf("result %+v, parts %v", res, parts)
	}
	got := f.pins[0]
	data, _ := base64.StdEncoding.DecodeString(got.MediaSource.Data)
	if got.BoardID != "1000" || got.Title != "Atlas build of the week" || !strings.HasPrefix(got.Description, "A 16\" upper") ||
		got.Link != "https://araldo.dev/b/1?utm_source=pinterest" || len([]rune(got.AltText)) != maxAlt ||
		got.MediaSource.SourceType != "image_base64" || got.MediaSource.ContentType != "image/png" || string(data) != string(img) {
		t.Fatalf("pin %+v", got)
	}
	if _, err := a.Publish(t.Context(), platform.Credentials{"access_token": "t", "board_id": "1000"},
		platform.Payload{Parts: []string{"no image"}}, nil); platform.KindOf(err) != platform.Rejected {
		t.Fatalf("a pin without an image: %v", err)
	}
	// A pin already made is not made again.
	again, err := a.Publish(context.Background(), platform.Credentials{"access_token": "t"},
		platform.Payload{Parts: []string{"x"}, Posted: []platform.RemoteRef{{ID: "1"}}}, nil)
	if err != nil || len(f.pins) != 1 || len(again.Parts) != 1 {
		t.Fatalf("resuming a published pin: %v, %d pins", err, len(f.pins))
	}
}

func TestSplit(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 101)
	tests := []struct {
		text, title, description string
	}{
		{"Title\nBody text", "Title", "Body text"},
		{"  Title  \n\n  Body  ", "Title", "Body"},
		{"Only one line", "", "Only one line"},
		{long + "\nBody", "", long + "\nBody"},
		{"Title\n   ", "", "Title"},
		{"\nBody", "", "Body"},
	}
	for _, tt := range tests {
		if title, desc := Split(tt.text); title != tt.title || desc != tt.description {
			t.Errorf("Split(%q) = %q, %q; want %q, %q", tt.text, title, desc, tt.title, tt.description)
		}
	}
	if FirstLink("see https://a.example/x, then https://b.example") != "https://a.example/x" || FirstLink("none") != "" {
		t.Fatal("FirstLink")
	}
}

func TestEngagement(t *testing.T) {
	t.Parallel()
	_, a := setup(t)
	creds := platform.Credentials{"access_token": "t"}
	got, err := a.Engagement(t.Context(), creds, []platform.RemoteRef{{ID: "1"}, {ID: "2"}, {ID: "3"}, {ID: "4"}})
	if err != nil {
		t.Fatal(err)
	}
	views := func(n int64) *int64 { return &n }
	want := map[string]platform.Counts{
		"1": {Likes: 9, Replies: 2, Views: views(120)},
		"2": {Views: views(15)},
		"3": {},
	}
	if len(got) != len(want) {
		t.Fatalf("Engagement = %+v, want pins 1-3 and the deleted pin 4 missing", got)
	}
	for id, w := range want {
		g := got[id]
		if g.Likes != w.Likes || g.Replies != w.Replies || (g.Views == nil) != (w.Views == nil) || g.Views != nil && *g.Views != *w.Views {
			t.Errorf("pin %s: %+v, want %+v", id, g, w)
		}
	}
	if _, err := a.Engagement(t.Context(), creds, []platform.RemoteRef{{ID: "500"}}); platform.KindOf(err) != platform.Transient {
		t.Fatalf("a server error: %v", err)
	}
	if _, err := a.Engagement(t.Context(), platform.Credentials{"access_token": "old"}, []platform.RemoteRef{{ID: "1"}}); platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("a revoked token: %v", err)
	}
}
