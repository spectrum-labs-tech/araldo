// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
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
