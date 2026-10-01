// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"strings"
	"testing"

	"rsc.io/qr"
)

const testOTPURI = "otpauth://totp/Araldo:owner@example.com?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=Araldo"

func TestQRImageDrawsEveryModuleAtScale(t *testing.T) {
	t.Parallel()
	code, err := qr.Encode(testOTPURI, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	img := qrImage(code, qrScale)
	if want := (code.Size + 2*qrQuiet) * qrScale; img.Bounds().Dx() != want || img.Bounds().Dy() != want {
		t.Fatalf("image is %v, want %dx%d", img.Bounds().Size(), want, want)
	}
	// Every pixel of every module matches the code, offset by the quiet zone.
	for my := 0; my < code.Size; my++ {
		for mx := 0; mx < code.Size; mx++ {
			want := uint8(0xff)
			if code.Black(mx, my) {
				want = 0
			}
			for dy := 0; dy < qrScale; dy++ {
				for dx := 0; dx < qrScale; dx++ {
					x, y := (mx+qrQuiet)*qrScale+dx, (my+qrQuiet)*qrScale+dy
					if got := img.GrayAt(x, y).Y; got != want {
						t.Fatalf("module (%d,%d) pixel (%d,%d) = %d, want %d", mx, my, x, y, got, want)
					}
				}
			}
		}
	}
	// The quiet zone is white all round.
	side := img.Bounds().Dx()
	for i := 0; i < side; i++ {
		for _, p := range [][2]int{{i, 0}, {0, i}, {i, side - 1}, {side - 1, i}, {i, qrQuiet*qrScale - 1}} {
			if img.GrayAt(p[0], p[1]).Y != 0xff {
				t.Fatalf("quiet zone pixel %v is not white", p)
			}
		}
	}
}

func TestQRDataURL(t *testing.T) {
	t.Parallel()
	url := qrDataURL(testOTPURI)
	data, ok := strings.CutPrefix(url, "data:image/png;base64,")
	if !ok {
		t.Fatalf("not a PNG data URL: %.40s", url)
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() < 200 {
		t.Errorf("QR is %dpx wide; phones need something they can see", img.Bounds().Dx())
	}
}
