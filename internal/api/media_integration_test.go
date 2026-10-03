// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/api"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/media"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/sandbox"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

var (
	setupOnce sync.Once
	shared    *store.Store
	setupErr  error
)

// client is an API key's view of a fresh org with one brand and a sandbox
// channel imitating Bluesky. Tests never clean up.
type client struct {
	t     *testing.T
	s     *core.Service
	owner core.Actor
	h     http.Handler
	key   string
	brand string
	// channel is the sandbox channel's ID.
	channel string
}

func newClient(t *testing.T, opts ...func(*core.Config)) *client {
	t.Helper()
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	setupOnce.Do(func() {
		shared, setupErr = store.Open(t.Context(), dsn)
		if setupErr == nil {
			setupErr = shared.Migrate(t.Context())
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	mk, err := keyring.ParseMasterKeys("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := core.Config{BaseURL: "https://araldo.test", AllowPrivateWebhooks: true}
	for _, o := range opts {
		o(&cfg)
	}
	s := core.New(shared, keyring.New(mk, shared), platform.NewRegistry(sandbox.New("https://araldo.test")), log, cfg)
	ctx := t.Context()
	email := fmt.Sprintf("api-%s@example.com", uuid.NewString()[:8])
	u, err := s.CreateUser(ctx, email, "Owner", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	org, err := s.CreateOrg(ctx, u.ID, "Org "+email)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := s.MemberActor(ctx, u.ID, org.ID, false, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBrand(ctx, owner, core.BrandInput{Name: "AR15.build"})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := s.ConnectChannel(ctx, owner, core.ConnectInput{BrandID: b.ID, Provider: platform.Sandbox, Fields: map[string]string{"emulates": "bluesky"}})
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := s.CreateOperatorAPIKey(ctx, owner, core.APIKeyInput{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, s: s, owner: owner, h: api.New(s, log), key: key, brand: id.Format(id.Brand, b.ID), channel: id.Format(id.Channel, ch.ID)}
}

// do sends a request and decodes the JSON answer.
func (c *client) do(method, path, contentType string, body []byte, header map[string]string) (int, map[string]any) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (c *client) json(method, path string, v any) (int, map[string]any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	return c.do(method, path, "application/json", b, nil)
}

// form builds an upload form: fields in order, then the file if not nil.
func form(t *testing.T, fields [][2]string, file []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range fields {
		_ = w.WriteField(f[0], f[1])
	}
	if file != nil {
		fw, _ := w.CreateFormFile("file", "build.png")
		_, _ = fw.Write(file)
	}
	_ = w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUploadAndPostWithMedia(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	body, ct := form(t, [][2]string{{"brand", c.brand}, {"alt", "Front view"}}, pngOf(t, 1200, 800))
	status, m := c.do(http.MethodPost, "/v1/media", ct, body, map[string]string{"Idempotency-Key": uuid.NewString()})
	if status != http.StatusCreated || m["object"] != "media" || m["type"] != media.PNG || m["width"] != float64(1200) ||
		m["alt"] != "Front view" || m["filename"] != "build.png" || m["brand"] != c.brand {
		t.Fatalf("upload: %d %v", status, m)
	}
	mediaID, _ := m["id"].(string)

	status, got := c.do(http.MethodGet, "/v1/media/"+mediaID, "", nil, nil)
	if status != http.StatusOK || got["id"] != mediaID {
		t.Fatalf("get: %d %v", status, got)
	}
	status, got = c.json(http.MethodPost, "/v1/media/"+mediaID, map[string]any{"alt": "Left side"})
	if status != http.StatusOK || got["alt"] != "Left side" {
		t.Fatalf("update: %d %v", status, got)
	}
	status, got = c.do(http.MethodGet, "/v1/media?brand="+c.brand, "", nil, nil)
	if data, _ := got["data"].([]any); status != http.StatusOK || len(data) != 1 {
		t.Fatalf("list: %d %v", status, got)
	}

	status, post := c.json(http.MethodPost, "/v1/posts", map[string]any{"brand": c.brand, "content": map[string]any{"body": "New build"}, "media": []string{mediaID}})
	pm, _ := post["media"].([]any)
	if status != http.StatusCreated || len(pm) != 1 || pm[0].(map[string]any)["id"] != mediaID {
		t.Fatalf("post: %d %v", status, post)
	}
	status, got = c.do(http.MethodDelete, "/v1/media/"+mediaID, "", nil, nil)
	if status != http.StatusConflict || got["code"] != "media_in_use" {
		t.Fatalf("deleting used media: %d %v", status, got)
	}
}

func TestUploadIsIdempotent(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	body, ct := form(t, [][2]string{{"brand", c.brand}}, pngOf(t, 8, 8))
	key := map[string]string{"Idempotency-Key": uuid.NewString()}
	_, first := c.do(http.MethodPost, "/v1/media", ct, body, key)
	_, again := c.do(http.MethodPost, "/v1/media", ct, body, key)
	if first["id"] == nil || first["id"] != again["id"] {
		t.Fatalf("a retried upload made %v then %v", first["id"], again["id"])
	}
}

func TestUploadErrors(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	tests := []struct {
		name   string
		fields [][2]string
		file   []byte
		status int
		code   string
	}{
		{"no file", [][2]string{{"brand", c.brand}}, nil, http.StatusBadRequest, "file_missing"},
		{"unknown field", [][2]string{{"brand", c.brand}, {"caption", "x"}}, pngOf(t, 1, 1), http.StatusBadRequest, "parameter_unknown"},
		{"not an image", [][2]string{{"brand", c.brand}}, []byte("GIF? no."), http.StatusUnprocessableEntity, "media_type_unsupported"},
		{"no brand", nil, pngOf(t, 1, 1), http.StatusUnprocessableEntity, "brand_required"},
		{"too large", [][2]string{{"brand", c.brand}}, make([]byte, media.MaxBytes+128<<10), http.StatusBadRequest, "body_too_large"},
	}
	for _, tt := range tests {
		body, ct := form(t, tt.fields, tt.file)
		status, got := c.do(http.MethodPost, "/v1/media", ct, body, nil)
		if status != tt.status || got["code"] != tt.code {
			t.Errorf("%s: %d %v, want %d %s", tt.name, status, got["code"], tt.status, tt.code)
		}
	}
	status, got := c.json(http.MethodPost, "/v1/media", map[string]any{"brand": c.brand})
	if status != http.StatusBadRequest || got["code"] != "file_missing" {
		t.Errorf("JSON without a url: %d %v", status, got)
	}
	status, got = c.json(http.MethodPost, "/v1/posts", map[string]any{"brand": c.brand, "content": map[string]any{"body": "x"}, "media": []string{"post_01h455vb4pex5vsknk084sn02q"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(got["errors"]), "media_invalid") {
		t.Errorf("a post ID as media: %d %v", status, got)
	}
}

func TestImportByURL(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	img := pngOf(t, 16, 9)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(img) }))
	defer srv.Close()
	status, m := c.json(http.MethodPost, "/v1/media", map[string]any{"brand": c.brand, "url": srv.URL + "/a/logo.png", "alt": "Logo"})
	if status != http.StatusCreated || m["width"] != float64(16) || m["filename"] != "logo.png" || m["alt"] != "Logo" {
		t.Fatalf("import: %d %v", status, m)
	}
}

func TestEngagementEndpoints(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	status, got := c.do(http.MethodGet, "/v1/engagement/summary?group_by=channel&brand="+c.brand, "", nil, nil)
	if data, ok := got["data"].([]any); status != http.StatusOK || !ok || len(data) != 0 || got["group_by"] != "channel" {
		t.Fatalf("summary of a new org: %d %v", status, got)
	}
	tests := []struct {
		query  string
		status int
		code   string
	}{
		{"group_by=day", http.StatusUnprocessableEntity, "group_by_invalid"},
		{"since=yesterday", http.StatusBadRequest, "parameter_invalid"},
		{"limit=0", http.StatusBadRequest, "parameter_invalid"},
		{"since=2026-10-02T00:00:00Z&until=2026-10-01T00:00:00Z", http.StatusUnprocessableEntity, "window_invalid"},
	}
	for _, tt := range tests {
		status, got := c.do(http.MethodGet, "/v1/engagement/summary?"+tt.query, "", nil, nil)
		if status != tt.status || got["code"] != tt.code {
			t.Errorf("summary?%s: %d %v, want %d %s", tt.query, status, got["code"], tt.status, tt.code)
		}
	}
	status, got = c.do(http.MethodGet, "/v1/post_targets/"+id.Make(id.Target)+"/engagement", "", nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("readings of an unknown target: %d %v", status, got)
	}
}

func TestSignedMediaLinks(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	img := pngOf(t, 12, 12)
	m, err := c.s.CreateMedia(t.Context(), c.owner, core.MediaInput{BrandID: mustBrand(t, c), Data: img})
	if err != nil {
		t.Fatal(err)
	}
	other, err := c.s.CreateMedia(t.Context(), c.owner, core.MediaInput{BrandID: mustBrand(t, c), Data: pngOf(t, 3, 3)})
	if err != nil {
		t.Fatal(err)
	}
	link := c.s.MediaLink(m)
	u, err := url.Parse(link)
	if err != nil || !strings.HasPrefix(link, "https://araldo.test/v1/media/media_") {
		t.Fatalf("link %q", link)
	}
	fetch := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil)) // no API key
		return rec
	}
	rec := fetch(u.RequestURI())
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), img) {
		t.Fatalf("a signed link: %d %q, %d bytes", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	q := u.Query()
	tampered := u.Path + "?expires=" + q.Get("expires") + "&signature=" + strings.Repeat("0", len(q.Get("signature")))
	later := u.Path + "?expires=" + strconv.FormatInt(time.Now().Add(48*time.Hour).Unix(), 10) + "&signature=" + q.Get("signature")
	swapped := "/v1/media/" + id.Format(id.Media, other.ID) + "/content?" + u.RawQuery
	for name, target := range map[string]string{"tampered": tampered, "extended": later, "another file": swapped,
		"no signature": u.Path, "an unknown parameter": u.RequestURI() + "&x=1"} {
		if rec := fetch(target); rec.Code == http.StatusOK {
			t.Errorf("%s link was served", name)
		}
	}
	// Expired.
	c.s.Now = func() time.Time { return time.Now().Add(2 * core.MediaLinkTTL) }
	if rec := fetch(u.RequestURI()); rec.Code != http.StatusNotFound {
		t.Fatalf("an expired link: %d", rec.Code)
	}
}

func mustBrand(t *testing.T, c *client) uuid.UUID {
	t.Helper()
	bid, err := id.Parse(id.Brand, c.brand)
	if err != nil {
		t.Fatal(err)
	}
	return bid
}
