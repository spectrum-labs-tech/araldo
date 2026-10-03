// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bufio"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/media"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Media (ADR 0017).

// maxUploadBody bounds a media upload: the largest image plus the form
// around it.
const maxUploadBody = media.MaxBytes + 64<<10

// maxFormField bounds the text fields of an upload form.
const maxFormField = 4 << 10

// bodyLimit is how large r's body may be.
func bodyLimit(r *http.Request) int64 {
	if r.Method == http.MethodPost && r.URL.Path == "/v1/media" {
		return maxUploadBody
	}
	return maxBody
}

func (h *Handler) createMedia(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var m *model.Media
	if ct == "multipart/form-data" {
		limit := int64(maxUploadBody)
		if v := h.svc.MaxVideoBytes(); v+64<<10 > limit {
			limit = v + 64<<10
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		up, err := h.readUpload(r, a)
		if err != nil {
			return err
		}
		if m, err = h.finishUpload(r, a, up); err != nil {
			h.svc.DiscardVideo(r.Context(), up.video)
			return err
		}
	} else {
		var body struct {
			Brand string `json:"brand"`
			URL   string `json:"url"`
			Alt   string `json:"alt"`
		}
		if err := decode(r, &body); err != nil {
			return err
		}
		if body.URL == "" {
			return badRequest("file_missing", "file", "Upload the image as multipart/form-data (a part named file), or send JSON with a url to fetch it from.")
		}
		b, err := h.svc.ResolveBrand(r.Context(), a, body.Brand)
		if err != nil {
			return err
		}
		if m, err = h.svc.ImportMedia(r.Context(), a, b.ID, body.URL, body.Alt); err != nil {
			return err
		}
	}
	ok(w, http.StatusCreated, core.ViewMedia(m))
	return nil
}

type upload struct {
	brand, alt, filename string
	// data is an image, read in memory; video is a video, streamed into
	// storage as it arrived (ADR 0027).
	data  []byte
	video *core.StagedVideo
}

// finishUpload makes an upload media, once its brand is known.
func (h *Handler) finishUpload(r *http.Request, a core.Actor, up upload) (*model.Media, error) {
	b, err := h.svc.ResolveBrand(r.Context(), a, up.brand)
	if err != nil {
		return nil, err
	}
	if up.video != nil {
		return h.svc.CreateVideo(r.Context(), a, up.video, b.ID, up.alt)
	}
	return h.svc.CreateMedia(r.Context(), a, core.MediaInput{BrandID: b.ID, Data: up.data, Filename: up.filename, Alt: up.alt})
}

// readUpload reads an upload form part by part: the file in "file" (an
// image in memory, a video streamed to storage), and the fields "brand"
// and "alt", in any order. Nothing goes to temporary files (the
// container's file system is read-only). On an error, a staged video is
// discarded.
func (h *Handler) readUpload(r *http.Request, a core.Actor) (up upload, err error) {
	ctx := r.Context()
	defer func() {
		if err != nil {
			h.svc.DiscardVideo(ctx, up.video)
		}
	}()
	mr, err := r.MultipartReader()
	if err != nil {
		return up, badRequest("form_invalid", "", "The multipart form could not be read: %v", err)
	}
	gotFile := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return up, formError(err)
		}
		name := part.FormName()
		switch name {
		case "file":
			if gotFile {
				return up, badRequest("parameter_invalid", "file", "Send one file per request.")
			}
			br := bufio.NewReader(part)
			if head, _ := br.Peek(12); core.IsVideoStart(head) {
				up.video, err = h.svc.StageVideo(ctx, a, br, part.FileName())
			} else {
				up.data, err = io.ReadAll(io.LimitReader(br, media.MaxBytes+1))
			}
			up.filename, gotFile = part.FileName(), true
		case "brand", "alt":
			var v []byte
			v, err = io.ReadAll(io.LimitReader(part, maxFormField+1))
			if err == nil && len(v) > maxFormField {
				return up, badRequest("parameter_invalid", name, "%s is too long.", name)
			}
			if name == "brand" {
				up.brand = string(v)
			} else {
				up.alt = string(v)
			}
		default:
			return up, badRequest("parameter_unknown", name, "Unknown parameter %q.", name)
		}
		_ = part.Close()
		if err != nil {
			return up, formError(err)
		}
	}
	if !gotFile {
		return up, badRequest("file_missing", "file", "Send the image in a part named file.")
	}
	return up, nil
}

func formError(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return badRequest("body_too_large", "", "Uploads are limited to %d MiB.", media.MaxBytes>>20)
	}
	return badRequest("form_invalid", "", "The multipart form could not be read: %v", err)
}

func (h *Handler) listMedia(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	pg, err := page(r, id.Media)
	if err != nil {
		return err
	}
	var f core.MediaFilter
	if ref := r.URL.Query().Get("brand"); ref != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, ref)
		if err != nil {
			return err
		}
		f.BrandID = &b.ID
	}
	items, more, err := h.svc.MediaList(r.Context(), a, f, pg)
	if err != nil {
		return err
	}
	out := make([]core.MediaView, 0, len(items))
	for _, m := range items {
		out = append(out, core.ViewMedia(m))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, HasMore: more, URL: "/v1/media"})
	return nil
}

func (h *Handler) getMedia(w http.ResponseWriter, r *http.Request) error {
	mid, err := pathID(r, id.Media, "media")
	if err != nil {
		return err
	}
	m, err := h.svc.Media(r.Context(), actor(r), mid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewMedia(m))
	return nil
}

func (h *Handler) updateMedia(w http.ResponseWriter, r *http.Request) error {
	mid, err := pathID(r, id.Media, "media")
	if err != nil {
		return err
	}
	var body struct {
		Alt *string `json:"alt"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Alt == nil {
		return badRequest("parameter_missing", "alt", "Send the new alt text; it is the only thing about media that can change.")
	}
	m, err := h.svc.UpdateMediaAlt(r.Context(), actor(r), mid, *body.Alt)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewMedia(m))
	return nil
}

func (h *Handler) deleteMedia(w http.ResponseWriter, r *http.Request) error {
	mid, err := pathID(r, id.Media, "media")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteMedia(r.Context(), actor(r), mid); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: id.Format(id.Media, mid), Object: "media", Deleted: true})
	return nil
}

// mediaContent serves a file by a signed link (ADR 0021): no API key, so
// a platform can fetch it.
func (h *Handler) mediaContent(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	m, rc, err := h.svc.LinkedMedia(r.Context(), r.PathValue("id"), q.Get("expires"), q.Get("signature"), q.Get("for"))
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = io.Copy(w, rc)
	return nil
}
