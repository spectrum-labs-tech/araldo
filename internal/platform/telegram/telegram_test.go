// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func server(t *testing.T, h http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a := New(srv.Client())
	a.API = srv.URL
	return a
}

func TestPublishAndVerify(t *testing.T) {
	t.Parallel()
	a := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/botSECRET/getChat":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":-100,"title":"Launches","username":"launches"}}`))
		case "/botSECRET/sendMessage":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7,"chat":{"id":-100,"username":"launches"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":404,"description":"Not Found"}`))
		}
	})
	creds := platform.Credentials{"bot_token": "SECRET", "chat_id": "@launches"}
	acct, err := a.Verify(t.Context(), creds)
	if err != nil || acct.Handle != "@launches" || acct.DisplayName != "Launches" {
		t.Fatalf("Verify = %+v, %v", acct, err)
	}
	res, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"hello"}}, nil)
	if err != nil || res.Permalink != "https://t.me/launches/7" {
		t.Fatalf("Publish = %+v, %v", res, err)
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		body   string
		want   platform.Kind
		retry  time.Duration
	}{
		{429, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":9}}`, platform.RateLimited, 9 * time.Second},
		{401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, platform.AuthRevoked, 0},
		{403, `{"ok":false,"error_code":403,"description":"Forbidden: bot is not a member"}`, platform.AuthRevoked, 0},
		{400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, platform.AuthRevoked, 0},
		{400, `{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`, platform.Rejected, 0},
		{502, `bad gateway`, platform.Uncertain, 0},
	}
	for _, tt := range tests {
		a := server(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(tt.body))
		})
		_, err := a.Publish(t.Context(), platform.Credentials{"bot_token": "SECRET", "chat_id": "1"}, platform.Payload{Parts: []string{"x"}}, nil)
		var pe *platform.Error
		if k := platform.KindOf(err); k != tt.want {
			t.Errorf("%d %s: kind %s, want %s", tt.status, tt.body, k, tt.want)
		}
		if pe, _ = err.(*platform.Error); pe != nil && pe.RetryAfter != tt.retry { //nolint:errorlint // direct result
			t.Errorf("%d: retry after %s, want %s", tt.status, pe.RetryAfter, tt.retry)
		}
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Errorf("error leaks the bot token: %v", err)
		}
	}
}

func TestNetworkErrorDoesNotLeakToken(t *testing.T) {
	t.Parallel()
	a := New(&http.Client{Timeout: time.Second})
	a.API = "http://127.0.0.1:1"
	_, err := a.Publish(t.Context(), platform.Credentials{"bot_token": "SECRET123", "chat_id": "1"}, platform.Payload{Parts: []string{"x"}}, nil)
	if err == nil || strings.Contains(err.Error(), "SECRET123") {
		t.Fatalf("err = %v (must exist and must not contain the token)", err)
	}
}

func TestPublishPhotos(t *testing.T) {
	t.Parallel()
	type call struct {
		method string
		fields map[string]string
		files  []string // field:name:data
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		c := call{method: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], fields: map[string]string{}}
		for k, v := range r.MultipartForm.Value {
			c.fields[k] = v[0]
		}
		for field, hs := range r.MultipartForm.File {
			f, _ := hs[0].Open()
			data, _ := io.ReadAll(f)
			c.files = append(c.files, field+":"+hs[0].Filename+":"+string(data))
		}
		sort.Strings(c.files)
		calls = append(calls, c)
		msg := `{"message_id":7,"chat":{"id":-100,"username":"ar15build"}}`
		if c.method == "sendMediaGroup" {
			msg = `[` + msg + `,{"message_id":8,"chat":{"id":-100,"username":"ar15build"}}]`
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":` + msg + `}`))
	}))
	defer srv.Close()
	a := New(srv.Client())
	a.API = srv.URL
	creds := platform.Credentials{"bot_token": "123:abc", "chat_id": "@ar15build"}
	png := platform.Media{Type: "image/png"}.WithData([]byte("png"))
	jpg := platform.Media{Type: "image/jpeg"}.WithData([]byte("jpg"))

	res, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"one photo"}, Media: []platform.Media{png}}, nil)
	if err != nil || res.Permalink != "https://t.me/ar15build/7" {
		t.Fatalf("Publish = %+v, %v", res, err)
	}
	if c := calls[0]; c.method != "sendPhoto" || c.fields["caption"] != "one photo" || c.fields["chat_id"] != "@ar15build" ||
		strings.Join(c.files, ",") != "photo:image1.png:png" {
		t.Fatalf("one photo: %+v", c)
	}

	res, err = a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"an album"}, Media: []platform.Media{png, jpg}}, nil)
	if err != nil || res.Parts[0].ID != "7" {
		t.Fatalf("Publish = %+v, %v", res, err)
	}
	c := calls[1]
	var album []map[string]string
	_ = json.Unmarshal([]byte(c.fields["media"]), &album)
	if c.method != "sendMediaGroup" || strings.Join(c.files, ",") != "photo0:image1.png:png,photo1:image2.jpg:jpg" || len(album) != 2 ||
		album[0]["media"] != "attach://photo0" || album[0]["caption"] != "an album" || album[1]["caption"] != "" {
		t.Fatalf("album: %+v, media %v", c, album)
	}

	if _, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{""}, Media: []platform.Media{png}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := calls[2].fields["caption"]; ok {
		t.Fatal("an empty caption should be left out")
	}
}

func TestPublishVideo(t *testing.T) {
	t.Parallel()
	var (
		fields map[string]string
		file   string
		length int64
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		length = r.ContentLength
		if err := r.ParseMultipartForm(1 << 20); err != nil || !strings.HasSuffix(r.URL.Path, "/sendVideo") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
		hs := r.MultipartForm.File["video"][0]
		f, _ := hs.Open()
		data, _ := io.ReadAll(f)
		file = hs.Filename + ":" + hs.Header.Get("Content-Type") + ":" + string(data)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":9,"chat":{"id":-100,"username":"ar15build"}}}`))
	}))
	defer srv.Close()
	a := New(srv.Client())
	a.API = srv.URL
	v := platform.Media{Type: "video/mp4", Width: 1080, Height: 1920, Duration: 12400 * time.Millisecond}.WithData([]byte("mp4 bytes"))
	res, err := a.Publish(t.Context(), platform.Credentials{"bot_token": "123:abc", "chat_id": "@ar15build"},
		platform.Payload{Parts: []string{"watch this"}, Media: []platform.Media{v}}, nil)
	if err != nil || res.Permalink != "https://t.me/ar15build/9" {
		t.Fatalf("Publish = %+v, %v", res, err)
	}
	if fields["caption"] != "watch this" || fields["width"] != "1080" || fields["height"] != "1920" || fields["duration"] != "12" ||
		fields["supports_streaming"] != "true" || file != "video1.mp4:video/mp4:mp4 bytes" || length <= 0 {
		t.Fatalf("fields %v, file %q, length %d", fields, file, length)
	}
}
