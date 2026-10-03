// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// MCP over HTTP runs Araldo's tools as the caller's key, through the API
// itself (ADR 0020).
func TestMCPOverHTTP(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	call := func(cl *client, msg string) (int, map[string]any) {
		t.Helper()
		return cl.do(http.MethodPost, "/v1/mcp", "application/json", []byte(msg), nil)
	}

	// No key, no tools.
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("without a key: %d", rec.Code)
	}

	status, got := call(c, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools, _ := got["result"].(map[string]any)["tools"].([]any)
	if status != http.StatusOK || len(tools) < 10 {
		t.Fatalf("tools/list: %d %v", status, got)
	}

	// A tool reads the caller's own org.
	status, got = call(c, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_brands","arguments":{}}}`)
	if status != http.StatusOK || !strings.Contains(toolText(got), c.brand) {
		t.Fatalf("list_brands: %d %v", status, got)
	}

	// A read-only key cannot post through the tools either.
	reader := c.keyWith("posts:read", "brands:read")
	msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_post","arguments":{"brand":%q,"body":"hello"}}}`, c.brand)
	status, got = call(reader, msg)
	result, _ := got["result"].(map[string]any)
	if status != http.StatusOK || result["isError"] != true || !strings.Contains(toolText(got), "scope_missing") {
		t.Fatalf("create_post with a read-only key: %d %v", status, got)
	}
}

// toolText is a tools/call result's text.
func toolText(res map[string]any) string {
	result, _ := res["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		b, _ := json.Marshal(res)
		return string(b)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	return text
}
