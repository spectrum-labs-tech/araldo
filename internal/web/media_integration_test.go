// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// multipartPost sends a multipart form, with file (if any) in fileField,
// as the signed-in owner.
func multipartPost(t *testing.T, d *dash, path string, fields [][2]string, fileField string, file []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("csrf", d.login.Session.CSRFToken)
	for _, f := range fields {
		_ = w.WriteField(f[0], f[1])
	}
	if fileField != "" {
		name := "logo.png"
		if file == nil {
			name = "" // what browsers send for an empty file input
		}
		fw, _ := w.CreateFormFile(fileField, name)
		_, _ = fw.Write(file)
	}
	_ = w.Close()
	r := httptest.NewRequest(http.MethodPost, path, &buf)
	r.Header.Set("Content-Type", w.FormDataContentType())
	return d.send(r)
}

// The media library lists each brand's media, takes uploads that it keeps,
// edits and deletes them, and refuses to delete what something uses.
func TestMediaLibrary(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	get := func(path string) string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	brand := id.Format(id.Brand, d.brand.ID)

	if page := get("/media"); !strings.Contains(page, `href="/media" class="active" aria-current="page"`) || !strings.Contains(page, `href="/media/new"`) ||
		!strings.Contains(page, "No media yet.") {
		t.Fatalf("the empty library:\n%s", page)
	}
	if page := get("/media/new?brand=" + brand); !strings.Contains(page, `enctype="multipart/form-data"`) || !strings.Contains(page, `value="`+brand+`" selected`) {
		t.Fatalf("the upload form:\n%s", page)
	}

	// Nothing chosen: the form again, with what was typed.
	rec := multipartPost(t, d, "/media", [][2]string{{"brand", brand}, {"alt", "The shop front"}}, "files", nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Choose a file") || !strings.Contains(rec.Body.String(), `value="The shop front"`) {
		t.Fatalf("an upload with nothing in it: %d\n%s", rec.Code, rec.Body)
	}
	rec = multipartPost(t, d, "/media", [][2]string{{"brand", brand}, {"alt", "The shop front"}}, "files", pngBytes(t, 30, 20))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/media?brand="+url.QueryEscape(brand) {
		t.Fatalf("upload: %d %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	list, _, err := d.s.MediaList(ctx, d.owner, core.MediaFilter{BrandID: &d.brand.ID}, core.Page{})
	if err != nil || len(list) != 1 || !list[0].Kept || list[0].Alt != "The shop front" || list[0].Filename != "logo.png" {
		t.Fatalf("after uploading: %+v, %v", list, err)
	}
	m := list[0]
	ref := id.Format(id.Media, m.ID)
	page := get("/media?brand=" + brand)
	if !strings.Contains(page, `href="/media/`+ref+`/edit"`) || !strings.Contains(page, `alt="The shop front"`) || !strings.Contains(page, ">kept<") {
		t.Fatalf("the library after uploading:\n%s", page)
	}

	page = get("/media/" + ref + "/edit")
	if !strings.Contains(page, "![The shop front]("+ref+")") || !strings.Contains(page, "nothing yet") || !strings.Contains(page, `action="/media/`+ref+`/delete"`) {
		t.Fatalf("the media page:\n%s", page)
	}
	form := url.Values{"csrf": {d.login.Session.CSRFToken}, "alt": {"The shop, at night"}} // kept unticked
	r := httptest.NewRequest(http.MethodPost, "/media/"+ref, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := d.send(r); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving: %d\n%s", rec.Code, rec.Body)
	}
	if got, err := d.s.Media(ctx, d.owner, m.ID); err != nil || got.Alt != "The shop, at night" || got.Kept {
		t.Fatalf("after saving: %+v, %v", got, err)
	}

	// Used by a post, it says so and cannot be deleted.
	if _, err := d.s.CreatePost(ctx, d.owner, core.PostInput{BrandID: d.brand.ID, Content: &model.Content{Body: "Open late"}, Media: []uuid.UUID{m.ID}}); err != nil {
		t.Fatal(err)
	}
	if page := get("/media/" + ref + "/edit"); !strings.Contains(page, "1 post") || strings.Contains(page, `action="/media/`+ref+`/delete"`) {
		t.Fatalf("the page of media a post uses:\n%s", page)
	}
	del := func(ref string) *httptest.ResponseRecorder {
		form := url.Values{"csrf": {d.login.Session.CSRFToken}}
		r := httptest.NewRequest(http.MethodPost, "/media/"+ref+"/delete", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return d.send(r)
	}
	if rec := del(ref); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "uses this media") {
		t.Fatalf("deleting used media: %d\n%s", rec.Code, rec.Body)
	}

	// Unused media is deleted.
	loose, err := d.s.CreateMedia(ctx, d.owner, core.MediaInput{BrandID: d.brand.ID, Data: pngBytes(t, 10, 10), Keep: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec := del(id.Format(id.Media, loose.ID)); rec.Code != http.StatusSeeOther || noticeOf(rec) == "" {
		t.Fatalf("deleting unused media: %d\n%s", rec.Code, rec.Body)
	}
	if rec := d.send(httptest.NewRequest(http.MethodGet, "/media/"+id.Format(id.Media, loose.ID)+"/edit", nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted media's page: %d", rec.Code)
	}
}

// A logo uploaded on the brand's email theme goes to the library, kept,
// and is chosen as the logo.
func TestEmailThemeLogoUpload(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	brandPath := "/brands/" + id.Format(id.Brand, d.brand.ID)
	page := d.send(httptest.NewRequest(http.MethodGet, brandPath, nil)).Body.String()
	if !strings.Contains(page, `name="logo_file"`) || !strings.Contains(page, `action="`+brandPath+`/email_theme" enctype="multipart/form-data"`) {
		t.Fatalf("the theme form takes no upload:\n%s", page)
	}

	// With an accent that is not a color the theme is refused; the logo is
	// in the library and chosen on the form shown again.
	rec := multipartPost(t, d, brandPath+"/email_theme", [][2]string{{"logo", ""}, {"accent", "green"}, {"postal_address", "1 Main St"}}, "logo_file",
		pngBytes(t, 120, 40))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a theme with a bad accent: %d\n%s", rec.Code, rec.Body)
	}
	list, _, err := d.s.MediaList(ctx, d.owner, core.MediaFilter{BrandID: &d.brand.ID}, core.Page{})
	if err != nil || len(list) != 1 || !list[0].Kept || list[0].Alt != d.brand.Name {
		t.Fatalf("the uploaded logo: %+v, %v", list, err)
	}
	logo := id.Format(id.Media, list[0].ID)
	if !regexp.MustCompile(`value="` + logo + `" selected`).MatchString(rec.Body.String()) {
		t.Fatalf("the form shown again does not have the new logo chosen:\n%s", rec.Body)
	}

	rec = multipartPost(t, d, brandPath+"/email_theme", [][2]string{{"logo", logo}, {"accent", "#0f5132"}, {"postal_address", "1 Main St"}}, "logo_file", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("saving the theme: %d\n%s", rec.Code, rec.Body)
	}
	b, err := d.s.Brand(ctx, d.owner, d.brand.ID)
	if err != nil || b.EmailTheme.LogoMediaID == nil || *b.EmailTheme.LogoMediaID != list[0].ID {
		t.Fatalf("the brand's logo: %+v, %v", b.EmailTheme, err)
	}
	if list, _, _ := d.s.MediaList(ctx, d.owner, core.MediaFilter{BrandID: &d.brand.ID}, core.Page{}); len(list) != 1 {
		t.Fatalf("an empty file input stored %d media", len(list))
	}
}
