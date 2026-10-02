// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package mcp_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/api"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/mcp"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/sandbox"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// TestAgentWorkflow plays an assistant's session against the real API: see
// the brands, draft a post that is too long for Bluesky, read the
// violation, fix it, schedule it, and find it again.
func TestAgentWorkflow(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	ctx := t.Context()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mk, _ := keyring.ParseMasterKeys("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := core.New(st, keyring.New(mk, st), platform.NewRegistry(sandbox.New("https://araldo.test")), log, core.Config{BaseURL: "https://araldo.test"})
	email := fmt.Sprintf("mcp-%s@example.com", uuid.NewString()[:8])
	u, err := s.CreateUser(ctx, email, "", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	org, err := s.CreateOrg(ctx, u.ID, "Org "+email)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := s.MemberActor(ctx, u.ID, org.ID, false, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBrand(ctx, owner, core.BrandInput{Name: "Otium " + uuid.NewString()[:6]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConnectChannel(ctx, owner, core.ConnectInput{BrandID: b.ID, Provider: platform.Sandbox, Fields: map[string]string{"emulates": "bluesky"}}); err != nil {
		t.Fatal(err)
	}
	key, _, err := s.CreateOperatorAPIKey(ctx, owner, core.APIKeyInput{Name: "assistant", Scopes: []string{"brands:read", "channels:read", "templates:read", "posts:read", "posts:write"}})
	if err != nil {
		t.Fatal(err)
	}
	apiSrv := httptest.NewServer(api.New(s, log))
	t.Cleanup(apiSrv.Close)

	long := strings.Repeat("Otium helps you rest. ", 20) // 440 graphemes: too long for Bluesky
	lines := []string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		call(1, "list_brands", `{}`),
		call(2, "preview_post", fmt.Sprintf(`{"brand":%q,"body":%q}`, b.Slug, long)),
		call(3, "preview_post", fmt.Sprintf(`{"brand":%q,"body":%q,"fit":"thread"}`, b.Slug, long)),
		call(4, "create_post", fmt.Sprintf(`{"brand":%q,"body":%q,"fit":"thread","publish_at":"next_slot","idempotency_key":"launch-1"}`, b.Slug, long)),
		call(5, "create_post", fmt.Sprintf(`{"brand":%q,"body":%q,"fit":"thread","publish_at":"next_slot","idempotency_key":"launch-1"}`, b.Slug, long)),
		call(6, "list_posts", fmt.Sprintf(`{"brand":%q,"q":"helps you rest"}`, b.Slug)),
		call(7, "engagement_summary", `{"days":7}`),
		call(8, "list_channels", `{}`),
		call(9, "create_post", `{"brand":"not-a-brand","body":"x"}`),
	}
	pr, pw := io.Pipe()
	go func() {
		_ = mcp.NewServer(mcp.NewClient(apiSrv.URL, key)).Serve(ctx, strings.NewReader(strings.Join(lines, "\n")+"\n"), pw)
		_ = pw.Close()
	}()
	results := map[int]json.RawMessage{}
	errs := map[int]bool{}
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	for sc.Scan() {
		var msg struct {
			ID     int `json:"id"`
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			t.Fatal(err)
		}
		if len(msg.Result.Content) > 0 {
			results[msg.ID] = json.RawMessage(msg.Result.Content[0].Text)
			errs[msg.ID] = msg.Result.IsError
		}
	}
	for i := 1; i <= 8; i++ {
		if errs[i] {
			t.Fatalf("call %d failed: %s", i, results[i])
		}
	}
	if !strings.Contains(string(results[1]), b.Slug) {
		t.Fatalf("list_brands: %s", results[1])
	}
	var preview struct {
		Valid      bool `json:"valid"`
		Renditions []struct {
			Parts      []string `json:"parts"`
			Violations []struct {
				Code string `json:"code"`
			} `json:"violations"`
		} `json:"renditions"`
	}
	_ = json.Unmarshal(results[2], &preview)
	if preview.Valid || len(preview.Renditions) != 1 || preview.Renditions[0].Violations[0].Code != "too_long" {
		t.Fatalf("the long draft: %s", results[2])
	}
	_ = json.Unmarshal(results[3], &preview)
	if !preview.Valid || len(preview.Renditions[0].Parts) < 2 {
		t.Fatalf("the threaded draft: %s", results[3])
	}
	var first, again struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(results[4], &first)
	_ = json.Unmarshal(results[5], &again)
	if first.Status != "scheduled" || first.ID == "" || again.ID != first.ID {
		t.Fatalf("create twice with one key: %s then %s", results[4], results[5])
	}
	if !strings.Contains(string(results[6]), first.ID) {
		t.Fatalf("list_posts does not find it: %s", results[6])
	}
	if !strings.Contains(string(results[7]), `"object":"engagement_summary"`) || !strings.Contains(string(results[8]), "Sandbox Bluesky") {
		t.Fatalf("summary %s, channels %s", results[7], results[8])
	}
	if !errs[9] || !strings.Contains(string(results[9]), "resource_missing") {
		t.Fatalf("an unknown brand: %s (error %v)", results[9], errs[9])
	}
}

func call(id int, tool, args string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, tool, args)
}
