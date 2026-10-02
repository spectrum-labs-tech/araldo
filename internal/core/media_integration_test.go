// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/media"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// pngOf encodes a blank w×h PNG.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// noisyPNG encodes random pixels, which do not compress: about 4·w·h bytes.
func noisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	_, _ = rand.Read(img.Pix)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// hasProblem reports whether err is a validation error listing code.
func hasProblem(err error, code string) bool {
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		return false
	}
	if ae.Code == code {
		return true
	}
	for _, p := range ae.Problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

func TestPostWithImagesPublishes(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	m, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 1200, 800),
		Filename: `C:\fakepath\build.png`, Alt: "  The build on a bench  "})
	if err != nil {
		t.Fatal(err)
	}
	if m.ContentType != media.PNG || m.Width != 1200 || m.Height != 800 || m.Alt != "The build on a bench" ||
		m.Filename != "build.png" || m.Storage != model.StoragePostgres || len(m.SHA256) != 32 || m.Livemode {
		t.Fatalf("media = %+v", m)
	}

	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "New build"}, Media: []uuid.UUID{m.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Media) != 1 || p.Media[0].ID != m.ID {
		t.Fatalf("post media = %+v", p.Media)
	}
	got := settle(t, w, p.ID)
	if got.Status != model.PostPublished {
		t.Fatalf("status %s, targets %+v", got.Status, got.Targets)
	}
	if v := core.ViewPost(got); len(v.Media) != 1 || v.Media[0].ID != id.Format(id.Media, m.ID) || v.Media[0].Alt != "The build on a bench" {
		t.Fatalf("post view media = %+v", v.Media)
	}

	// A post uses it, so it stays.
	if err := w.s.DeleteMedia(ctx, w.owner, m.ID); !hasProblem(err, "media_in_use") {
		t.Fatalf("deleting media a post uses: %v", err)
	}
	// Its alt text can still be fixed.
	if m2, err := w.s.UpdateMediaAlt(ctx, w.owner, m.ID, "The build, left side"); err != nil || m2.Alt != "The build, left side" {
		t.Fatalf("UpdateMediaAlt = %+v, %v", m2, err)
	}
	_, rc, err := w.s.MediaContent(ctx, w.owner, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := media.Inspect(data); err != nil {
		t.Fatalf("stored file: %v", err)
	}
}

func TestImageOnlyPost(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	m, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 10, 10)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{}, Media: []uuid.UUID{m.ID}})
	if err != nil {
		t.Fatalf("a post with an image and no text: %v", err)
	}
	if got := settle(t, w, p.ID); got.Status != model.PostPublished {
		t.Fatalf("status %s", got.Status)
	}
	if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{}}); !hasProblem(err, "content_empty") {
		t.Fatalf("a post with neither: %v", err)
	}
}

func TestImagesAreCheckedPerChannel(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	// Bluesky takes images up to 1,000,000 bytes; Mastodon up to 16 MiB.
	big, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: noisyPNG(t, 600, 600)})
	if err != nil {
		t.Fatal(err)
	}
	if big.Size <= 1_000_000 {
		t.Fatalf("test image is only %d bytes", big.Size)
	}
	mastodon, err := w.s.ConnectChannel(ctx, w.owner, core.ConnectInput{BrandID: w.brand.ID, Provider: platform.Sandbox,
		Fields: map[string]string{"emulates": "mastodon"}})
	if err != nil {
		t.Fatal(err)
	}
	in := core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "big"}, Media: []uuid.UUID{big.ID}}
	_, err = w.s.CreatePost(ctx, w.owner, in)
	var ae *apperr.Error
	if !errors.As(err, &ae) || len(ae.Problems) != 1 || ae.Problems[0].Code != "media_too_large" ||
		ae.Problems[0].Detail["media"] != id.Format(id.Media, big.ID) || ae.Problems[0].Detail["channel"] != id.Format(id.Channel, w.channel.ID) {
		t.Fatalf("CreatePost with a 1.4 MB image on Bluesky and Mastodon = %+v", err)
	}
	renders, err := w.s.PreviewPost(ctx, w.owner, in)
	if err != nil || len(renders) != 2 {
		t.Fatalf("PreviewPost = %+v, %v", renders, err)
	}
	for _, r := range renders {
		bad := len(r.Violations) > 0
		if bad != (r.Provider == platform.Bluesky) {
			t.Errorf("%s violations %+v: only Bluesky should refuse it", r.Provider, r.Violations)
		}
	}
	// Choosing only the Mastodon channel works.
	in.Channels = []uuid.UUID{mastodon.ID}
	if _, err := w.s.CreatePost(ctx, w.owner, in); err != nil {
		t.Fatalf("the image on Mastodon alone: %v", err)
	}
}

func TestMediaValidation(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	tests := []struct {
		name string
		in   core.MediaInput
		code string
	}{
		{"empty", core.MediaInput{}, "media_empty"},
		{"not an image", core.MediaInput{Data: []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")}, "media_type_unsupported"},
		{"too large", core.MediaInput{Data: make([]byte, media.MaxBytes+1)}, "media_too_large"},
		{"alt too long", core.MediaInput{Data: pngOf(t, 1, 1), Alt: strings.Repeat("é", media.MaxAlt+1)}, "alt_too_long"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.in.BrandID = w.brand.ID
			if _, err := w.s.CreateMedia(t.Context(), w.owner, tt.in); !hasProblem(err, tt.code) {
				t.Fatalf("CreateMedia = %v, want %s", err, tt.code)
			}
		})
	}
}

func TestMediaBelongsToItsBrandAndMode(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	m, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 2, 2)})
	if err != nil {
		t.Fatal(err)
	}
	other, err := w.s.CreateBrand(ctx, w.owner, core.BrandInput{Name: "Other brand"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.ConnectChannel(ctx, w.owner, core.ConnectInput{BrandID: other.ID, Provider: platform.Sandbox,
		Fields: map[string]string{"emulates": "bluesky"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: other.ID, Content: &model.Content{Body: "x"}, Media: []uuid.UUID{m.ID}}); !hasProblem(err, "media_other_brand") {
		t.Fatalf("another brand's media: %v", err)
	}
	if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "x"}, Media: []uuid.UUID{m.ID, m.ID}}); !hasProblem(err, "media_duplicate") {
		t.Fatalf("the same media twice: %v", err)
	}
	live := w.owner
	live.Livemode = true
	if _, err := w.s.Media(ctx, live, m.ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("live mode reading test-mode media: %v", err)
	}
	// A key limited to another brand cannot see it.
	plain, _, err := w.s.CreateOperatorAPIKey(ctx, w.owner, core.APIKeyInput{Name: "other", Scopes: []string{"posts:write"}, BrandID: &other.ID})
	if err != nil {
		t.Fatal(err)
	}
	key, err := w.s.AuthenticateKey(ctx, plain, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Media(ctx, key, m.ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("a key for another brand reading media: %v", err)
	}
	if list, _, err := w.s.MediaList(ctx, key, core.MediaFilter{}, store.Page{}); err != nil || len(list) != 0 {
		t.Fatalf("a key for another brand listing media: %d items, %v", len(list), err)
	}
}

func TestKeysWithOnlyPostsWrite(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	plain, _, err := w.s.CreateOperatorAPIKey(ctx, w.owner, core.APIKeyInput{Name: "writer", Scopes: []string{"posts:write"}, BrandID: &w.brand.ID})
	if err != nil {
		t.Fatal(err)
	}
	key, err := w.s.AuthenticateKey(ctx, plain, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.s.CreateMedia(ctx, key, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 3, 3)})
	if err != nil {
		t.Fatalf("a posts:write key uploading media: %v", err)
	}
	if _, err := w.s.CreatePost(ctx, key, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "x"}, Media: []uuid.UUID{m.ID}}); err != nil {
		t.Fatalf("a posts:write key posting: %v", err)
	}
	readOnly := key
	readOnly.Scopes = []string{"posts:read"}
	if _, err := w.s.CreateMedia(ctx, readOnly, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 3, 3)}); kind(err) != apperr.KindForbidden {
		t.Fatalf("a read-only key uploading: %v", err)
	}
	if _, err := w.s.Media(ctx, readOnly, m.ID); err != nil {
		t.Fatalf("a read-only key reading media: %v", err)
	}
}

func TestImportMedia(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	img := pngOf(t, 30, 20)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old/logo.png":
			http.Redirect(rw, r, "/logo.png", http.StatusFound)
		case "/logo.png":
			rw.Header().Set("Content-Type", "text/plain") // the bytes decide, not the header
			_, _ = rw.Write(img)
		default:
			http.NotFound(rw, r)
		}
	}))
	defer srv.Close()

	// The test service allows private addresses; netguard's own tests cover
	// refusing them. Redirects are followed.
	m, err := w.s.ImportMedia(ctx, w.owner, w.brand.ID, srv.URL+"/old/logo.png", "Logo")
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 30 || m.Height != 20 || m.Filename != "logo.png" || m.Alt != "Logo" {
		t.Fatalf("imported %+v", m)
	}
	if _, err := w.s.ImportMedia(ctx, w.owner, w.brand.ID, srv.URL+"/missing.png", ""); !hasProblem(err, "url_unreachable") {
		t.Fatalf("a 404: %v", err)
	}
	if _, err := w.s.ImportMedia(ctx, w.owner, w.brand.ID, "file:///etc/passwd", ""); !hasProblem(err, "url_invalid") {
		t.Fatalf("a file URL: %v", err)
	}
}

func TestUnusedMediaIsPruned(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	unused, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 5, 5)})
	if err != nil {
		t.Fatal(err)
	}
	used, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 6, 6)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "x"}, Media: []uuid.UUID{used.ID}}); err != nil {
		t.Fatal(err)
	}

	if n, err := core.PruneUnusedMediaOrg(w.s, w.org.ID); err != nil || n != 0 {
		t.Fatalf("pruning fresh media: %d, %v", n, err)
	}
	w.s.Now = func() time.Time { return time.Now().Add(core.UnusedMediaTTL + time.Hour) }
	if n, err := core.PruneUnusedMediaOrg(w.s, w.org.ID); err != nil || n != 1 {
		t.Fatalf("pruning a day later: %d, %v; want 1", n, err)
	}
	if _, err := w.s.Media(ctx, w.owner, unused.ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("unused media after pruning: %v", err)
	}
	if _, err := w.s.Media(ctx, w.owner, used.ID); err != nil {
		t.Fatalf("media a post uses was pruned: %v", err)
	}
	list, _, err := w.s.MediaList(ctx, w.owner, core.MediaFilter{}, store.Page{})
	if err != nil || len(list) != 1 || list[0].ID != used.ID {
		t.Fatalf("listing after pruning: %d items, %v", len(list), err)
	}
}

// memBlobs is S3 in memory.
type memBlobs struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (b *memBlobs) Put(_ context.Context, key string, data []byte, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = bytes.Clone(data)
	return nil
}

func (b *memBlobs) Get(_ context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.objects[key]
	if !ok {
		return nil, errors.New("no such object")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *memBlobs) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.objects, key)
	return nil
}

func (b *memBlobs) get(key string) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.objects[key]
}

func (b *memBlobs) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.objects)
}

// TestMediaInObjectStorage stores files through Blobs. It schedules its
// post ahead and never publishes: workers in other tests claim due posts in
// every org, and they have no object storage. Publishing reads files through
// the same openMedia as MediaContent, which the Postgres tests publish with.
func TestMediaInObjectStorage(t *testing.T) {
	t.Parallel()
	blobs := &memBlobs{objects: map[string][]byte{}}
	w := newWorld(t, func(c *core.Config) { c.Blobs = blobs })
	ctx := t.Context()
	data := pngOf(t, 40, 40)
	m, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	key := id.Format(id.Org, w.org.ID) + "/" + id.Format(id.Media, m.ID) + ".png"
	if m.Storage != model.StorageS3 || m.StorageKey != key || !bytes.Equal(blobs.get(key), data) {
		t.Fatalf("media %+v; objects %d", m, blobs.len())
	}
	_, rc, err := w.s.MediaContent(ctx, w.owner, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("read back %d bytes, want %d", len(got), len(data))
	}
	later := time.Now().Add(time.Hour).Format(time.RFC3339)
	if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "from S3"}, Media: []uuid.UUID{m.ID}, PublishAt: later}); err != nil {
		t.Fatal(err)
	}

	// Deleting media removes its object too.
	unused, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 2, 2)})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.DeleteMedia(ctx, w.owner, unused.ID); err != nil {
		t.Fatal(err)
	}
	if blobs.get(unused.StorageKey) != nil || blobs.len() != 1 {
		t.Fatalf("after deleting: %d objects", blobs.len())
	}

	// A lost object is an error, not an empty image.
	if err := blobs.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.MediaContent(ctx, w.owner, m.ID); err == nil {
		t.Fatal("reading a lost object succeeded")
	}
}
