// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/sandbox"
	"github.com/spectrum-labs-tech/araldo/internal/store"
	"github.com/spectrum-labs-tech/araldo/internal/web"
)

// TestPostFormCarriesImages drives the new-post form as a browser would: an
// upload with a preview, which must keep the image (browsers forget files),
// then scheduling with an edited alt text.
func TestPostFormCarriesImages(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	ctx := t.Context()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mk, err := keyring.ParseMasterKeys("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := core.New(st, keyring.New(mk, st), platform.NewRegistry(sandbox.New("https://araldo.test")), log, core.Config{BaseURL: "https://araldo.test"})
	email := fmt.Sprintf("web-%s@example.com", uuid.NewString()[:8])
	u, err := s.CreateUser(ctx, email, "Owner", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	org, err := s.CreateOrg(ctx, u.ID, "Org "+email)
	if err != nil {
		t.Fatal(err)
	}
	login, err := s.Login(ctx, email, "correct horse battery", "test", "127.0.0.1")
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
	if _, err := s.ConnectChannel(ctx, owner, core.ConnectInput{BrandID: b.ID, Provider: platform.Sandbox, Fields: map[string]string{"emulates": "bluesky"}}); err != nil {
		t.Fatal(err)
	}
	dash, err := web.New(s, log, web.Config{})
	if err != nil {
		t.Fatal(err)
	}
	send := func(r *http.Request) *httptest.ResponseRecorder {
		r.AddCookie(&http.Cookie{Name: "araldo_session", Value: login.Token})
		rec := httptest.NewRecorder()
		dash.ServeHTTP(rec, r)
		return rec
	}
	post := func(fields [][2]string, file []byte) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("csrf", login.Session.CSRFToken)
		for _, f := range fields {
			_ = w.WriteField(f[0], f[1])
		}
		name := "build.png"
		if file == nil {
			name = "" // what browsers send for an empty file input
		}
		fw, _ := w.CreateFormFile("images", name)
		_, _ = fw.Write(file)
		_ = w.Close()
		r := httptest.NewRequest(http.MethodPost, "/posts", &buf)
		r.Header.Set("Content-Type", w.FormDataContentType())
		return send(r)
	}
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 64, 48))); err != nil {
		t.Fatal(err)
	}
	form := [][2]string{{"mode", "content"}, {"brand", id.Format(id.Brand, b.ID)}, {"body", "New build"}, {"fit", "error"}, {"publish", "now"}}

	rec := post(append(form, [2]string{"action", "preview"}, [2]string{"new_alt", "Front"}), img.Bytes())
	page := rec.Body.String()
	m := regexp.MustCompile(`name="media" value="(media_[0-9a-z]+)"`).FindStringSubmatch(page)
	if rec.Code != http.StatusOK || m == nil || !strings.Contains(page, `src="/media/`+m[1]+`"`) || !strings.Contains(page, `value="Front"`) {
		t.Fatalf("preview: %d, image kept: %v\n%s", rec.Code, m != nil, page)
	}
	mediaRef := m[1]

	rec = send(httptest.NewRequest(http.MethodGet, "/media/"+mediaRef, nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), img.Bytes()) {
		t.Fatalf("GET /media: %d %q, %d bytes", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}

	rec = post(append(form, [2]string{"action", "create"}, [2]string{"media", mediaRef}, [2]string{"alt_" + mediaRef, "Front view"}), nil)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/posts/post_") {
		t.Fatalf("create: %d %q\n%s", rec.Code, loc, rec.Body)
	}
	pid, err := id.Parse(id.Post, strings.SplitN(strings.TrimPrefix(loc, "/posts/"), "?", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Post(ctx, owner, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Media) != 1 || id.Format(id.Media, p.Media[0].ID) != mediaRef || p.Media[0].Alt != "Front view" {
		t.Fatalf("post media %+v", p.Media)
	}
	rec = send(httptest.NewRequest(http.MethodGet, loc, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `src="/media/`+mediaRef+`"`) {
		t.Fatalf("post page: %d, shows the image: %v", rec.Code, strings.Contains(rec.Body.String(), mediaRef))
	}
}
