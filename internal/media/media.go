// SPDX-License-Identifier: AGPL-3.0-or-later

// Package media identifies uploaded images (ADR 0017): their type from the
// bytes themselves and their dimensions from the header. It never decodes
// pixels, so a decompression bomb costs nothing.
package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
)

// Limits on every upload, whatever the platform.
const (
	// MaxBytes is the most any supported platform accepts for an image
	// (Mastodon's default image_size_limit).
	MaxBytes = 16 << 20
	// MaxAlt is X's limit on alt text, the lowest of the platforms that
	// take it.
	MaxAlt = 1000
)

// Image types Araldo accepts.
const (
	JPEG = "image/jpeg"
	PNG  = "image/png"
	GIF  = "image/gif"
	WebP = "image/webp"
)

// Types lists the accepted types.
var Types = []string{JPEG, PNG, GIF, WebP}

// ErrUnsupported is returned for anything but a JPEG, PNG, GIF or WebP.
var ErrUnsupported = errors.New("not a JPEG, PNG, GIF or WebP image")

// Info is what Araldo knows about an image.
type Info struct {
	Type          string
	Width, Height int
}

// Inspect identifies data and reads its dimensions.
func Inspect(data []byte) (Info, error) {
	typ := http.DetectContentType(data)
	var (
		cfg image.Config
		err error
	)
	switch typ {
	case JPEG:
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case PNG:
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case GIF:
		cfg, err = gif.DecodeConfig(bytes.NewReader(data))
	case WebP:
		cfg, err = webpConfig(data)
	default:
		return Info{}, ErrUnsupported
	}
	if err != nil {
		return Info{}, fmt.Errorf("unreadable %s: %w", typ, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Info{}, fmt.Errorf("unreadable %s: no dimensions", typ)
	}
	return Info{Type: typ, Width: cfg.Width, Height: cfg.Height}, nil
}

// Extension is the usual file extension for an accepted type.
func Extension(typ string) string {
	switch typ {
	case JPEG:
		return ".jpg"
	case PNG:
		return ".png"
	case GIF:
		return ".gif"
	case WebP:
		return ".webp"
	}
	return ""
}

// webpConfig reads a WebP's canvas size from its first chunk: a lossy
// (VP8), lossless (VP8L) or extended (VP8X) header
// (https://developers.google.com/speed/webp/docs/riff_container).
func webpConfig(b []byte) (image.Config, error) {
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return image.Config{}, errors.New("short or malformed RIFF header")
	}
	switch string(b[12:16]) {
	case "VP8 ":
		// A 3-byte frame tag, the start code 9d 01 2a, then 14-bit width
		// and height (RFC 6386, section 9.1).
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return image.Config{}, errors.New("missing VP8 start code")
		}
		w := int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
		return image.Config{Width: w, Height: h}, nil
	case "VP8L":
		// The signature 0x2f, then 14 bits each of width-1 and height-1.
		if b[20] != 0x2f {
			return image.Config{}, errors.New("missing VP8L signature")
		}
		v := binary.LittleEndian.Uint32(b[21:25])
		return image.Config{Width: int(v&0x3fff) + 1, Height: int(v>>14&0x3fff) + 1}, nil
	case "VP8X":
		// Flags and reserved bytes, then 24 bits each of width-1 and
		// height-1.
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return image.Config{Width: w + 1, Height: h + 1}, nil
	}
	return image.Config{}, fmt.Errorf("unknown WebP chunk %q", b[12:16])
}
