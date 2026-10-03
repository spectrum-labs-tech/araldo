// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
	"github.com/spectrum-labs-tech/araldo/internal/tmpl"
	"github.com/spectrum-labs-tech/araldo/internal/web"
)

// TestPagesAreAccessible renders every dashboard page with real data and
// checks the WCAG 2.2 AA rules that markup alone decides (contrast is
// TestContrast's): names for every control, alt text, headings, landmarks,
// a skip link, ids and the references to them, and no change of context on
// input.
func TestPagesAreAccessible(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()

	// One of everything a page can show.
	tpl, _, err := d.s.CreateTemplate(ctx, d.owner, core.TemplateInput{BrandID: d.brand.ID, Key: "release", Name: "Release", Source: tmpl.Source{
		Body: "Shipped {{.version}}", Examples: []json.RawMessage{json.RawMessage(`{"version":"1.0"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := d.s.CreateMedia(ctx, d.owner, core.MediaInput{BrandID: d.brand.ID, Data: pngBytes(t, 40, 30), Alt: "A build"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.s.CreatePost(ctx, d.owner, core.PostInput{BrandID: d.brand.ID, Content: &model.Content{Body: "Accessible post"}, Media: []uuid.UUID{m.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var target model.Target
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := d.s.PublishDue(ctx, "a11y"); err != nil {
			t.Fatal(err)
		}
		got, err := d.s.Post(ctx, d.owner, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if target = got.Targets[0]; target.Status == model.TargetPublished || time.Now().After(deadline) {
			break
		}
	}
	// Two scheduled posts: the post page's move form, with a swap.
	var scheduled *model.Post
	for range 2 {
		if scheduled, err = d.s.CreatePost(ctx, d.owner, core.PostInput{BrandID: d.brand.ID, Content: &model.Content{Body: "Later"}, PublishAt: "next_slot"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.s.ConnectAdAccount(ctx, d.owner, core.AdAccountInput{BrandID: d.brand.ID, Network: ads.Sandbox,
		Fields: map[string]string{"name": "Accessible ads"}}); err != nil {
		t.Fatal(err)
	}
	_, key, err := d.s.CreateOperatorAPIKey(ctx, d.owner, core.APIKeyInput{Name: "ci", Scopes: []string{"posts:read"}})
	if err != nil {
		t.Fatal(err)
	}
	ep, _, err := d.s.CreateEndpoint(ctx, d.owner, core.EndpointInput{URL: "https://hooks.example.com/araldo", EventTypes: []string{"post.published"}})
	if err != nil {
		t.Fatal(err)
	}
	events, _, err := d.s.Events(ctx, d.owner, "", store.Page{Limit: 1})
	if err != nil || len(events) == 0 {
		t.Fatalf("events: %d, %v", len(events), err)
	}
	chans, err := d.s.Channels(ctx, d.owner, nil)
	if err != nil {
		t.Fatal(err)
	}

	pages := []string{
		"/", "/posts", "/posts?q=accessible&status=published", "/posts/new", "/posts/" + id.Format(id.Post, p.ID), "/posts/" + id.Format(id.Post, scheduled.ID),
		"/sandbox/" + id.Format(id.Target, target.ID), "/performance", "/ads", "/ads?tag_url=https://example.com/&tag_campaign=launch",
		"/templates", "/templates/new", "/templates/" + id.Format(id.Template, tpl.ID),
		"/channels", "/channels/apps", "/channels/new", "/channels/new?provider=bluesky", "/channels/" + id.Format(id.Channel, chans[0].ID) + "/reconnect",
		"/brands", "/brands/new", "/brands/" + id.Format(id.Brand, d.brand.ID),
		"/keys", "/keys/" + id.Format(id.APIKey, key.ID), "/webhooks", "/webhooks/" + id.Format(id.WebhookEndpoint, ep.ID),
		"/events", "/events/" + id.Format(id.Event, events[0].ID), "/api-reference",
		"/org", "/org/members/" + id.Format(id.User, *d.owner.UserID), "/org/audit", "/org/tasks", "/account", "/confirm",
	}
	for _, path := range pages {
		rec := d.send(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d", path, rec.Code)
			continue
		}
		for _, issue := range a11yIssues(rec.Body.String(), true) {
			t.Errorf("%s: %s", path, issue)
		}
	}

	// A form shown again with its problems.
	form := url.Values{"csrf": {d.login.Session.CSRFToken}, "mode": {"content"}, "brand": {d.brand.Slug}, "body": {""}, "publish": {"now"}, "action": {"create"}}
	r := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := d.send(r)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid post: %d", rec.Code)
	}
	for _, issue := range a11yIssues(rec.Body.String(), true) {
		t.Errorf("POST /posts (with errors): %s", issue)
	}

	// Signed out.
	srv, err := web.New(d.s, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/login"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		for _, issue := range a11yIssues(rec.Body.String(), false) {
			t.Errorf("%s (signed out): %s", path, issue)
		}
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
