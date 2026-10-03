// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"bufio"
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/blob"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/media"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Video (ADR 0027): streamed into S3-compatible storage, never held in
// memory or Postgres, and read from its index without transcoding.

// DefaultMaxVideoBytes is the largest video an install accepts unless
// configured otherwise.
const DefaultMaxVideoBytes = 1 << 30

// videoFetchTimeout bounds fetching a video from a URL.
const videoFetchTimeout = 15 * time.Minute

// VideoBlobs is storage that can take video: S3-compatible storage, which
// streams uploads and reads ranges.
type VideoBlobs interface {
	PutStream(ctx context.Context, key string, r io.Reader, contentType string, maxBytes int64) (int64, []byte, error)
	GetRange(ctx context.Context, key string, off, n int64) ([]byte, error)
}

func (s *Service) maxVideoBytes() int64 {
	if s.cfg.MaxVideoBytes > 0 {
		return s.cfg.MaxVideoBytes
	}
	return DefaultMaxVideoBytes
}

func (s *Service) videoBlobs() (VideoBlobs, error) {
	vb, ok := s.blobs.(VideoBlobs)
	if !ok || s.blobs == nil {
		return nil, apperr.Invalid("video_needs_s3", "file",
			"This install cannot store video: it needs S3-compatible storage (ARALDO_S3_BUCKET).")
	}
	return vb, nil
}

// StagedVideo is a video in storage that is not media yet: its brand and
// alt text may arrive after the file.
type StagedVideo struct {
	id       uuid.UUID
	orgID    uuid.UUID
	key      string
	size     int64
	sha256   []byte
	info     media.Video
	filename string
}

// Info is what the video's index says.
func (st *StagedVideo) Info() media.Video { return st.info }

// IsVideoStart reports whether a file's first bytes are a video's.
func IsVideoStart(head []byte) bool {
	_, ok := media.SniffVideo(head)
	return ok
}

// StageVideo streams a video into storage and reads its index. The caller
// makes it media with CreateVideo or throws it away with DiscardVideo.
func (s *Service) StageVideo(ctx context.Context, a Actor, r io.Reader, filename string) (*StagedVideo, error) {
	if err := a.require(PermPostsWrite); err != nil {
		return nil, err
	}
	vb, err := s.videoBlobs()
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(r)
	head, _ := br.Peek(12)
	typ, ok := media.SniffVideo(head)
	if !ok {
		return nil, apperr.Invalid("media_type_unsupported", "file", "Send an MP4 or QuickTime video.")
	}
	ext := ".mp4"
	if typ == media.QuickTime {
		ext = ".mov"
	}
	st := &StagedVideo{id: id.New(), orgID: a.OrgID, filename: cleanFilename(filename)}
	st.key = id.Format(id.Org, a.OrgID) + "/" + id.Format(id.Media, st.id) + ext
	st.size, st.sha256, err = vb.PutStream(ctx, st.key, br, typ, s.maxVideoBytes())
	switch {
	case errors.Is(err, blob.ErrTooLarge):
		return nil, apperr.Invalid("media_too_large", "file", "Videos are limited to %d MiB on this install.", s.maxVideoBytes()>>20)
	case err != nil:
		return nil, err
	}
	info, err := media.ProbeVideo(objectReader{ctx: ctx, b: vb, key: st.key, size: st.size}, st.size)
	if err != nil {
		s.DiscardVideo(ctx, st)
		return nil, apperr.Invalid("media_unreadable", "file", "The video cannot be read: %v. Export it as an MP4 (H.264 and AAC).", err)
	}
	st.info = info
	return st, nil
}

// CreateVideo makes a staged video one of a brand's media.
func (s *Service) CreateVideo(ctx context.Context, a Actor, st *StagedVideo, brandID uuid.UUID, alt string) (*model.Media, error) {
	b, err := s.Brand(ctx, a, brandID)
	if err != nil {
		return nil, err
	}
	if len([]rune(alt)) > media.MaxAlt {
		return nil, apperr.Invalid("alt_too_long", "alt", "Alt text is at most %d characters.", media.MaxAlt)
	}
	v := st.info
	m := &model.Media{ID: st.id, OrgID: a.OrgID, BrandID: b.ID, Livemode: a.Livemode, ContentType: v.Type, Size: st.size,
		Width: v.Width, Height: v.Height, SHA256: st.sha256, Alt: alt, Filename: st.filename, Storage: model.StorageS3, StorageKey: st.key,
		DurationMS: v.Duration.Milliseconds(), FrameRate: v.FrameRate, VideoCodec: v.VideoCodec, AudioCodec: v.AudioCodec,
		CreatedByUser: a.UserID, CreatedByKey: a.KeyID}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateMedia(ctx, m, nil); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "media.create", id.Format(id.Media, m.ID), map[string]any{"bytes": m.Size, "type": m.ContentType})
	})
	if err != nil {
		return nil, err
	}
	return s.store.Media(ctx, a.OrgID, m.ID)
}

// DiscardVideo deletes a staged video that will not become media.
func (s *Service) DiscardVideo(ctx context.Context, st *StagedVideo) {
	if st == nil {
		return
	}
	s.deleteBlob(context.WithoutCancel(ctx), &model.Media{ID: st.id, Storage: model.StorageS3, StorageKey: st.key})
}

// objectReader reads a stored object by ranges.
type objectReader struct {
	ctx  context.Context
	b    VideoBlobs
	key  string
	size int64
}

func (o objectReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= o.size {
		return 0, io.EOF
	}
	data, err := o.b.GetRange(o.ctx, o.key, off, int64(len(p)))
	n := copy(p, data)
	if err != nil {
		return n, err
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
