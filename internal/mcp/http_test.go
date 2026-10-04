// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// brandsAPI answers /v1/brands and records the keys it was called with.
type brandsAPI struct {
	mu   sync.Mutex
	keys []string
}

func (f *brandsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.keys = append(f.keys, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if r.URL.Path == "/v1/brands" {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"brand_1","slug":"araldo"}]}`))
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"code":"not_found"}`))
}

func post(t *testing.T, h http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "https://araldo.test/v1/mcp", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer ald_test_k")
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestHTTPRunsToolsAsTheCaller(t *testing.T) {
	t.Parallel()
	api := &brandsAPI{}
	h := &HTTPHandler{API: api}

	rec := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_brands","arguments":{}}}`, nil)
	var res struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK || res.Result.IsError ||
		!strings.Contains(res.Result.Content[0].Text, "brand_1") {
		t.Fatalf("tools/call: %d %s", rec.Code, rec.Body)
	}
	if len(api.keys) != 1 || api.keys[0] != "Bearer ald_test_k" {
		t.Fatalf("the API was called with %v, want the caller's own key", api.keys)
	}
}

func TestHTTPTransport(t *testing.T) {
	t.Parallel()
	h := &HTTPHandler{API: &brandsAPI{}}
	tests := []struct {
		name    string
		body    string
		headers map[string]string
		status  int
		want    string
	}{
		{"initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, nil, 200, `"protocolVersion":"2025-06-18"`},
		{"a notification", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil, 202, ""},
		{"a batch", `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`, nil, 200, `"id":2`},
		{"a batch of notifications", `[{"jsonrpc":"2.0","method":"notifications/initialized"}]`, nil, 202, ""},
		{"not JSON", `{nope`, nil, 400, `"code":-32700`},
		{"another site's page", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Origin": "https://evil.example"}, 403, `"code":-32600`},
		{"this site's page", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Origin": "https://araldo.test"}, 200, `"result":{}`},
		{"an unknown protocol version", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"MCP-Protocol-Version": "1999-01-01"}, 400, "MCP-Protocol-Version"},
		{"a known protocol version", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"MCP-Protocol-Version": "2025-03-26"}, 200, `"result":{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := post(t, h, tt.body, tt.headers)
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.want) {
				t.Fatalf("%d %s, want %d containing %s", rec.Code, rec.Body, tt.status, tt.want)
			}
			if tt.status == 202 && rec.Body.Len() != 0 {
				t.Fatalf("a 202 has no body, got %s", rec.Body)
			}
		})
	}
}
