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

	"golang.org/x/image/draw"
	xwebp "golang.org/x/image/webp"
)

// Resizing images too big for a platform (ADR 0027). Pixels are decoded
// only here, and only for images within MaxPixels.

// MaxPixels bounds the images Araldo decodes, by their header's
// dimensions, so a decompression bomb costs nothing.
const MaxPixels = 50_000_000

// Errors from Fit.
var (
	ErrTooManyPixels = errors.New("the image has more than 50 megapixels")
	ErrTransparent   = errors.New("the image has transparent pixels, which a JPEG cannot keep")
	ErrCannotFit     = errors.New("the image cannot be made small enough")
)

// qualities are the JPEG qualities Fit tries, best first; 64 is as low as
// a photo still looks like itself.
var qualities = []int{88, 80, 72, 64}

// minSide is the shortest longest side Fit scales an image down to; an
// image already smaller is still re-encoded once.
const minSide = 320

// Resizable reports whether Fit can make a JPEG of an image of this type:
// a still image, without transparency.
func Resizable(info Info) bool {
	return (info.Type == JPEG || info.Type == PNG || info.Type == WebP) && !info.Transparent &&
		int64(info.Width)*int64(info.Height) <= MaxPixels
}

// Fit re-encodes an image as a JPEG of at most maxBytes (0: any size)
// whose width plus height is at most maxSum (0: any), scaling it down,
// keeping its shape, only as far as needed. A JPEG's EXIF orientation is
// applied, since the result carries no EXIF.
func Fit(data []byte, info Info, maxBytes int64, maxSum int) ([]byte, Info, error) {
	if int64(info.Width)*int64(info.Height) > MaxPixels {
		return nil, Info{}, ErrTooManyPixels
	}
	img, err := decode(data, info.Type)
	if err != nil {
		return nil, Info{}, err
	}
	if !opaque(img) {
		return nil, Info{}, ErrTransparent
	}
	orient := 1
	if info.Type == JPEG {
		orient = orientation(data)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if orient >= 5 { // the stored image is sideways
		w, h = h, w
	}
	side := max(w, h)
	if maxSum > 0 && w+h > maxSum {
		side = side * maxSum / (w + h)
	}
	for {
		tw, th := w, h
		if side < max(w, h) {
			if w >= h {
				tw, th = side, max(1, h*side/w)
			} else {
				tw, th = max(1, w*side/h), side
			}
		}
		out := orientAndScale(img, orient, tw, th)
		for _, q := range qualities {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: q}); err != nil {
				return nil, Info{}, err
			}
			if maxBytes <= 0 || int64(buf.Len()) <= maxBytes {
				return buf.Bytes(), Info{Type: JPEG, Width: tw, Height: th}, nil
			}
		}
		if side = side * 4 / 5; side < minSide {
			return nil, Info{}, ErrCannotFit
		}
	}
}

func decode(data []byte, typ string) (image.Image, error) {
	r := bytes.NewReader(data)
	switch typ {
	case JPEG:
		return jpeg.Decode(r)
	case PNG:
		return png.Decode(r)
	case WebP:
		return xwebp.Decode(r)
	case GIF:
		return gif.Decode(r)
	}
	return nil, ErrUnsupported
}

// opaque reports whether every pixel is fully opaque.
func opaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

// orientAndScale scales img to the oriented size tw×th, then turns it
// upright: scaling first touches fewer pixels.
func orientAndScale(img image.Image, orient, tw, th int) image.Image {
	sw, sh := tw, th
	if orient >= 5 {
		sw, sh = th, tw
	}
	scaled := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), img, img.Bounds(), draw.Src, nil)
	if orient <= 1 || orient > 8 {
		return scaled
	}
	out := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			var sx, sy int
			switch orient {
			case 2: // mirrored
				sx, sy = sw-1-x, y
			case 3: // upside down
				sx, sy = sw-1-x, sh-1-y
			case 4: // mirrored upside down
				sx, sy = x, sh-1-y
			case 5: // mirrored, turned left
				sx, sy = y, x
			case 6: // turned left: rotate right to fix
				sx, sy = y, sh-1-x
			case 7: // mirrored, turned right
				sx, sy = sw-1-y, sh-1-x
			case 8: // turned right: rotate left to fix
				sx, sy = sw-1-y, x
			}
			i, j := out.PixOffset(x, y), scaled.PixOffset(sx, sy)
			copy(out.Pix[i:i+4], scaled.Pix[j:j+4])
		}
	}
	return out
}

// orientation reads a JPEG's EXIF orientation (1 to 8), or 1
// (https://www.cipa.jp/std/documents/e/DC-008-2012_E.pdf, tag 0x0112).
func orientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xff || b[1] != 0xd8 {
		return 1
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xff {
			return 1
		}
		marker := b[i+1]
		if marker == 0xda || marker == 0xd9 { // image data: no EXIF before it
			return 1
		}
		n := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if n < 2 || i+2+n > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+n]
		if marker == 0xe1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return exifOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

func exifOrientation(t []byte) int {
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := int(order.Uint32(t[4:8]))
	if ifd+2 > len(t) {
		return 1
	}
	count := int(order.Uint16(t[ifd : ifd+2]))
	for k := 0; k < count; k++ {
		e := ifd + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if order.Uint16(t[e:e+2]) == 0x0112 {
			if v := int(order.Uint16(t[e+8 : e+10])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// mayHaveAlpha reports from the header whether an image can carry
// transparency: a PNG with an alpha channel or a tRNS chunk, a WebP with
// its alpha flag, or a GIF.
func mayHaveAlpha(b []byte, typ string) bool {
	switch typ {
	case GIF:
		return true
	case PNG:
		if len(b) < 33 {
			return true
		}
		if ct := b[25]; ct == 4 || ct == 6 {
			return true
		}
		for i := 8; i+8 <= len(b); {
			n := int(binary.BigEndian.Uint32(b[i : i+4]))
			kind := string(b[i+4 : i+8])
			switch kind {
			case "tRNS":
				return true
			case "IDAT", "IEND":
				return false
			}
			i += 12 + n
		}
		return false
	case WebP:
		if len(b) < 30 {
			return true
		}
		switch string(b[12:16]) {
		case "VP8X":
			return b[20]&0x10 != 0
		case "VP8L":
			return binary.LittleEndian.Uint32(b[21:25])>>28&1 != 0
		}
	}
	return false
}

// transparent reports whether an image has any pixel that is not fully
// opaque, decoding it only when its header allows transparency. One too
// large to decode counts as transparent.
func transparent(data []byte, info Info) bool {
	if !mayHaveAlpha(data, info.Type) {
		return false
	}
	if int64(info.Width)*int64(info.Height) > MaxPixels {
		return true
	}
	img, err := decode(data, info.Type)
	return err != nil || !opaque(img)
}
