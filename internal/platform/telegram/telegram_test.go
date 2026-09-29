// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"net/http"
	"net/http/httptest"
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
