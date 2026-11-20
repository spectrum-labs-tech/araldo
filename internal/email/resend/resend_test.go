// SPDX-License-Identifier: AGPL-3.0-or-later

package resend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakeResend answers for API key "k" (full access) and "s" (sending
// access): three domains, 120 segments (two pages), and broadcasts it
// keeps, listed oldest first a page of 100 at a time.
type fakeResend struct {
	mu         sync.Mutex
	broadcasts map[string]map[string]any
	order      []string
	deleted    []string
	tests      []map[string]any
	metricsQ   string
	next       int
}

func (f *fakeResend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fail := func(status int, name, msg string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": status, "name": name, "message": msg})
	}
	switch r.Header.Get("Authorization") {
	case "Bearer k":
	case "Bearer s":
		if r.URL.Path != "/emails" {
			fail(http.StatusUnauthorized, "restricted_api_key", "This API key is restricted to only send emails.")
			return
		}
	default:
		fail(http.StatusBadRequest, "validation_error", "API key is invalid")
		return
	}
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	q := r.URL.Query()
	page := func(items []map[string]any) {
		start := 0
		if after := q.Get("after"); after != "" {
			for i, it := range items {
				if it["id"] == after {
					start = i + 1
				}
			}
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		end := min(start+limit, len(items))
		write(map[string]any{"object": "list", "has_more": end < len(items), "data": items[start:end]})
	}
	switch path := r.URL.Path; {
	case path == "/domains":
		page([]map[string]any{
			{"id": "d-1", "name": "news.brand.example", "status": "verified"},
			{"id": "d-2", "name": "pending.brand.example", "status": "pending"},
			{"id": "d-3", "name": "brand.example", "status": "verified"},
		})
	case path == "/segments":
		var segs []map[string]any
		for i := range 120 {
			segs = append(segs, map[string]any{"id": fmt.Sprintf("seg-%d", i+1), "name": fmt.Sprintf("Segment %d", i+1), "created_at": "2026-10-01 09:00:00.000+00"})
		}
		page(segs)
	case path == "/broadcasts" && r.Method == http.MethodPost:
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.next++
		id := fmt.Sprintf("b-%d", f.next)
		b["id"], b["status"] = id, "scheduled"
		f.broadcasts[id] = b
		f.order = append(f.order, id)
		write(map[string]any{"object": "broadcast", "id": id})
	case path == "/broadcasts":
		var items []map[string]any
		for _, id := range f.order {
			if b, ok := f.broadcasts[id]; ok {
				items = append(items, map[string]any{"id": id, "name": b["name"], "status": b["status"]})
			}
		}
		page(items)
	case path == "/emails/metrics":
		f.metricsQ = r.URL.RawQuery
		write(map[string]any{"object": "metrics", "totals": map[string]any{"sent": 150, "delivered": 147, "unique_opened": 60,
			"opened": 90, "unique_clicked": 12, "clicked": 20, "unsubscribed": 1, "bounced": 3, "complained": 1}})
	case strings.HasPrefix(path, "/broadcasts/"):
		rest := strings.TrimPrefix(path, "/broadcasts/")
		id, action, _ := strings.Cut(rest, "/")
		b, ok := f.broadcasts[id]
		if !ok {
			fail(http.StatusNotFound, "not_found", "Broadcast not found")
			return
		}
		switch {
		case action == "cancel":
			if b["status"] != "scheduled" && b["status"] != "queued" {
				fail(http.StatusUnprocessableEntity, "validation_error", "Only scheduled or queued broadcasts can be canceled")
				return
			}
			b["status"], b["scheduled_at"] = "draft", nil
			write(map[string]any{"object": "broadcast", "id": id})
		case action == "send":
			if b["status"] != "draft" {
				fail(http.StatusUnprocessableEntity, "validation_error", "Only draft broadcasts can be sent")
				return
			}
			var s map[string]any
			_ = json.NewDecoder(r.Body).Decode(&s)
			b["status"], b["scheduled_at"] = "scheduled", s["scheduled_at"]
			write(map[string]any{"id": id})
		case r.Method == http.MethodDelete:
			delete(f.broadcasts, id)
			f.deleted = append(f.deleted, id)
			write(map[string]any{"object": "broadcast", "id": id, "deleted": true})
		default:
			out := map[string]any{"object": "broadcast", "id": id, "status": b["status"], "created_at": "2026-10-04 09:00:00.000+00",
				"scheduled_at": b["scheduled_at"], "sent_at": nil}
			if b["status"] == "sent" {
				out["sent_at"] = "2026-10-05 08:00:00.120+00"
			}
			write(out)
		}
	case path == "/emails":
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		f.tests = append(f.tests, m)
		write(map[string]any{"id": "e-1"})
	default:
		fail(http.StatusNotFound, "not_found", "The requested endpoint does not exist.")
	}
}

func setup(t *testing.T) (*fakeResend, *Mailer, platform.Credentials) {
	t.Helper()
	f := &fakeResend{broadcasts: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	m := New(srv.Client())
	m.API = srv.URL
	return f, m, platform.Credentials{"api_key": "k"}
}

func kindOf(err error) platform.Kind {
	var pe *platform.Error
	if errors.As(err, &pe) {
		return pe.Kind
	}
	return ""
}

func TestVerify(t *testing.T) {
	t.Parallel()
	_, m, c := setup(t)
	tests := []struct {
		name  string
		creds platform.Credentials
		from  string
		kind  platform.Kind
		acct  email.Account
	}{
		{"an address on a verified domain, any case", c, "News@News.Brand.example", "", email.Account{ExternalID: "d-1", Name: "news.brand.example"}},
		{"the parent domain, verified on its own", c, "hello@brand.example", "", email.Account{ExternalID: "d-3", Name: "brand.example"}},
		{"a domain still pending", c, "news@pending.brand.example", platform.Rejected, email.Account{}},
		{"a domain not in the account", c, "news@elsewhere.example", platform.Rejected, email.Account{}},
		{"a wrong key", platform.Credentials{"api_key": "nope"}, "news@news.brand.example", platform.AuthRevoked, email.Account{}},
		{"a sending-access key", platform.Credentials{"api_key": "s"}, "news@news.brand.example", platform.AuthRevoked, email.Account{}},
		{"no key", platform.Credentials{}, "news@news.brand.example", platform.Rejected, email.Account{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			acct, err := m.Verify(t.Context(), tt.creds, email.Address{Name: "Brand", Email: tt.from})
			if kindOf(err) != tt.kind || (err == nil) != (tt.kind == "") {
				t.Fatalf("got %v, want kind %q", err, tt.kind)
			}
			if acct != tt.acct {
				t.Fatalf("account %+v, want %+v", acct, tt.acct)
			}
		})
	}
}

func TestVerifySaysWhatTheKeyLacks(t *testing.T) {
	t.Parallel()
	_, m, _ := setup(t)
	_, err := m.Verify(t.Context(), platform.Credentials{"api_key": "s"}, email.Address{Email: "news@news.brand.example"})
	if err == nil || !strings.Contains(err.Error(), "full access") {
		t.Fatalf("a sending-access key should be told to use full access: %v", err)
	}
}

func TestAudiences(t *testing.T) {
	t.Parallel()
	_, m, c := setup(t)
	got, err := m.Audiences(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 120 {
		t.Fatalf("got %d audiences, want 120 segments over two pages", len(got))
	}
	if got[0] != (email.Audience{ID: "seg-1", Name: "Segment 1", Kind: "segment", Size: -1}) || got[119].ID != "seg-120" {
		t.Fatalf("first %+v, last %+v", got[0], got[119])
	}
}

func TestScheduleRefusesAudiences(t *testing.T) {
	t.Parallel()
	f, m, c := setup(t)
	msg := email.Message{Name: "October", Subject: "What shipped", HTML: "<p>Hi</p>", From: email.Address{Email: "news@news.brand.example"}, Tag: "nldel_1"}
	tests := []struct {
		name      string
		audiences []string
		code      string
	}{
		{"none", nil, "audience_missing"},
		{"two segments", []string{"seg-1", "seg-2"}, "audience_multiple"},
	}
	// Cleanup runs once the parallel subtests are done.
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.broadcasts) != 0 {
			t.Errorf("nothing should be created: %v", f.broadcasts)
		}
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := m.Schedule(t.Context(), c, msg, tt.audiences, time.Now().Add(time.Hour))
			var pe *platform.Error
			if !errors.As(err, &pe) || pe.Kind != platform.Rejected || pe.Code != tt.code {
				t.Fatalf("got %v, want rejected %s", err, tt.code)
			}
		})
	}
}

func TestCampaignLifecycle(t *testing.T) {
	t.Parallel()
	f, m, c := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.FixedZone("CET", 3600))
	msg := email.Message{Name: "What shipped · iss_1", Subject: "What shipped", PreviewText: "Three things", HTML: "<p>Hi</p>", Text: "Hi",
		From: email.Address{Name: "Brand, Inc.", Email: "news@news.brand.example"}, ReplyTo: "hello@brand.example", Tag: "nldel_1"}

	// Older broadcasts fill the first page of the list, so Find has to page.
	for i := range 105 {
		if _, err := m.Schedule(ctx, c, email.Message{Name: "old", Tag: fmt.Sprintf("nldel_old%d", i), From: msg.From}, []string{"seg-1"}, at); err != nil {
			t.Fatal(err)
		}
	}
	id, err := m.Schedule(ctx, c, msg, []string{"seg-7"}, at)
	if err != nil {
		t.Fatal(err)
	}
	sent := f.broadcasts[id]
	if sent["scheduled_at"] != "2026-10-05T08:00:00Z" || sent["send"] != true || sent["segment_id"] != "seg-7" ||
		sent["name"] != "What shipped · iss_1 · nldel_1" || sent["from"] != `"Brand, Inc." <news@news.brand.example>` ||
		sent["reply_to"] != "hello@brand.example" || sent["text"] != "Hi" || sent["html"] != "<p>Hi</p>" {
		t.Fatalf("created %s: %v", id, sent)
	}

	if found, err := m.Find(ctx, c, "nldel_1"); err != nil || found != id {
		t.Fatalf("find: %q, %v; want %q", found, err, id)
	}
	if found, err := m.Find(ctx, c, "nldel_2"); err != nil || found != "" {
		t.Fatalf("find a broadcast never made: %q, %v", found, err)
	}

	if err := m.Reschedule(ctx, c, id, at.Add(time.Hour)); err != nil || f.broadcasts[id]["scheduled_at"] != "2026-10-05T09:00:00Z" ||
		f.broadcasts[id]["status"] != "scheduled" {
		t.Fatalf("reschedule: %v, %v", err, f.broadcasts[id])
	}
	// A move that failed half way left a draft: moving it back sends it again.
	f.broadcasts[id]["status"] = "draft"
	if err := m.Reschedule(ctx, c, id, at); err != nil || f.broadcasts[id]["status"] != "scheduled" ||
		f.broadcasts[id]["scheduled_at"] != "2026-10-05T08:00:00Z" {
		t.Fatalf("reschedule a draft: %v, %v", err, f.broadcasts[id])
	}
	if cp, err := m.Campaign(ctx, c, id); err != nil || cp.Status != email.CampaignScheduled || cp.SentAt != nil {
		t.Fatalf("a scheduled broadcast: %+v, %v", cp, err)
	}
	f.broadcasts[id]["status"] = "queued"
	if cp, err := m.Campaign(ctx, c, id); err != nil || cp.Status != email.CampaignSending {
		t.Fatalf("a queued broadcast: %+v, %v", cp, err)
	}
	if err := m.Reschedule(ctx, c, id, at); kindOf(err) != platform.Rejected {
		t.Fatalf("moving a broadcast that is sending: %v", err)
	}

	f.broadcasts[id]["status"] = "sent"
	cp, err := m.Campaign(ctx, c, id)
	if err != nil || cp.Status != email.CampaignSent || cp.SentAt == nil || !cp.SentAt.Equal(time.Date(2026, 10, 5, 8, 0, 0, 120e6, time.UTC)) {
		t.Fatalf("a sent broadcast: %+v, %v", cp, err)
	}
	want := email.Results{Recipients: 150, Delivered: 147, Opens: 60, Clicks: 12, Unsubscribes: 1, Bounces: 3, Complaints: 1}
	if cp.Results != want {
		t.Fatalf("results %+v, want %+v (unique opens and clicks)", cp.Results, want)
	}
	if !strings.Contains(f.metricsQ, "broadcast_id="+id) || !strings.Contains(f.metricsQ, "start_date=2026-10-04") {
		t.Fatalf("metrics asked for %q, want this broadcast from the day before it was sent", f.metricsQ)
	}

	f.broadcasts[id]["status"] = "scheduled"
	if err := m.Cancel(ctx, c, id); err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(ctx, c, id); err != nil {
		t.Fatalf("canceling a broadcast already gone: %v", err)
	}
	if cp, err := m.Campaign(ctx, c, id); err != nil || cp.Status != email.CampaignStopped {
		t.Fatalf("a deleted broadcast: %+v, %v", cp, err)
	}
}

func TestCampaignStatuses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status string
		want   email.CampaignStatus
	}{
		{"draft", email.CampaignStopped},
		{"scheduled", email.CampaignScheduled},
		{"queued", email.CampaignSending},
		{"canceled", email.CampaignStopped},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()
			f, m, c := setup(t)
			f.broadcasts["b-1"] = map[string]any{"status": tt.status}
			cp, err := m.Campaign(t.Context(), c, "b-1")
			if err != nil || cp.Status != tt.want || cp.Results != (email.Results{}) {
				t.Fatalf("got %+v, %v; want %s and no results", cp, err, tt.want)
			}
		})
	}
}

func TestRevokedKey(t *testing.T) {
	t.Parallel()
	_, m, _ := setup(t)
	bad := platform.Credentials{"api_key": "nope"}
	if _, err := m.Campaign(t.Context(), bad, "b-1"); kindOf(err) != platform.AuthRevoked {
		t.Fatalf("a key Resend no longer knows answers 400 there, which is still a revoked key: %v", err)
	}
}

func TestSendTest(t *testing.T) {
	t.Parallel()
	f, m, c := setup(t)
	msg := email.Message{Subject: "[Test] What shipped", HTML: "<p>Hi</p>", Text: "Hi", From: email.Address{Name: "Brand", Email: "news@news.brand.example"}}
	if err := m.SendTest(t.Context(), c, msg, []string{"me@brand.example", "you@brand.example"}); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(f.tests[0]["to"])
	if string(got) != `["me@brand.example","you@brand.example"]` || f.tests[0]["text"] != "Hi" || f.tests[0]["from"] != `"Brand" <news@news.brand.example>` {
		t.Fatalf("test send %v", f.tests[0])
	}
	if _, ok := f.tests[0]["reply_to"]; ok {
		t.Fatal("no reply-to was given")
	}
	// A sending-access key is enough for a test send.
	if err := m.SendTest(t.Context(), platform.Credentials{"api_key": "s"}, msg, []string{"me@brand.example"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SendTest(t.Context(), c, msg, make([]string, 51)); kindOf(err) != platform.Rejected {
		t.Fatalf("51 addresses: %v", err)
	}
}
