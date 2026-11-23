// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewslettersOverTheAPI(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	status, providers := c.json(http.MethodGet, "/v1/mail_providers", nil)
	if data, _ := providers["data"].([]any); status != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["provider"] != "sandbox" {
		t.Fatalf("providers: %d %v", status, providers)
	}
	status, acct := c.json(http.MethodPost, "/v1/mail_accounts", map[string]any{"brand": c.brand, "provider": "sandbox",
		"from_name": "Brand", "from_email": "news@brand.example", "default_audiences": []string{"list-1"}})
	if status != http.StatusCreated || acct["object"] != "mail_account" || acct["credentials"] != nil {
		t.Fatalf("connect: %d %v", status, acct)
	}
	aid := acct["id"].(string)
	status, aud := c.json(http.MethodGet, "/v1/mail_accounts/"+aid+"/audiences", nil)
	if data, _ := aud["data"].([]any); status != http.StatusOK || len(data) != 3 || data[2].(map[string]any)["size"] != nil {
		t.Fatalf("audiences: %d %v", status, aud)
	}

	status, brand := c.json(http.MethodPost, "/v1/brands/"+c.brand+"/email_theme", map[string]any{"accent": "#1d4ed8",
		"postal_address": "1 Main St", "footer": "Thanks for reading."})
	if theme, _ := brand["email_theme"].(map[string]any); status != http.StatusOK || theme["postal_address"] != "1 Main St" || theme["logo"] != nil {
		t.Fatalf("theme: %d %v", status, brand)
	}

	issue := map[string]any{"brand": c.brand, "subject": "October", "preview_text": "What shipped", "body": "# Hello\n\n[Read](https://brand.example){.button}"}
	status, prev := c.json(http.MethodPost, "/v1/newsletters/preview", issue)
	if status != http.StatusOK || prev["object"] != "newsletter_preview" || !strings.Contains(prev["html"].(string), "Thanks for reading.") ||
		!strings.Contains(prev["text"].(string), "Read: https://brand.example") {
		t.Fatalf("preview: %d %v", status, prev)
	}
	status, is := c.json(http.MethodPost, "/v1/newsletters", issue)
	if status != http.StatusCreated || is["object"] != "newsletter_issue" || is["status"] != "draft" {
		t.Fatalf("create: %d %v", status, is)
	}
	nid := is["id"].(string)
	if ds, _ := is["deliveries"].([]any); len(ds) != 1 || ds[0].(map[string]any)["mail_account"] != aid {
		t.Fatalf("deliveries: %v", is["deliveries"])
	}
	// An edit without deliveries keeps them.
	status, is = c.json(http.MethodPost, "/v1/newsletters/"+nid, map[string]any{"subject": "October news", "body": "Hello again"})
	if ds, _ := is["deliveries"].([]any); status != http.StatusOK || is["subject"] != "October news" || len(ds) != 1 {
		t.Fatalf("edit: %d %v", status, is)
	}
	if status, got := c.json(http.MethodPost, "/v1/newsletters/"+nid, map[string]any{"brand": c.brand, "subject": "x", "body": "x"}); status != http.StatusBadRequest {
		t.Fatalf("moving an issue to a brand: %d %v", status, got)
	}
	if status, got := c.json(http.MethodPost, "/v1/newsletters/"+nid+"/test", map[string]any{"mail_account": aid, "to": []string{"me@brand.example"}}); status != http.StatusOK || got["sent"].(float64) != 1 {
		t.Fatalf("test: %d %v", status, got)
	}
	sendAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	status, is = c.json(http.MethodPost, "/v1/newsletters/"+nid+"/schedule", map[string]any{"send_at": sendAt})
	if status != http.StatusOK || is["status"] != "scheduled" || is["send_at"] != sendAt {
		t.Fatalf("schedule: %d %v", status, is)
	}
	status, list := c.json(http.MethodGet, "/v1/newsletters?status=scheduled", nil)
	if data, _ := list["data"].([]any); status != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["id"] != nid {
		t.Fatalf("list: %d %v", status, list)
	}
	if status, got := c.json(http.MethodDelete, "/v1/mail_accounts/"+aid, nil); status != http.StatusConflict {
		t.Fatalf("disconnecting an account in use: %d %v", status, got)
	}
	status, is = c.json(http.MethodPost, "/v1/newsletters/"+nid+"/cancel", nil)
	if status != http.StatusOK || is["status"] != "canceled" {
		t.Fatalf("cancel: %d %v", status, is)
	}
	if status, got := c.json(http.MethodGet, "/v1/newsletters/"+nid+"/preview", nil); status != http.StatusOK || got["html"] == "" {
		t.Fatalf("render: %d %v", status, got)
	}
	if status, got := c.json(http.MethodDelete, "/v1/mail_accounts/"+aid, nil); status != http.StatusOK || got["deleted"] != true {
		t.Fatalf("disconnect: %d %v", status, got)
	}
}

// TestTopPostsOverTheAPI checks the endpoint's parameters and that a brand
// with nothing read yet is told why.
func TestTopPostsOverTheAPI(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	for _, tt := range []struct {
		query  string
		status int
		code   string
	}{
		{"", http.StatusUnprocessableEntity, "brand_required"},
		{"?brand=" + c.brand + "&days=week", http.StatusUnprocessableEntity, "days_invalid"},
		{"?brand=" + c.brand + "&count=11", http.StatusUnprocessableEntity, "count_invalid"},
		{"?brand=" + c.brand, http.StatusUnprocessableEntity, "no_top_posts"},
		{"?brand=brand_01aaaaaaaaaaaaaaaaaaaaaaaa", http.StatusNotFound, ""},
	} {
		status, body := c.json(http.MethodGet, "/v1/newsletters/top_posts"+tt.query, nil)
		if status != tt.status || tt.code != "" && body["code"] != tt.code {
			t.Errorf("GET top_posts%s: %d %v, want %d %s", tt.query, status, body, tt.status, tt.code)
		}
	}
}

// TestNewsletterTemplatesOverTheAPI checks a caller makes a newsletter
// template, previews it, and creates issues from it with its own data.
func TestNewsletterTemplatesOverTheAPI(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	src := map[string]any{"brand": c.brand, "key": "weekly", "kind": "newsletter", "subject": "Week of {{.week}}", "preview_text": "What shipped",
		"body":      "# Week of {{md .week}}",
		"variables": map[string]any{"type": "object", "required": []string{"week"}, "properties": map[string]any{"week": map[string]any{"type": "string"}}},
		"examples":  []any{map[string]any{"week": "May 4"}}}
	status, tpl := c.json(http.MethodPost, "/v1/templates", src)
	latest, _ := tpl["latest"].(map[string]any)
	if status != http.StatusCreated || tpl["kind"] != "newsletter" || latest["subject"] != "Week of {{.week}}" {
		t.Fatalf("create: %d %v", status, tpl)
	}
	if status, body := c.json(http.MethodPost, "/v1/templates", map[string]any{"brand": c.brand, "key": "nosubject", "kind": "newsletter", "body": "x"}); status != http.StatusUnprocessableEntity ||
		!strings.Contains(func() string { b, _ := json.Marshal(body); return string(b) }(), "subject_missing") {
		t.Errorf("no subject: %d %v", status, body)
	}
	status, list := c.json(http.MethodGet, "/v1/templates?brand="+c.brand+"&kind=newsletter", nil)
	if data, _ := list["data"].([]any); status != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["key"] != "weekly" {
		t.Fatalf("list by kind: %d %v", status, list)
	}
	status, prev := c.json(http.MethodPost, "/v1/templates/preview", map[string]any{"brand": c.brand, "template": "weekly", "data": map[string]any{"week": "May 11"}})
	if issue, _ := prev["issue"].(map[string]any); status != http.StatusOK || issue["subject"] != "Week of May 11" || issue["body"] != "# Week of May 11" {
		t.Fatalf("preview: %d %v", status, prev)
	}
	status, is := c.json(http.MethodPost, "/v1/newsletters", map[string]any{"brand": c.brand, "template": "weekly", "data": map[string]any{"week": "May 18"}})
	if status != http.StatusCreated || is["subject"] != "Week of May 18" || is["template"] != tpl["id"] || is["template_version"] != float64(1) {
		t.Fatalf("issue from the template: %d %v", status, is)
	}
	if status, body := c.json(http.MethodPost, "/v1/newsletters", map[string]any{"brand": c.brand, "template": "weekly", "subject": "x",
		"data": map[string]any{"week": "May 18"}}); status != http.StatusUnprocessableEntity || body["code"] != "content_conflict" {
		t.Errorf("template and subject: %d %v", status, body)
	}
}

// TestMailConnectionsOverTheAPI checks a sender returns its connection, a
// second sender joins it by ID, the connection lists its audiences and
// senders count, is renamed, and is refused deletion while used.
func TestMailConnectionsOverTheAPI(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	status, acct := c.json(http.MethodPost, "/v1/mail_accounts", map[string]any{"brand": c.brand, "provider": "sandbox",
		"from_name": "Brand", "from_email": "news@brand.example"})
	conn, _ := acct["connection"].(string)
	if status != http.StatusCreated || !strings.HasPrefix(conn, "mailconn_") {
		t.Fatalf("connect: %d %v", status, acct)
	}
	status, second := c.json(http.MethodPost, "/v1/mail_accounts", map[string]any{"brand": c.brand, "connection": conn,
		"from_name": "Brand updates", "from_email": "updates@brand.example"})
	if status != http.StatusCreated || second["connection"] != conn || second["provider"] != "sandbox" {
		t.Fatalf("a second sender: %d %v", status, second)
	}
	if status, got := c.json(http.MethodPost, "/v1/mail_accounts", map[string]any{"brand": c.brand, "connection": "nope",
		"from_name": "x", "from_email": "x@brand.example"}); status != http.StatusBadRequest {
		t.Fatalf("a malformed connection: %d %v", status, got)
	}
	status, got := c.json(http.MethodGet, "/v1/mail_connections/"+conn, nil)
	if status != http.StatusOK || got["object"] != "mail_connection" || got["senders"].(float64) != 2 || got["credentials"] != nil {
		t.Fatalf("get: %d %v", status, got)
	}
	status, list := c.json(http.MethodGet, "/v1/mail_connections", nil)
	if data, _ := list["data"].([]any); status != http.StatusOK || len(data) != 1 {
		t.Fatalf("list: %d %v", status, list)
	}
	status, aud := c.json(http.MethodGet, "/v1/mail_connections/"+conn+"/audiences", nil)
	if data, _ := aud["data"].([]any); status != http.StatusOK || len(data) != 3 {
		t.Fatalf("audiences: %d %v", status, aud)
	}
	status, got = c.json(http.MethodPost, "/v1/mail_connections/"+conn, map[string]any{"name": "Brand's sandbox"})
	if status != http.StatusOK || got["name"] != "Brand's sandbox" {
		t.Fatalf("rename: %d %v", status, got)
	}
	if status, got := c.json(http.MethodDelete, "/v1/mail_connections/"+conn, nil); status != http.StatusConflict || got["code"] != "mail_connection_in_use" {
		t.Fatalf("deleting a connection in use: %d %v", status, got)
	}
	status, made := c.json(http.MethodPost, "/v1/mail_connections", map[string]any{"provider": "sandbox", "fields": map[string]string{}})
	if status != http.StatusCreated || made["name"] != "Sandbox" || made["senders"].(float64) != 0 {
		t.Fatalf("create: %d %v", status, made)
	}
	if status, got := c.json(http.MethodDelete, "/v1/mail_connections/"+made["id"].(string), nil); status != http.StatusOK || got["deleted"] != true {
		t.Fatalf("delete: %d %v", status, got)
	}
}
