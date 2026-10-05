// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI records requests and answers them from a table.
type fakeAPI struct {
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	Method, Path, Query, Auth, IdemKey string
	Body                               map[string]any
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), body})
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/v1/posts" && body["brand"] == "nope":
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"resource_missing","detail":"No such brand."}`))
	case r.URL.Path == "/v1/templates" && r.URL.Query().Get("key") == "release":
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"tmpl_1"}]}`))
	default:
		_, _ = w.Write([]byte(`{"ok":true,"path":"` + r.URL.Path + `"}`))
	}
}

// session runs a server on requests (one JSON message per line) and
// returns its responses by id.
func session(t *testing.T, lines ...string) (map[string]map[string]any, *fakeAPI, int) {
	t.Helper()
	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- NewServer(NewClient(srv.URL+"/", "ald_test_k")).Serve(t.Context(), in, pw)
		_ = pw.Close()
	}()
	out := map[string]map[string]any{}
	n := 0
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			t.Fatalf("not JSON: %s", sc.Text())
		}
		if msg["jsonrpc"] != "2.0" {
			t.Fatalf("message without jsonrpc 2.0: %s", sc.Text())
		}
		raw, _ := json.Marshal(msg["id"])
		out[string(raw)] = msg
		n++
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return out, f, n
}

func call(id int, tool string, args string) string {
	return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func result(t *testing.T, msg map[string]any) map[string]any {
	t.Helper()
	r, ok := msg["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", msg)
	}
	return r
}

func toolText(t *testing.T, msg map[string]any) (string, bool) {
	t.Helper()
	r := result(t, msg)
	content, _ := r["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content %v", r["content"])
	}
	c, _ := content[0].(map[string]any)
	isErr, _ := r["isError"].(bool)
	return c["text"].(string), isErr
}

func TestHandshake(t *testing.T) {
	t.Parallel()
	out, _, n := session(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
	)
	if n != 3 {
		t.Fatalf("%d responses, want 3 (none for the notification)", n)
	}
	init := result(t, out["1"])
	if init["protocolVersion"] != "2025-03-26" || init["instructions"] == "" {
		t.Fatalf("initialize: %v", init)
	}
	info, _ := init["serverInfo"].(map[string]any)
	caps, _ := init["capabilities"].(map[string]any)
	if info["name"] != "araldo" || caps["tools"] == nil {
		t.Fatalf("server info %v, capabilities %v", info, caps)
	}
	if v := result(t, out["2"])["protocolVersion"]; v != protocolVersions[0] {
		t.Fatalf("an unknown version gets %v, want the newest, %s", v, protocolVersions[0])
	}
	if r := result(t, out["3"]); len(r) != 0 {
		t.Fatalf("ping: %v", r)
	}
}

func TestToolsList(t *testing.T) {
	t.Parallel()
	out, _, _ := session(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	list, _ := result(t, out["1"])["tools"].([]any)
	if len(list) != 21 {
		t.Fatalf("%d tools, want 21", len(list))
	}
	names := map[string]map[string]any{}
	for _, raw := range list {
		tool := raw.(map[string]any)
		schema, _ := tool["inputSchema"].(map[string]any)
		ann, _ := tool["annotations"].(map[string]any)
		if schema["type"] != "object" || tool["description"] == "" || ann == nil {
			t.Errorf("tool %v: schema %v, annotations %v", tool["name"], schema, ann)
		}
		names[tool["name"].(string)] = ann
	}
	if names["preview_post"]["readOnlyHint"] != true || names["create_post"]["readOnlyHint"] != false ||
		names["cancel_post"]["destructiveHint"] != true || names["create_post"]["openWorldHint"] != true {
		t.Fatalf("annotations: %v", names)
	}
}

func TestToolsCallTheAPI(t *testing.T) {
	t.Parallel()
	out, f, _ := session(t,
		call(1, "preview_post", `{"brand":"araldo","body":"New build","fit":"thread","overrides":{"x":"Short"},"media":["media_1"]}`),
		call(2, "create_post", `{"brand":"araldo","template":"release","data":{"version":"1.0"},"publish_at":"next_slot"}`),
		call(3, "create_post", `{"brand":"araldo","body":"again","idempotency_key":"mine"}`),
		call(4, "list_posts", `{"brand":"araldo","status":"failed","limit":5}`),
		call(5, "get_template", `{"template":"release","brand":"araldo"}`),
		call(6, "engagement_summary", `{"group_by":"channel","days":7}`),
		call(7, "cancel_post", `{"post":"post_1"}`),
		call(8, "reschedule_post", `{"post":"post_1","swap_with":"post_2"}`),
		call(9, "ads_summary", `{"group_by":"day","days":7}`),
		call(10, "analytics_summary", `{"group_by":"post","days":14}`),
		call(11, "brand_report", `{"brand":"araldo","month":"2026-09"}`),
		call(12, "draft_newsletter", `{"brand":"araldo","subject":"October","body":"# Hi"}`),
		call(13, "preview_newsletter", `{"brand":"araldo","subject":"October","preview_text":"What shipped","body":"# Hi"}`),
		call(14, "list_newsletters", `{"status":"sent"}`),
		call(15, "retry_target", `{"target":"ptgt_1"}`),
		call(16, "mark_target_published", `{"target":"ptgt_2","permalink":"https://bsky.app/profile/araldo.dev/post/1"}`),
	)
	for i := 1; i <= 16; i++ {
		if text, isErr := toolText(t, out[itoa(i)]); isErr {
			t.Fatalf("call %d failed: %s", i, text)
		}
	}
	byPath := map[string]recorded{}
	for _, r := range f.reqs {
		if r.Auth != "Bearer ald_test_k" {
			t.Fatalf("%s without the key: %q", r.Path, r.Auth)
		}
		byPath[r.Method+" "+r.Path] = r
	}
	preview := byPath["POST /v1/posts/preview"]
	content, _ := preview.Body["content"].(map[string]any)
	fit, _ := content["fit"].(map[string]any)
	if content["body"] != "New build" || fit["bluesky"] != "thread" || fit["x"] != "thread" || preview.Body["media"] == nil ||
		content["overrides"].(map[string]any)["x"] != "Short" || preview.IdemKey != "" {
		t.Fatalf("preview body %v", preview.Body)
	}
	var creates []recorded
	for _, r := range f.reqs {
		if r.Method == http.MethodPost && r.Path == "/v1/posts" {
			creates = append(creates, r)
		}
	}
	if len(creates) != 2 || creates[0].IdemKey == "" || creates[1].IdemKey != "mine" || creates[0].Body["content"] != nil ||
		creates[0].Body["template"] != "release" || creates[0].Body["publish_at"] != "next_slot" {
		t.Fatalf("creates %+v", creates)
	}
	if q := byPath["GET /v1/posts"].Query; !strings.Contains(q, "status=failed") || !strings.Contains(q, "limit=5") {
		t.Fatalf("list query %q", q)
	}
	if _, ok := byPath["GET /v1/templates/tmpl_1"]; !ok {
		t.Fatalf("get_template by key did not fetch the template: %v", f.reqs)
	}
	if q := byPath["GET /v1/engagement/summary"].Query; !strings.Contains(q, "since=") || !strings.Contains(q, "group_by=channel") {
		t.Fatalf("summary query %q", q)
	}
	if _, ok := byPath["POST /v1/posts/post_1/cancel"]; !ok {
		t.Fatal("cancel_post did not call the API")
	}
	if move := byPath["POST /v1/posts/post_1/reschedule"]; len(move.Body) != 1 || move.Body["swap_with"] != "post_2" {
		t.Fatalf("reschedule_post body %v", move.Body)
	}
	if _, ok := byPath["POST /v1/post_targets/ptgt_1/retry"]; !ok {
		t.Fatal("retry_target did not call the API")
	}
	if mark := byPath["POST /v1/post_targets/ptgt_2/mark_published"]; mark.Body["permalink"] != "https://bsky.app/profile/araldo.dev/post/1" {
		t.Fatalf("mark_target_published body %v", mark.Body)
	}
	if q := byPath["GET /v1/ads/summary"].Query; !strings.Contains(q, "group_by=day") || !strings.Contains(q, "since=") {
		t.Fatalf("ads_summary query %q", q)
	}
	if q := byPath["GET /v1/analytics/summary"].Query; !strings.Contains(q, "group_by=post") || !strings.Contains(q, "since=") {
		t.Fatalf("analytics_summary query %q", q)
	}
	if q := byPath["GET /v1/reports"].Query; q != "brand=araldo&month=2026-09" {
		t.Fatalf("brand_report query %q", q)
	}
	if d := byPath["POST /v1/newsletters"]; d.Body["subject"] != "October" || d.Body["brand"] != "araldo" || d.IdemKey == "" {
		t.Fatalf("draft_newsletter %+v", d)
	}
	if p := byPath["POST /v1/newsletters/preview"]; p.Body["preview_text"] != "What shipped" || p.IdemKey != "" {
		t.Fatalf("preview_newsletter %+v", p)
	}
	if q := byPath["GET /v1/newsletters"].Query; q != "status=sent" {
		t.Fatalf("list_newsletters query %q", q)
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	out, _, _ := session(t,
		call(1, "create_post", `{"brand":"nope","body":"x"}`),
		call(2, "no_such_tool", `{}`),
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`,
		`not json`,
		call(5, "get_post", `{}`),
	)
	text, isErr := toolText(t, out["1"])
	if !isErr || !strings.Contains(text, `"code":"resource_missing"`) {
		t.Fatalf("an API error: %q (isError %v), want its problem details", text, isErr)
	}
	for id, want := range map[string]float64{"2": codeInvalidParams, "3": codeMethodNotFound, "null": codeParse} {
		e, _ := out[id]["error"].(map[string]any)
		if e["code"] != want {
			t.Errorf("response %s: error %v, want code %v", id, out[id]["error"], want)
		}
	}
	if text, isErr := toolText(t, out["5"]); !isErr || !strings.Contains(text, "post is required") {
		t.Fatalf("a missing argument: %q", text)
	}
}
