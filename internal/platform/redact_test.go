// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"https://graph.facebook.com/v23.0/1/media?access_token=EAAB12x&fields=id", "https://graph.facebook.com/v23.0/1/media?access_token=REDACTED&fields=id"},
		{`Post "https://graph.threads.net/refresh_access_token?grant_type=th_refresh_token&access_token=THAA9": EOF`,
			`Post "https://graph.threads.net/refresh_access_token?grant_type=th_refresh_token&access_token=REDACTED": EOF`},
		{`{"message":"Post \"https://x.example/?refresh_token=abc\": timeout"}`, `{"message":"Post \"https://x.example/?refresh_token=REDACTED\": timeout"}`},
		{"https://discord.com/api/webhooks/123456/AbC-def_9.x?wait=true", "https://discord.com/api/webhooks/123456/REDACTED?wait=true"},
		{"https://discordapp.com/api/v10/webhooks/1/tok", "https://discordapp.com/api/v10/webhooks/1/REDACTED"},
		{"https://api.telegram.org/bot123:AAH-x_y/sendMessage", "https://api.telegram.org/botREDACTED/sendMessage"},
		{"https://www.googleapis.com/youtube/v3/videos?part=id&key=AIza1", "https://www.googleapis.com/youtube/v3/videos?part=id&key=REDACTED"},
		{"rejected: the caption is too long", "rejected: the caption is too long"},
		{"a token of thanks; tokens=3 left", "a token of thanks; tokens=3 left"},
	}
	for _, tt := range tests {
		if got := Scrub(tt.in); got != tt.want {
			t.Errorf("Scrub(%q)\n = %q\nwant %q", tt.in, got, tt.want)
		}
	}
}

// TestTransportErrorsCarryNoURL checks a failed request's error names the
// host and why, never the URL with its credentials.
func TestTransportErrorsCarryNoURL(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close() // drop the connection mid-request
	}))
	defer srv.Close()
	err := JSON(context.Background(), srv.Client(), http.MethodPost, srv.URL+"/v1/media?access_token=SECRET123", nil, nil, nil)
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != "network" {
		t.Fatalf("err = %v", err)
	}
	text := pe.Error()
	if strings.Contains(text, "SECRET123") || strings.Contains(text, "/v1/media") || !strings.Contains(text, strings.TrimPrefix(srv.URL, "http://")) {
		t.Fatalf("error text %q", text)
	}
	if _, err := http.NewRequest(http.MethodGet, "https://x.example/%zz?access_token=SECRET123", nil); err == nil {
		t.Fatal("expected a malformed URL")
	}
	err = JSON(context.Background(), srv.Client(), http.MethodGet, "https://x.example/%zz?access_token=SECRET123", nil, nil, nil)
	if err == nil || strings.Contains(err.Error(), "SECRET123") {
		t.Fatalf("a malformed request's error %v", err)
	}
}
