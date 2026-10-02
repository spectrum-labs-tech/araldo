// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func encoded(t *testing.T, typ string, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	var err error
	switch typ {
	case JPEG:
		err = jpeg.Encode(&buf, img, nil)
	case PNG:
		err = png.Encode(&buf, img)
	case GIF:
		err = gif.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// webp builds a WebP header with one chunk; Inspect reads no further.
func webp(chunk string, header []byte) []byte {
	b := []byte("RIFF\x00\x00\x00\x00WEBP" + chunk + "\x00\x00\x00\x00")
	b = append(b, header...)
	return append(b, make([]byte, 16)...)
}

func TestInspect(t *testing.T) {
	t.Parallel()
	vp8 := []byte{0, 0, 0, 0x9d, 0x01, 0x2a}
	vp8 = binary.LittleEndian.AppendUint16(vp8, 640)
	vp8 = binary.LittleEndian.AppendUint16(vp8, 480|0xc000) // the top two bits are scaling, not size
	vp8l := binary.LittleEndian.AppendUint32([]byte{0x2f}, uint32(1199)|uint32(799)<<14)
	vp8x := []byte{0x10, 0, 0, 0, 0x3f, 0x1f, 0, 0x37, 0x04, 0} // canvas 8000×1080

	tests := []struct {
		name string
		data []byte
		want Info
		err  bool
	}{
		{"jpeg", encoded(t, JPEG, 40, 30), Info{JPEG, 40, 30}, false},
		{"png", encoded(t, PNG, 1, 500), Info{PNG, 1, 500}, false},
		{"gif", encoded(t, GIF, 12, 12), Info{GIF, 12, 12}, false},
		{"webp lossy", webp("VP8 ", vp8), Info{WebP, 640, 480}, false},
		{"webp lossless", webp("VP8L", vp8l), Info{WebP, 1200, 800}, false},
		{"webp extended", webp("VP8X", vp8x), Info{WebP, 8000, 1080}, false},
		{"webp without a start code", webp("VP8 ", []byte{0, 0, 0, 1, 2, 3, 0, 0, 0, 0}), Info{}, true},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), Info{}, true},
		{"text", []byte("hello"), Info{}, true},
		{"empty", nil, Info{}, true},
		{"truncated png", encoded(t, PNG, 10, 10)[:20], Info{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Inspect(tt.data)
			if (err != nil) != tt.err {
				t.Fatalf("Inspect err = %v, want error %v", err, tt.err)
			}
			if got != tt.want {
				t.Fatalf("Inspect = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestInspectRefusesWhatItCannotPost(t *testing.T) {
	t.Parallel()
	_, err := Inspect([]byte("%PDF-1.7\n"))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Inspect(pdf) = %v, want ErrUnsupported", err)
	}
}

func TestExtension(t *testing.T) {
	t.Parallel()
	for typ, want := range map[string]string{JPEG: ".jpg", PNG: ".png", GIF: ".gif", WebP: ".webp", "image/svg+xml": ""} {
		if got := Extension(typ); got != want {
			t.Errorf("Extension(%q) = %q, want %q", typ, got, want)
		}
	}
}
