// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

func TestMediaRead(t *testing.T) {
	t.Parallel()
	open := func(data string, err error) func(context.Context) (io.ReadCloser, error) {
		return func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(data)), err }
	}
	tests := []struct {
		name string
		m    Media
		want string
		code string
	}{
		{"whole file", Media{Size: 5, Open: open("bytes", nil)}, "bytes", ""},
		{"no file", Media{Size: 5}, "", "media_unreadable"},
		{"open fails", Media{Size: 5, Open: open("", errors.New("gone"))}, "", "media_unreadable"},
		{"shorter than recorded", Media{Size: 9, Open: open("bytes", nil)}, "", "media_unreadable"},
		{"longer than recorded", Media{Size: 3, Open: open("bytes", nil)}, "", "media_unreadable"},
	}
	for _, tt := range tests {
		got, err := tt.m.Read(t.Context())
		var pe *Error
		switch {
		case tt.code == "" && (err != nil || string(got) != tt.want):
			t.Errorf("%s: Read = %q, %v", tt.name, got, err)
		case tt.code != "" && (!errors.As(err, &pe) || pe.Code != tt.code || pe.Kind != Transient):
			t.Errorf("%s: Read error %v, want a transient %s", tt.name, err, tt.code)
		}
	}
	if m := (Media{Type: "image/webp"}).WithData([]byte("abc")); m.Size != 3 || m.Filename(1) != "image2.webp" {
		t.Errorf("WithData: size %d, name %q", m.Size, m.Filename(1))
	}
}

func TestMultipart(t *testing.T) {
	t.Parallel()
	body, contentType, err := Multipart([][2]string{{"chat_id", "@x"}, {"caption", "héllo"}},
		[]File{{Field: "photo", Name: `a "b".png`, Type: "image/png", Data: []byte{0, 1, 2}}})
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("content type %q: %v", contentType, err)
	}
	form, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if form.Value["chat_id"][0] != "@x" || form.Value["caption"][0] != "héllo" {
		t.Fatalf("fields %v", form.Value)
	}
	fh := form.File["photo"][0]
	f, _ := fh.Open()
	data, _ := io.ReadAll(f)
	if fh.Filename != `a "b".png` || fh.Header.Get("Content-Type") != "image/png" || !bytes.Equal(data, []byte{0, 1, 2}) {
		t.Fatalf("file %q %q %v", fh.Filename, fh.Header.Get("Content-Type"), data)
	}
}
