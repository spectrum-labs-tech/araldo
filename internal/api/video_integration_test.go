// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"mime/multipart"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/blob"
	"github.com/spectrum-labs-tech/araldo/internal/core"
)

// streamBlobs is object storage in memory that streams like S3.
type streamBlobs struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (b *streamBlobs) Put(_ context.Context, key string, data []byte, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = bytes.Clone(data)
	return nil
}

func (b *streamBlobs) Get(_ context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.objects[key]
	if !ok {
		return nil, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *streamBlobs) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.objects, key)
	return nil
}

func (b *streamBlobs) PutStream(_ context.Context, key string, r io.Reader, _ string, maxBytes int64) (int64, []byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return 0, nil, err
	}
	if int64(len(data)) > maxBytes {
		return 0, nil, blob.ErrTooLarge
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = data
	sum := sha256.Sum256(data)
	return int64(len(data)), sum[:], nil
}

func (b *streamBlobs) GetRange(_ context.Context, key string, off, n int64) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.objects[key]
	if !ok {
		return nil, blob.ErrNotFound
	}
	if off >= int64(len(data)) {
		return nil, io.EOF
	}
	return bytes.Clone(data[off:min(off+n, int64(len(data)))]), nil
}

func (b *streamBlobs) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.objects)
}

// mp4 is a minimal MP4 with a 640x360 H.264 track of two seconds.
func mp4() []byte {
	box := func(typ string, parts ...[]byte) []byte {
		body := bytes.Join(parts, nil)
		return append(append(binary.BigEndian.AppendUint32(nil, uint32(8+len(body))), typ...), body...)
	}
	u32 := func(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
	var matrix []byte
	for _, x := range []uint32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000} {
		matrix = append(matrix, u32(x)...)
	}
	tkhd := box("tkhd", u32(0), u32(0), u32(0), u32(1), u32(0), u32(2000), make([]byte, 16), matrix, u32(640<<16), u32(360<<16))
	mdia := box("mdia", box("mdhd", u32(0), u32(0), u32(0), u32(1000), u32(2000), u32(0)), box("hdlr", u32(0), u32(0), []byte("vide"), make([]byte, 12)),
		box("minf", box("stbl", box("stsd", u32(0), u32(1), box("avc1", make([]byte, 20))), box("stts", u32(0), u32(1), u32(60), u32(33)))))
	moov := box("moov", box("mvhd", u32(0), u32(0), u32(0), u32(1000), u32(2000), make([]byte, 80)), box("trak", tkhd, mdia))
	return bytes.Join([][]byte{box("ftyp", []byte("isom"), u32(0x200), []byte("isomavc1")), moov, box("mdat", make([]byte, 2048))}, nil)
}

// A video streams in through the same upload as an image, its fields in
// any order.
func TestVideoUploadOverTheAPI(t *testing.T) {
	t.Parallel()
	blobs := &streamBlobs{objects: map[string][]byte{}}
	c := newClient(t, func(cfg *core.Config) { cfg.Blobs = blobs })

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "clip.mp4")
	_, _ = fw.Write(mp4())
	_ = w.WriteField("alt", "A bench test")
	_ = w.WriteField("brand", c.brand) // after the file
	_ = w.Close()
	status, m := c.do(http.MethodPost, "/v1/media", w.FormDataContentType(), buf.Bytes(), nil)
	video, _ := m["video"].(map[string]any)
	if status != http.StatusCreated || m["type"] != "video/mp4" || m["width"].(float64) != 640 || video == nil ||
		video["duration_ms"].(float64) != 2000 || video["video_codec"] != "avc1" || m["alt"] != "A bench test" {
		t.Fatalf("upload: %d %v", status, m)
	}

	// A form that fails after the video arrived leaves nothing behind.
	before := blobs.count()
	buf.Reset()
	w = multipart.NewWriter(&buf)
	fw, _ = w.CreateFormFile("file", "clip.mp4")
	_, _ = fw.Write(mp4())
	_ = w.WriteField("brand", "no-such-brand")
	_ = w.Close()
	if status, got := c.do(http.MethodPost, "/v1/media", w.FormDataContentType(), buf.Bytes(), nil); status != http.StatusNotFound {
		t.Fatalf("an unknown brand: %d %v", status, got)
	}
	if blobs.count() != before {
		t.Fatal("a video whose upload failed was kept")
	}
}

// TestLargeVideoUploadWithIdempotencyKey checks that a video bigger than an
// image may be is streamed under an Idempotency-Key, not refused for being
// too big to hold in memory, and that a retry replays the first answer.
func TestLargeVideoUploadWithIdempotencyKey(t *testing.T) {
	t.Parallel()
	blobs := &streamBlobs{objects: map[string][]byte{}}
	c := newClient(t, func(cfg *core.Config) { cfg.Blobs = blobs })
	// The clip, then a free box padding it past the 16 MiB image limit.
	clip := mp4()
	pad := make([]byte, 17<<20)
	binary.BigEndian.PutUint32(pad, uint32(len(pad)))
	copy(pad[4:], "free")
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("brand", c.brand)
	fw, _ := w.CreateFormFile("file", "clip.mp4")
	_, _ = fw.Write(clip)
	_, _ = fw.Write(pad)
	_ = w.Close()
	key := map[string]string{"Idempotency-Key": uuid.NewString()}
	status, first := c.do(http.MethodPost, "/v1/media", w.FormDataContentType(), buf.Bytes(), key)
	if status != http.StatusCreated || first["type"] != "video/mp4" {
		t.Fatalf("a %d MiB video with an Idempotency-Key: %d %v", buf.Len()>>20, status, first)
	}
	stored := blobs.count()
	status, again := c.do(http.MethodPost, "/v1/media", w.FormDataContentType(), buf.Bytes(), key)
	if status != http.StatusCreated || again["id"] != first["id"] || blobs.count() != stored {
		t.Fatalf("the retry: %d %v (want the first answer, nothing stored)", status, again)
	}
}
