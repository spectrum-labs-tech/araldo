// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/blob"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// memBlobs (in media_integration_test.go) streams like S3 too.

func (m *memBlobs) PutStream(_ context.Context, key string, r io.Reader, _ string, maxBytes int64) (int64, []byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return 0, nil, err
	}
	if int64(len(data)) > maxBytes {
		return 0, nil, blob.ErrTooLarge
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	sum := sha256.Sum256(data)
	return int64(len(data)), sum[:], nil
}

func (m *memBlobs) GetRange(_ context.Context, key string, off, n int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, blob.ErrNotFound
	}
	if off >= int64(len(data)) {
		return nil, io.EOF
	}
	return append([]byte(nil), data[off:min(off+n, int64(len(data)))]...), nil
}

func withBlobs(b core.Blobs) option {
	return func(cfg *core.Config, _ *[]platform.Adapter) { cfg.Blobs = b }
}

// testMP4 is a minimal MP4: an index for one H.264 track of seconds at 30
// frames a second, then data.
func testMP4(w, h, seconds int) []byte {
	box := func(typ string, parts ...[]byte) []byte {
		body := bytes.Join(parts, nil)
		return append(append(binary.BigEndian.AppendUint32(nil, uint32(8+len(body))), typ...), body...)
	}
	u32 := func(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
	var matrix []byte
	for _, x := range []uint32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000} {
		matrix = append(matrix, u32(x)...)
	}
	dur := u32(uint32(seconds * 1000))
	tkhd := box("tkhd", u32(0), u32(0), u32(0), u32(1), u32(0), dur, make([]byte, 16), matrix, u32(uint32(w)<<16), u32(uint32(h)<<16))
	mdia := box("mdia", box("mdhd", u32(0), u32(0), u32(0), u32(1000), dur, u32(0)), box("hdlr", u32(0), u32(0), []byte("vide"), make([]byte, 12)),
		box("minf", box("stbl", box("stsd", u32(0), u32(1), box("avc1", make([]byte, 20))), box("stts", u32(0), u32(1), u32(uint32(30*seconds)), u32(33)))))
	moov := box("moov", box("mvhd", u32(0), u32(0), u32(0), u32(1000), dur, make([]byte, 80)), box("trak", tkhd, mdia))
	return bytes.Join([][]byte{box("ftyp", []byte("isom"), u32(0x200), []byte("isomavc1")), moov, box("mdat", make([]byte, 4096))}, nil)
}

func TestVideoIsStreamedAndRead(t *testing.T) {
	t.Parallel()
	mem := &memBlobs{objects: map[string][]byte{}}
	w := newWorld(t, withBlobs(mem))
	ctx := t.Context()
	data := testMP4(1080, 1920, 12)
	st, err := w.s.StageVideo(ctx, w.owner, bytes.NewReader(data), "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.s.CreateVideo(ctx, w.owner, st, w.brand.ID, "A rifle on a bench")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if m.ContentType != "video/mp4" || m.Size != int64(len(data)) || m.Width != 1080 || m.Height != 1920 || m.DurationMS != 12000 ||
		m.FrameRate < 29 || m.VideoCodec != "avc1" || m.Storage != model.StorageS3 || hex.EncodeToString(m.SHA256) != hex.EncodeToString(sum[:]) {
		t.Fatalf("video %+v", m)
	}
	if v := core.ViewMedia(m); v.Video == nil || v.Video.DurationMS != 12000 || v.Video.VideoCodec != "avc1" {
		t.Fatalf("view %+v", v)
	}
	// Reading it back streams the stored file.
	_, rc, err := w.s.MediaContent(ctx, w.owner, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	back, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(back, data) {
		t.Fatal("the stored video differs")
	}

	// Bluesky takes it; a platform whose adapter cannot post video yet
	// refuses it in the preview.
	pin, err := w.s.ConnectChannel(ctx, w.owner, core.ConnectInput{BrandID: w.brand.ID, Provider: platform.Sandbox,
		Fields: map[string]string{"emulates": "pinterest"}})
	if err != nil {
		t.Fatal(err)
	}
	renders, err := w.s.PreviewPost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "Watch"},
		Channels: []uuid.UUID{w.channel.ID, pin.ID}, Media: []uuid.UUID{m.ID}})
	if err != nil || len(renders) != 2 || len(renders[0].Violations) != 0 || len(renders[1].Violations) != 1 ||
		renders[1].Violations[0].Code != "video_unsupported" {
		t.Fatalf("preview %+v, %v", renders, err)
	}

	// A file that only starts like a video is refused, and nothing is kept.
	before := mem.len()
	broken := append(append([]byte{}, data[:24]...), make([]byte, 100)...)
	if _, err := w.s.StageVideo(ctx, w.owner, bytes.NewReader(broken), "broken.mp4"); kind(err) != apperr.KindInvalid {
		t.Fatalf("a broken video: %v", err)
	}
	if mem.len() != before {
		t.Fatal("a refused video was left in storage")
	}

	// Discarding a staged video removes it.
	st, err = w.s.StageVideo(ctx, w.owner, bytes.NewReader(data), "again.mp4")
	if err != nil {
		t.Fatal(err)
	}
	w.s.DiscardVideo(ctx, st)
	if mem.len() != before {
		t.Fatal("a discarded video was left in storage")
	}
}

func TestVideoFromAURL(t *testing.T) {
	t.Parallel()
	mem := &memBlobs{objects: map[string][]byte{}}
	w := newWorld(t, withBlobs(mem))
	data := testMP4(1920, 1080, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/octet-stream") // the bytes decide
		_, _ = rw.Write(data)
	}))
	defer srv.Close()
	m, err := w.s.ImportMedia(t.Context(), w.owner, w.brand.ID, srv.URL+"/launch.mp4", "Launch")
	if err != nil {
		t.Fatal(err)
	}
	if m.ContentType != "video/mp4" || m.Width != 1920 || m.DurationMS != 3000 || m.Filename != "launch.mp4" {
		t.Fatalf("imported %+v", m)
	}
}

func TestVideoNeedsObjectStorage(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := w.s.StageVideo(t.Context(), w.owner, bytes.NewReader(testMP4(640, 480, 1)), "x.mp4"); kind(err) != apperr.KindInvalid {
		t.Fatalf("video without S3: %v", err)
	}
	if n := w.s.MaxVideoBytes(); n != 0 {
		t.Fatalf("an install without S3 takes videos up to %d bytes", n)
	}
	small := core.New(nil, nil, nil, nil, core.Config{Blobs: &memBlobs{objects: map[string][]byte{}}, MaxVideoBytes: 100})
	if n := small.MaxVideoBytes(); n != 100 {
		t.Fatalf("configured limit %d", n)
	}
	mem := &memBlobs{objects: map[string][]byte{}}
	w2 := newWorld(t, withBlobs(mem), func(cfg *core.Config, _ *[]platform.Adapter) { cfg.MaxVideoBytes = 1000 })
	if _, err := w2.s.StageVideo(t.Context(), w2.owner, bytes.NewReader(testMP4(640, 480, 1)), "big.mp4"); kind(err) != apperr.KindInvalid {
		t.Fatalf("a video over the limit: %v", err)
	}
}

// A video posts to a platform whose adapter takes video, streamed from
// storage; the sandbox imitating Telegram reads it through.
func TestVideoPublishes(t *testing.T) {
	t.Parallel()
	mem := &memBlobs{objects: map[string][]byte{}}
	w := newWorld(t, withBlobs(mem))
	ctx := t.Context()
	tg, err := w.s.ConnectChannel(ctx, w.owner, core.ConnectInput{BrandID: w.brand.ID, Provider: platform.Sandbox,
		Fields: map[string]string{"emulates": "telegram"}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := w.s.StageVideo(ctx, w.owner, bytes.NewReader(testMP4(1080, 1920, 8)), "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.s.CreateVideo(ctx, w.owner, st, w.brand.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "Watch the build"},
		Channels: []uuid.UUID{tg.ID}, Media: []uuid.UUID{m.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// Workers of other tests, without this test's storage, may claim the
	// target first and fail to read the video; that is transient, so move
	// this service's clock past the retry, where only it will claim it.
	got := settle(t, w, p.ID)
	for try := 0; got.Status != model.PostPublished && try < 5; try++ {
		tg := got.Targets[0]
		if tg.Status != model.TargetQueued || tg.ErrorCode != "media_unreadable" {
			break
		}
		at := tg.NextAttemptAt.Add(time.Second)
		w.s.Now = func() time.Time { return at }
		got = settle(t, w, p.ID)
	}
	if got.Status != model.PostPublished {
		t.Fatalf("published as %s: %+v", got.Status, got.Targets[0])
	}
}
