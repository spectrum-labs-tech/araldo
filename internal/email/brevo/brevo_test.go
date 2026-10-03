// SPDX-License-Identifier: AGPL-3.0-or-later

package brevo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// fakeBrevo answers for API key k: one active sender, one authenticated
// domain, 60 lists (two pages), one segment, and campaigns it keeps.
type fakeBrevo struct {
	mu        sync.Mutex
	campaigns map[int64]map[string]any
	deleted   []string
	tests     []map[string]any
	next      int64
}

func (f *fakeBrevo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("api-key") != "k" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized","message":"Key not found"}`))
		return
	}
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	q := r.URL.Query()
	switch path := r.URL.Path; {
	case path == "/account":
		write(map[string]any{"email": "owner@brand.example", "companyName": "Brand"})
	case path == "/senders":
		write(map[string]any{"senders": []map[string]any{{"email": "News@brand.example", "active": true}, {"email": "old@brand.example", "active": false}}})
	case path == "/senders/domains":
		write(map[string]any{"domains": []map[string]any{{"domain_name": "news.brand.example", "authenticated": true, "verified": true}}})
	case path == "/contacts/lists":
		var lists []map[string]any
		for i := 0; i < 60; i++ {
			lists = append(lists, map[string]any{"id": i + 1, "name": fmt.Sprintf("List %d", i+1), "uniqueSubscribers": 10 * (i + 1)})
		}
		offset := 0
		_, _ = fmt.Sscan(q.Get("offset"), &offset)
		end := min(offset+50, len(lists))
		write(map[string]any{"lists": lists[offset:end], "count": len(lists)})
	case path == "/contacts/segments":
		write(map[string]any{"segments": []map[string]any{{"id": 7, "segmentName": "Engaged"}}, "count": 1})
	case path == "/emailCampaigns" && r.Method == http.MethodPost:
		var c map[string]any
		_ = json.NewDecoder(r.Body).Decode(&c)
		f.next++
		c["status"] = "queued"
		f.campaigns[f.next] = c
		w.WriteHeader(http.StatusCreated)
		write(map[string]any{"id": f.next})
	case path == "/emailCampaigns":
		var list []map[string]any
		for id := f.next; id > 0; id-- {
			if c, ok := f.campaigns[id]; ok {
				list = append(list, map[string]any{"id": id, "tag": c["tag"]})
			}
		}
		write(map[string]any{"campaigns": list, "count": len(list)})
	case strings.HasPrefix(path, "/emailCampaigns/"):
		var id int64
		_, _ = fmt.Sscan(strings.TrimPrefix(path, "/emailCampaigns/"), &id)
		c, ok := f.campaigns[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"document_not_found","message":"Campaign does not exist"}`))
			return
		}
		switch r.Method {
		case http.MethodPut:
			var u map[string]any
			_ = json.NewDecoder(r.Body).Decode(&u)
			c["scheduledAt"] = u["scheduledAt"]
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			delete(f.campaigns, id)
			f.deleted = append(f.deleted, fmt.Sprint(id))
			w.WriteHeader(http.StatusNoContent)
		default:
			out := map[string]any{"id": id, "status": c["status"]}
			if c["status"] == "sent" {
				out["sentDate"] = "2026-10-05T09:00:00Z"
				out["statistics"] = map[string]any{"globalStats": map[string]any{}, "campaignStats": []map[string]any{
					{"sent": 100, "delivered": 98, "uniqueViews": 40, "uniqueClicks": 9, "unsubscriptions": 1, "hardBounces": 1, "softBounces": 1},
					{"sent": 50, "delivered": 50, "uniqueViews": 20, "uniqueClicks": 3, "complaints": 1},
				}}
			}
			write(out)
		}
	case path == "/smtp/email":
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		f.tests = append(f.tests, m)
		w.WriteHeader(http.StatusCreated)
		write(map[string]any{"messageId": "<1@brevo>"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*fakeBrevo, *Mailer, platform.Credentials) {
	t.Helper()
	f := &fakeBrevo{campaigns: map[int64]map[string]any{}}
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
	}{
		{"an active sender, any case", c, "news@brand.example", ""},
		{"an address on an authenticated domain", c, "hello@news.brand.example", ""},
		{"an inactive sender", c, "old@brand.example", platform.Rejected},
		{"an unknown domain", c, "news@elsewhere.example", platform.Rejected},
		{"a wrong key", platform.Credentials{"api_key": "nope"}, "news@brand.example", platform.AuthRevoked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			acct, err := m.Verify(t.Context(), tt.creds, email.Address{Name: "Brand", Email: tt.from})
			if kindOf(err) != tt.kind || (err == nil) != (tt.kind == "") {
				t.Fatalf("got %v, want kind %q", err, tt.kind)
			}
			if err == nil && (acct.ExternalID != "owner@brand.example" || acct.Name != "Brand") {
				t.Fatalf("account %+v", acct)
			}
		})
	}
}

func TestAudiences(t *testing.T) {
	t.Parallel()
	_, m, c := setup(t)
	got, err := m.Audiences(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 61 {
		t.Fatalf("got %d audiences, want 60 lists over two pages and a segment", len(got))
	}
	if got[0] != (email.Audience{ID: "list:1", Name: "List 1", Kind: "list", Size: 10}) {
		t.Fatalf("first %+v", got[0])
	}
	if last := got[60]; last != (email.Audience{ID: "segment:7", Name: "Engaged", Kind: "segment", Size: -1}) {
		t.Fatalf("segment %+v", last)
	}
}

func TestCampaignLifecycle(t *testing.T) {
	t.Parallel()
	f, m, c := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.FixedZone("CET", 3600))
	msg := email.Message{Name: "October", Subject: "What shipped", PreviewText: "Three things", HTML: "<p>Hi</p>",
		From: email.Address{Name: "Brand", Email: "news@brand.example"}, ReplyTo: "hello@brand.example", Tag: "nldel_1"}

	if _, err := m.Schedule(ctx, c, msg, []string{"list:3", "people"}, at); kindOf(err) != platform.Rejected {
		t.Fatalf("an audience that is not Brevo's: %v", err)
	}
	if _, err := m.Schedule(ctx, c, msg, nil, at); kindOf(err) != platform.Rejected {
		t.Fatalf("no audience: %v", err)
	}
	id, err := m.Schedule(ctx, c, msg, []string{"list:3", "segment:7"}, at)
	if err != nil {
		t.Fatal(err)
	}
	sent := f.campaigns[1]
	rc, _ := json.Marshal(sent["recipients"])
	if id != "1" || sent["scheduledAt"] != "2026-10-05T08:00:00Z" || string(rc) != `{"listIds":[3],"segmentIds":[7]}` ||
		sent["tag"] != "nldel_1" || sent["replyTo"] != "hello@brand.example" || sent["previewText"] != "Three things" {
		t.Fatalf("created %s: %v", id, sent)
	}

	if found, err := m.Find(ctx, c, "nldel_1"); err != nil || found != "1" {
		t.Fatalf("find: %q, %v", found, err)
	}
	if found, err := m.Find(ctx, c, "nldel_2"); err != nil || found != "" {
		t.Fatalf("find a campaign never made: %q, %v", found, err)
	}

	if err := m.Reschedule(ctx, c, id, at.Add(time.Hour)); err != nil || f.campaigns[1]["scheduledAt"] != "2026-10-05T09:00:00Z" {
		t.Fatalf("reschedule: %v, %v", err, f.campaigns[1]["scheduledAt"])
	}
	if cp, err := m.Campaign(ctx, c, id); err != nil || cp.Status != email.CampaignScheduled {
		t.Fatalf("a queued campaign: %+v, %v", cp, err)
	}
	f.campaigns[1]["status"] = "sent"
	cp, err := m.Campaign(ctx, c, id)
	if err != nil || cp.Status != email.CampaignSent || cp.SentAt == nil {
		t.Fatalf("a sent campaign: %+v, %v", cp, err)
	}
	want := email.Results{Recipients: 150, Delivered: 148, Opens: 60, Clicks: 12, Unsubscribes: 1, Bounces: 2, Complaints: 1}
	if cp.Results != want {
		t.Fatalf("results %+v, want %+v (added up over lists)", cp.Results, want)
	}

	if err := m.Cancel(ctx, c, id); err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(ctx, c, id); err != nil {
		t.Fatalf("canceling a campaign already gone: %v", err)
	}
	if cp, err := m.Campaign(ctx, c, id); err != nil || cp.Status != email.CampaignStopped {
		t.Fatalf("a deleted campaign: %+v, %v", cp, err)
	}
}

func TestSendTest(t *testing.T) {
	t.Parallel()
	f, m, c := setup(t)
	msg := email.Message{Subject: "[Test] What shipped", HTML: "<p>Hi</p>", Text: "Hi", From: email.Address{Name: "Brand", Email: "news@brand.example"}}
	if err := m.SendTest(t.Context(), c, msg, []string{"me@brand.example", "you@brand.example"}); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(f.tests[0]["to"])
	if string(got) != `[{"email":"me@brand.example"},{"email":"you@brand.example"}]` || f.tests[0]["textContent"] != "Hi" {
		t.Fatalf("test send %v", f.tests[0])
	}
	if _, ok := f.tests[0]["replyTo"]; ok {
		t.Fatal("no reply-to was given")
	}
}
