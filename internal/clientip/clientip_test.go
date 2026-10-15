// SPDX-License-Identifier: AGPL-3.0-or-later

package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFrom(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, header, value, remote, want string
	}{
		{"the connection", "", "", "203.0.113.7:4321", "203.0.113.7"},
		{"no port", "", "", "203.0.113.7", "203.0.113.7"},
		{"the proxy's header", "X-Forwarded-For", "198.51.100.2", "10.0.0.1:80", "198.51.100.2"},
		{"the last entry, not the client's claim", "X-Forwarded-For", "1.2.3.4, 198.51.100.2", "10.0.0.1:80", "198.51.100.2"},
		{"a header not trusted", "", "1.2.3.4", "203.0.113.7:1", "203.0.113.7"},
		{"the trusted header missing", "X-Real-Ip", "", "203.0.113.7:1", "203.0.113.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			if tt.value != "" {
				r.Header.Set("X-Forwarded-For", tt.value)
			}
			if got := From(r, tt.header); got != tt.want {
				t.Fatalf("From = %q, want %q", got, tt.want)
			}
		})
	}
}
