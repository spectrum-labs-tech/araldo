// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/media"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Media (ADR 0017).
const (
	// MaxPostMedia is the most media one post can carry (the most any
	// platform takes).
	MaxPostMedia = 10
	// UnusedMediaTTL is how long media no post uses is kept.
	UnusedMediaTTL    = 24 * time.Hour
	mediaFetchTimeout = 30 * time.Second
	maxFilename       = 200
)

// Blobs stores media files outside Postgres (ADR 0017); *blob.S3 is one.
type Blobs interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// mediaClient fetches media by URL: refusing private addresses unless
// allowed, and following up to five redirects, each checked again.
func mediaClient(allowPrivate bool, base *http.Client) *http.Client {
	c := *base
	c.Timeout = mediaFetchTimeout
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("redirect to %s", req.URL.Scheme)
		}
		return nil
	}
	return &c
}

// MediaInput creates media from a file.
type MediaInput struct {
	BrandID  uuid.UUID
	Data     []byte
	Filename string
	Alt      string
}

// CreateMedia stores an image for posts to use (ADR 0017).
func (s *Service) CreateMedia(ctx context.Context, a Actor, in MediaInput) (*model.Media, error) {
	if err := a.require(PermPostsWrite); err != nil {
		return nil, err
	}
	b, err := s.Brand(ctx, a, in.BrandID)
	if err != nil {
		return nil, err
	}
	var ps apperr.Problems
	switch {
	case len(in.Data) == 0:
		ps.Add("media_empty", "file", "The file is empty.")
	case len(in.Data) > media.MaxBytes:
		ps.Add("media_too_large", "file", "Images are limited to %d MiB.", media.MaxBytes>>20)
	}
	alt := strings.TrimSpace(in.Alt)
	if utf8.RuneCountInString(alt) > media.MaxAlt {
		ps.Add("alt_too_long", "alt", "Alt text is limited to %d characters.", media.MaxAlt)
	}
	if err := ps.Err("The file cannot be used."); err != nil {
		return nil, err
	}
	info, err := media.Inspect(in.Data)
	if err != nil {
		return nil, apperr.Invalid("media_type_unsupported", "file", "Send a JPEG, PNG, GIF or WebP image (%v).", err)
	}
	sum := sha256.Sum256(in.Data)
	m := &model.Media{ID: id.New(), OrgID: a.OrgID, BrandID: b.ID, Livemode: a.Livemode, ContentType: info.Type, Size: int64(len(in.Data)),
		Width: info.Width, Height: info.Height, SHA256: sum[:], Alt: alt, Filename: cleanFilename(in.Filename),
		Storage: model.StoragePostgres, CreatedByUser: a.UserID, CreatedByKey: a.KeyID}
	if s.blobs != nil {
		m.Storage = model.StorageS3
		m.StorageKey = id.Format(id.Org, a.OrgID) + "/" + id.Format(id.Media, m.ID) + media.Extension(info.Type)
		if err := s.blobs.Put(ctx, m.StorageKey, in.Data, info.Type); err != nil {
			return nil, fmt.Errorf("storing media: %w", err)
		}
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateMedia(ctx, m, in.Data); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "media.create", id.Format(id.Media, m.ID), map[string]any{"bytes": m.Size, "type": m.ContentType})
	})
	if err != nil {
		s.deleteBlob(ctx, m)
		return nil, err
	}
	stored, err := s.store.Media(ctx, a.OrgID, m.ID)
	return stored, err
}

// ImportMedia fetches an image from a public URL and stores it.
func (s *Service) ImportMedia(ctx context.Context, a Actor, brandID uuid.UUID, rawURL, alt string) (*model.Media, error) {
	if err := a.require(PermPostsWrite); err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, apperr.Invalid("url_invalid", "url", "Give an http or https URL.")
	}
	data, err := s.fetch(ctx, u.String())
	if err != nil {
		return nil, err
	}
	return s.CreateMedia(ctx, a, MediaInput{BrandID: brandID, Data: data, Filename: path.Base(u.Path), Alt: alt})
}

func (s *Service) fetch(ctx context.Context, rawURL string) ([]byte, error) {
	fail := func(format string, args ...any) error {
		return apperr.Invalid("url_unreachable", "url", "Could not fetch the image: "+format, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, mediaFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fail("%v.", err)
	}
	req.Header.Set("User-Agent", "Araldo (+https://github.com/spectrum-labs-tech/araldo)")
	req.Header.Set("Accept", strings.Join(media.Types, ", "))
	resp, err := s.MediaHTTP.Do(req) //nolint:gosec // G704: the client refuses private addresses unless the operator allows them
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL is already in the message's context
		}
		return nil, fail("%v.", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fail("the server answered HTTP %d.", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, media.MaxBytes+1))
	if err != nil {
		return nil, fail("%v.", err)
	}
	if len(data) > media.MaxBytes {
		return nil, apperr.Invalid("media_too_large", "url", "Images are limited to %d MiB.", media.MaxBytes>>20)
	}
	return data, nil
}

// cleanFilename keeps the last path element of a client's file name, for
// display only.
func cleanFilename(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "." || name == ".." {
		return ""
	}
	if utf8.RuneCountInString(name) > maxFilename {
		name = string([]rune(name)[:maxFilename])
	}
	return name
}

// Media returns one of the actor's media items.
func (s *Service) Media(ctx context.Context, a Actor, mediaID uuid.UUID) (*model.Media, error) {
	if err := a.require(PermPostsRead); err != nil && !a.Can(PermPostsWrite) {
		return nil, err
	}
	return s.visibleMedia(ctx, a, mediaID)
}

func (s *Service) visibleMedia(ctx context.Context, a Actor, mediaID uuid.UUID) (*model.Media, error) {
	m, err := s.store.Media(ctx, a.OrgID, mediaID)
	if err != nil {
		return nil, notFound(err, "media")
	}
	if m.Livemode != a.Livemode || a.brandAllowed(m.BrandID) != nil {
		return nil, apperr.NotFound("media")
	}
	return m, nil
}

// MediaFilter narrows a listing.
type MediaFilter = store.MediaFilter

// MediaList lists media newest first.
func (s *Service) MediaList(ctx context.Context, a Actor, f MediaFilter, page store.Page) ([]*model.Media, bool, error) {
	if err := a.require(PermPostsRead); err != nil && !a.Can(PermPostsWrite) {
		return nil, false, err
	}
	if a.BrandID != nil {
		f.BrandID = a.BrandID
	}
	return s.store.MediaList(ctx, a.OrgID, a.Livemode, f, page)
}

// UpdateMediaAlt changes a media item's alt text; the file never changes.
func (s *Service) UpdateMediaAlt(ctx context.Context, a Actor, mediaID uuid.UUID, alt string) (*model.Media, error) {
	if err := a.require(PermPostsWrite); err != nil {
		return nil, err
	}
	alt = strings.TrimSpace(alt)
	if utf8.RuneCountInString(alt) > media.MaxAlt {
		return nil, apperr.Invalid("alt_too_long", "alt", "Alt text is limited to %d characters.", media.MaxAlt)
	}
	m, err := s.visibleMedia(ctx, a, mediaID)
	if err != nil {
		return nil, err
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SetMediaAlt(ctx, a.OrgID, m.ID, alt); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "media.update", id.Format(id.Media, m.ID), nil)
	})
	if err != nil {
		return nil, err
	}
	m.Alt = alt
	return m, nil
}

// DeleteMedia deletes media no post uses.
func (s *Service) DeleteMedia(ctx context.Context, a Actor, mediaID uuid.UUID) error {
	if err := a.require(PermPostsWrite); err != nil {
		return err
	}
	m, err := s.visibleMedia(ctx, a, mediaID)
	if err != nil {
		return err
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteMedia(ctx, a.OrgID, m.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "media.delete", id.Format(id.Media, m.ID), nil)
	})
	if errors.Is(err, store.ErrReferenced) {
		return apperr.Conflict("media_in_use", "A post uses this media, so it is kept as long as the post is.")
	}
	if err != nil {
		return notFound(err, "media")
	}
	s.deleteBlob(ctx, m)
	return nil
}

// MediaContent opens a media item's file, for the dashboard to show.
func (s *Service) MediaContent(ctx context.Context, a Actor, mediaID uuid.UUID) (*model.Media, io.ReadCloser, error) {
	m, err := s.Media(ctx, a, mediaID)
	if err != nil {
		return nil, nil, err
	}
	rc, err := s.openMedia(ctx, m)
	return m, rc, err
}

// openMedia opens the file wherever it is stored.
func (s *Service) openMedia(ctx context.Context, m *model.Media) (io.ReadCloser, error) {
	switch m.Storage {
	case model.StoragePostgres:
		data, err := s.store.MediaBlob(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	case model.StorageS3:
		if s.blobs == nil {
			return nil, fmt.Errorf("media %s is in S3, which is not configured", id.Format(id.Media, m.ID))
		}
		return s.blobs.Get(ctx, m.StorageKey)
	}
	return nil, fmt.Errorf("media %s: unknown storage %q", id.Format(id.Media, m.ID), m.Storage)
}

// deleteBlob removes a file stored outside Postgres; a failure leaves an
// orphan file, which is logged rather than undoing the delete.
func (s *Service) deleteBlob(ctx context.Context, m *model.Media) {
	if m.Storage != model.StorageS3 || s.blobs == nil {
		return
	}
	if err := s.blobs.Delete(context.WithoutCancel(ctx), m.StorageKey); err != nil {
		s.log.WarnContext(ctx, "deleting a media file failed", "media", id.Format(id.Media, m.ID), "err", err)
	}
}

// PruneUnusedMedia deletes media that no post has used for a day.
func (s *Service) PruneUnusedMedia(ctx context.Context) (int, error) {
	return s.pruneUnusedMedia(ctx, nil)
}

// pruneUnusedMedia prunes one org's media, or (orgID nil) every org's.
func (s *Service) pruneUnusedMedia(ctx context.Context, orgID *uuid.UUID) (int, error) {
	unused, err := s.store.UnusedMedia(ctx, orgID, s.Now().Add(-UnusedMediaTTL), 100)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range unused {
		err := s.store.DeleteMedia(ctx, m.OrgID, m.ID)
		if errors.Is(err, store.ErrReferenced) || errors.Is(err, store.ErrNotFound) {
			continue // a post took it, or someone deleted it, meanwhile
		}
		if err != nil {
			return n, err
		}
		s.deleteBlob(ctx, m)
		n++
	}
	return n, nil
}

// postMedia resolves a post's media IDs: each must exist in the actor's
// mode and belong to the post's brand.
func (s *Service) postMedia(ctx context.Context, a Actor, brandID uuid.UUID, ids []uuid.UUID, ps *apperr.Problems) []*model.Media {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > MaxPostMedia {
		ps.Add("too_much_media", "media", "A post carries at most %d media items.", MaxPostMedia)
		return nil
	}
	found, err := s.store.MediaByIDs(ctx, a.OrgID, ids)
	if err != nil {
		ps.Add("media_missing", "media", "Could not read the media.")
		return nil
	}
	byID := map[uuid.UUID]*model.Media{}
	for _, m := range found {
		byID[m.ID] = m
	}
	out := make([]*model.Media, 0, len(ids))
	seen := map[uuid.UUID]bool{}
	for i, mid := range ids {
		param := fmt.Sprintf("media[%d]", i)
		m := byID[mid]
		switch {
		case seen[mid]:
			ps.Add("media_duplicate", param, "Media %s is listed twice.", id.Format(id.Media, mid))
		case m == nil || m.Livemode != a.Livemode:
			ps.Add("media_missing", param, "No such media %s in this mode.", id.Format(id.Media, mid))
		case m.BrandID != brandID:
			ps.Add("media_other_brand", param, "Media %s belongs to another brand.", id.Format(id.Media, mid))
		default:
			out = append(out, m)
		}
		seen[mid] = true
	}
	return out
}

// ruleMedia describes media for rule checks.
func ruleMedia(ms []*model.Media) []platform.Media {
	out := make([]platform.Media, 0, len(ms))
	for _, m := range ms {
		out = append(out, platform.Media{Type: m.ContentType, Size: m.Size, Width: m.Width, Height: m.Height, Alt: m.Alt})
	}
	return out
}

// payloadMedia describes media for an adapter, opening each file when it
// is read.
func (s *Service) payloadMedia(ms []*model.Media) []platform.Media {
	out := ruleMedia(ms)
	for i, m := range ms {
		out[i].Open = func(ctx context.Context) (io.ReadCloser, error) { return s.openMedia(ctx, m) }
	}
	return out
}
