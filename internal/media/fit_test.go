// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"testing"
)

// noise is a photo-like image that compresses badly.
func noise(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		if i%4 == 3 {
			img.Pix[i] = 0xff
		} else {
			img.Pix[i] = uint8(r.IntN(256))
		}
	}
	return img
}

func jpegOf(t *testing.T, img image.Image, q int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFitShrinksToTheLimit(t *testing.T) {
	t.Parallel()
	data := jpegOf(t, noise(1600, 1200), 95)
	info, err := Inspect(data)
	if err != nil {
		t.Fatal(err)
	}
	const limit = 300_000
	if len(data) <= limit {
		t.Fatalf("the test image is only %d bytes", len(data))
	}
	out, got, err := Fit(data, info, limit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > limit || got.Type != JPEG || got.Width > 1600 || got.Width*3 != got.Height*4 {
		t.Fatalf("fit to %d bytes, %+v", len(out), got)
	}
	if back, err := Inspect(out); err != nil || back.Width != got.Width || back.Height != got.Height {
		t.Fatalf("the result reads as %+v, %v", back, err)
	}
}

func TestFitKeepsSizeWhenOnlyTheTypeChanges(t *testing.T) {
	t.Parallel()
	img := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	info, _ := Inspect(buf.Bytes())
	if info.Transparent || !Resizable(info) {
		t.Fatalf("an opaque PNG %+v should be resizable", info)
	}
	_, got, err := Fit(buf.Bytes(), info, 0, 0)
	if err != nil || got != (Info{Type: JPEG, Width: 300, Height: 200}) {
		t.Fatalf("converted %+v, %v", got, err)
	}
}

func TestFitBoundsWidthPlusHeight(t *testing.T) {
	t.Parallel()
	data := jpegOf(t, noise(400, 300), 80)
	info, _ := Inspect(data)
	_, got, err := Fit(data, info, 0, 500)
	if err != nil || got.Width+got.Height > 500 || got.Width < 280 {
		t.Fatalf("fit within 500: %+v, %v", got, err)
	}
}

// withOrientation inserts an EXIF APP1 segment naming an orientation.
func withOrientation(data []byte, o uint16) []byte {
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01") // big-endian, IFD0 at 8, one entry
	tiff = binary.BigEndian.AppendUint16(tiff, 0x0112)
	tiff = binary.BigEndian.AppendUint16(tiff, 3) // SHORT
	tiff = binary.BigEndian.AppendUint32(tiff, 1)
	tiff = binary.BigEndian.AppendUint16(tiff, o)
	tiff = append(tiff, 0, 0, 0, 0, 0, 0)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := binary.BigEndian.AppendUint16([]byte{0xff, 0xe1}, uint16(len(seg)+2))
	app1 = append(app1, seg...)
	return append(append([]byte{0xff, 0xd8}, app1...), data[2:]...)
}

func TestFitTurnsPhotosUpright(t *testing.T) {
	t.Parallel()
	// Stored sideways, as a phone held upright saves it: red on the left,
	// blue on the right.
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			c := color.RGBA{R: 0xff, A: 0xff}
			if x >= 200 {
				c = color.RGBA{B: 0xff, A: 0xff}
			}
			img.Set(x, y, c)
		}
	}
	tests := []struct {
		orientation uint16
		w, h        int
		top, bottom string
	}{
		{1, 400, 200, "", ""},
		{6, 200, 400, "red", "blue"},
		{8, 200, 400, "blue", "red"},
	}
	for _, tt := range tests {
		data := withOrientation(jpegOf(t, img, 90), tt.orientation)
		if got := orientation(data); got != int(tt.orientation) {
			t.Fatalf("orientation read as %d, want %d", got, tt.orientation)
		}
		info, _ := Inspect(data)
		out, got, err := Fit(data, info, 0, 0)
		if err != nil || got.Width != tt.w || got.Height != tt.h {
			t.Fatalf("orientation %d: %+v, %v", tt.orientation, got, err)
		}
		if tt.top == "" {
			continue
		}
		res, err := jpeg.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if c := colorName(res.At(100, 50)); c != tt.top {
			t.Errorf("orientation %d: the top is %s, want %s", tt.orientation, c, tt.top)
		}
		if c := colorName(res.At(100, 350)); c != tt.bottom {
			t.Errorf("orientation %d: the bottom is %s, want %s", tt.orientation, c, tt.bottom)
		}
	}
}

func colorName(c color.Color) string {
	r, _, b, _ := c.RGBA()
	switch {
	case r > 0xc000 && b < 0x4000:
		return "red"
	case b > 0xc000 && r < 0x4000:
		return "blue"
	}
	return "other"
}

func TestFitRefuses(t *testing.T) {
	t.Parallel()
	clear := image.NewNRGBA(image.Rect(0, 0, 20, 20)) // every pixel transparent
	var buf bytes.Buffer
	if err := png.Encode(&buf, clear); err != nil {
		t.Fatal(err)
	}
	info, _ := Inspect(buf.Bytes())
	if Resizable(info) {
		t.Fatal("a transparent PNG is not resizable")
	}
	if _, _, err := Fit(buf.Bytes(), info, 0, 0); !errors.Is(err, ErrTransparent) {
		t.Fatalf("transparent: %v", err)
	}
	if _, _, err := Fit(nil, Info{Type: JPEG, Width: 10_000, Height: 6_000}, 0, 0); !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("60 megapixels: %v", err)
	}
	data := jpegOf(t, noise(400, 300), 90)
	info, _ = Inspect(data)
	if _, _, err := Fit(data, info, 100, 0); !errors.Is(err, ErrCannotFit) {
		t.Fatalf("100 bytes: %v", err)
	}
	if Resizable(Info{Type: GIF, Width: 10, Height: 10}) {
		t.Fatal("a GIF is not resizable")
	}
}
