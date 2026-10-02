// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
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
	var (
		m   *model.Media
		err error
	)
	if ct == "multipart/form-data" {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
		var up upload
		if up, err = readUpload(r); err != nil {
			return err
		}
		b, err := h.svc.ResolveBrand(r.Context(), a, up.brand)
		if err != nil {
			return err
		}
		m, err = h.svc.CreateMedia(r.Context(), a, core.MediaInput{BrandID: b.ID, Data: up.data, Filename: up.filename, Alt: up.alt})
		if err != nil {
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
	data                 []byte
}

// readUpload reads an upload form part by part, in memory: the image in
// "file", and the fields "brand" and "alt". Nothing goes to temporary
// files (the container's file system is read-only).
func readUpload(r *http.Request) (upload, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return upload{}, badRequest("form_invalid", "", "The multipart form could not be read: %v", err)
	}
	var up upload
	gotFile := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return upload{}, formError(err)
		}
		name := part.FormName()
		switch name {
		case "file":
			if gotFile {
				return upload{}, badRequest("parameter_invalid", "file", "Send one file per request.")
			}
			up.data, err = io.ReadAll(io.LimitReader(part, media.MaxBytes+1))
			up.filename, gotFile = part.FileName(), true
		case "brand", "alt":
			var v []byte
			v, err = io.ReadAll(io.LimitReader(part, maxFormField+1))
			if err == nil && len(v) > maxFormField {
				return upload{}, badRequest("parameter_invalid", name, "%s is too long.", name)
			}
			if name == "brand" {
				up.brand = string(v)
			} else {
				up.alt = string(v)
			}
		default:
			return upload{}, badRequest("parameter_unknown", name, "Unknown parameter %q.", name)
		}
		_ = part.Close()
		if err != nil {
			return upload{}, formError(err)
		}
	}
	if !gotFile {
		return upload{}, badRequest("file_missing", "file", "Send the image in a part named file.")
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
	m, rc, err := h.svc.LinkedMedia(r.Context(), r.PathValue("id"), q.Get("expires"), q.Get("signature"))
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
