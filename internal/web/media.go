// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"mime/multipart"
	"net/url"
	"strings"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// The media library (ADR 0017): each brand's images and videos, uploaded
// here to be kept, or for posts, which keep theirs while they use them.

const mediaPageSize = 48

// mediaLibraryData is the library: one page of media, filtered by brand.
type mediaLibraryData struct {
	Items    []*model.Media
	Brands   []*model.Brand
	Brand    string
	CanWrite bool
	// BrandNames names each item's brand.
	BrandNames map[string]string
	// Older links the next page.
	Older string
}

func (s *Server) mediaLibrary(c *reqCtx) error {
	q := c.r.URL.Query()
	d := &mediaLibraryData{Brand: q.Get("brand"), CanWrite: c.actor.Can(core.PermPostsWrite), BrandNames: map[string]string{}}
	var err error
	if d.Brands, err = s.svc.Brands(c.ctx(), c.actor); err != nil {
		return err
	}
	for _, b := range d.Brands {
		d.BrandNames[id.Format(id.Brand, b.ID)] = b.Name
	}
	var f core.MediaFilter
	if d.Brand != "" {
		b, err := s.svc.ResolveBrand(c.ctx(), c.actor, d.Brand)
		if err != nil {
			return err
		}
		f.BrandID = &b.ID
	}
	pg := core.Page{Limit: mediaPageSize}
	if u, err := id.Parse(id.Media, q.Get("starting_after")); err == nil {
		pg.StartingAfter = u
	}
	items, more, err := s.svc.MediaList(c.ctx(), c.actor, f, pg)
	if err != nil {
		return err
	}
	d.Items = items
	if more && len(items) > 0 {
		v := url.Values{"starting_after": {id.Format(id.Media, items[len(items)-1].ID)}}
		if d.Brand != "" {
			v.Set("brand", d.Brand)
		}
		d.Older = "/media?" + v.Encode()
	}
	return s.page(c, "media", "media", "Media", d)
}

// mediaNewData is the upload form.
type mediaNewData struct {
	Brands []*model.Brand
	// Form is what was typed, kept when it is shown again with problems.
	Form     url.Values
	MaxVideo int64
	// Added counts files already stored when the link after them failed.
	Added int
}

func (s *Server) newMedia(c *reqCtx) error {
	d, err := s.mediaNewData(c, url.Values{"brand": {c.r.URL.Query().Get("brand")}})
	if err != nil {
		return err
	}
	return s.page(c, "media_new", "media", "Add media", d)
}

func (s *Server) mediaNewData(c *reqCtx, form url.Values) (*mediaNewData, error) {
	d := &mediaNewData{Form: form, MaxVideo: s.svc.MaxVideoBytes()}
	var err error
	d.Brands, err = s.svc.Brands(c.ctx(), c.actor)
	return d, err
}

// uploadMedia stores the form's files and link, kept in the library, and
// returns to the library.
func (s *Server) uploadMedia(c *reqCtx) error {
	f := c.r.PostForm
	form := url.Values{"brand": {f.Get("brand")}, "url": {f.Get("url")}, "alt": {f.Get("alt")}}
	d, err := s.mediaNewData(c, form)
	if err != nil {
		return err
	}
	fail := func(err error) error { return s.formErr(c, "media_new", "media", "Add media", d, err) }
	if f.Get("brand") == "" {
		return fail(apperr.Invalid("brand_required", "brand", "Choose the brand the media is for."))
	}
	b, err := s.svc.ResolveBrand(c.ctx(), c.actor, f.Get("brand"))
	if err != nil {
		return fail(err)
	}
	alt := f.Get("alt")
	added := 0
	if c.r.MultipartForm != nil {
		for _, fh := range c.r.MultipartForm.File["files"] {
			if fh.Size == 0 {
				continue // the file input, left empty
			}
			data, err := readFile(fh)
			if err != nil {
				return fail(err)
			}
			if _, err := s.svc.CreateMedia(c.ctx(), c.actor, core.MediaInput{BrandID: b.ID, Data: data, Filename: fh.Filename, Alt: alt, Keep: true}); err != nil {
				return fail(err)
			}
			added++
		}
	}
	if ref := strings.TrimSpace(f.Get("url")); ref != "" {
		if _, err := s.svc.ImportMedia(c.ctx(), c.actor, b.ID, ref, alt, true); err != nil {
			d.Added = added // the files are in; only the link is left to fix
			return fail(err)
		}
		added++
	}
	if added == 0 {
		return fail(apperr.Invalid("file_missing", "files", "Choose a file to upload, or give a link to fetch one from."))
	}
	notice := "Added 1 item to the library."
	if added > 1 {
		notice = fmt.Sprintf("Added %d items to the library.", added)
	}
	return redirect(c, "/media?brand="+url.QueryEscape(id.Format(id.Brand, b.ID)), notice)
}

// mediaDetailData is one item: its file, what uses it, and its forms.
type mediaDetailData struct {
	Media     *model.Media
	BrandName string
	Uses      core.MediaUses
	CanWrite  bool
	// Alt is the alt text form's value, kept when it is shown again.
	Alt string
}

// UsedBy says what uses the media, in words ("1 post, 2 email themes").
func (d *mediaDetailData) UsedBy() string {
	var parts []string
	add := func(n int, one, many string) {
		switch {
		case n == 1:
			parts = append(parts, "1 "+one)
		case n > 1:
			parts = append(parts, fmt.Sprintf("%d %s", n, many))
		}
	}
	add(d.Uses.Posts, "post", "posts")
	add(d.Uses.Issues, "newsletter issue", "newsletter issues")
	add(d.Uses.Logos, "email theme", "email themes")
	if len(parts) == 0 {
		return "nothing yet"
	}
	return strings.Join(parts, ", ")
}

func (s *Server) mediaDetailData(c *reqCtx) (*mediaDetailData, error) {
	mid, err := pathUUID(c, id.Media, "media")
	if err != nil {
		return nil, err
	}
	m, err := s.svc.Media(c.ctx(), c.actor, mid)
	if err != nil {
		return nil, err
	}
	d := &mediaDetailData{Media: m, Alt: m.Alt, CanWrite: c.actor.Can(core.PermPostsWrite)}
	if b, err := s.svc.Brand(c.ctx(), c.actor, m.BrandID); err == nil {
		d.BrandName = b.Name
	}
	if d.Uses, err = s.svc.MediaUsage(c.ctx(), c.actor, m.ID); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Server) mediaDetail(c *reqCtx) error {
	d, err := s.mediaDetailData(c)
	if err != nil {
		return err
	}
	return s.page(c, "media_detail", "media", mediaTitle(d.Media), d)
}

// mediaTitle names media for a page title: its file name, else its ID.
func mediaTitle(m *model.Media) string {
	if m.Filename != "" {
		return m.Filename
	}
	return id.Format(id.Media, m.ID)
}

// saveMedia changes the alt text and whether the library keeps it.
func (s *Server) saveMedia(c *reqCtx) error {
	d, err := s.mediaDetailData(c)
	if err != nil {
		return err
	}
	alt, kept := c.r.PostFormValue("alt"), c.r.PostFormValue("kept") == "on"
	if _, err := s.svc.UpdateMedia(c.ctx(), c.actor, d.Media.ID, core.MediaUpdate{Alt: &alt, Kept: &kept}); err != nil {
		d.Alt = alt
		return s.formErr(c, "media_detail", "media", mediaTitle(d.Media), d, err)
	}
	return redirect(c, "/media/"+id.Format(id.Media, d.Media.ID)+"/edit", "Saved.")
}

// deleteMediaItem deletes media nothing uses.
func (s *Server) deleteMediaItem(c *reqCtx) error {
	d, err := s.mediaDetailData(c)
	if err != nil {
		return err
	}
	if err := s.svc.DeleteMedia(c.ctx(), c.actor, d.Media.ID); err != nil {
		return s.formErr(c, "media_detail", "media", mediaTitle(d.Media), d, err)
	}
	return redirect(c, "/media?brand="+url.QueryEscape(id.Format(id.Brand, d.Media.BrandID)), "Deleted "+mediaTitle(d.Media)+".")
}

// uploadedFile is the form's file named name, or nil when the input was
// left empty or the form was not an upload.
func uploadedFile(c *reqCtx, name string) *multipart.FileHeader {
	if c.r.MultipartForm == nil {
		return nil
	}
	for _, fh := range c.r.MultipartForm.File[name] {
		if fh.Size > 0 {
			return fh
		}
	}
	return nil
}
