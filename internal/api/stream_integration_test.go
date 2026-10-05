// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/api"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// sseEvent is one server-sent event.
type sseEvent struct{ id, typ, data string }

// stream opens the event stream and sends what arrives on the channel.
func stream(t *testing.T, ctx context.Context, srv *httptest.Server, key, query, lastID string) <-chan sseEvent {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/events/stream"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := srv.Client().Do(req) //nolint:bodyclose // the goroutine reading the stream closes it
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	out := make(chan sseEvent, 16)
	go func() {
		defer resp.Body.Close()
		defer close(out)
		var e sseEvent
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				e.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				e.typ = line[7:]
			case strings.HasPrefix(line, "data: "):
				e.data = line[6:]
			case line == "" && e.id != "":
				out <- e
				e = sseEvent{}
			}
		}
	}()
	return out
}

// next waits for the stream's next event of a type.
func next(t *testing.T, events <-chan sseEvent, typ string) sseEvent {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatalf("the stream ended before a %s", typ)
			}
			if e.typ == typ {
				return e
			}
		case <-timeout:
			t.Fatalf("no %s event arrived", typ)
		}
	}
}

// TestEventStream checks that the stream sends the org's new events as
// they happen, only the types asked for, and resumes after the last one.
func TestEventStream(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	srv := httptest.NewServer(c.h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	all := stream(t, ctx, srv, c.key, "", "")
	only := stream(t, ctx, srv, c.key, "?types=post.canceled", "")
	b, err := id.Parse(id.Brand, c.brand)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.s.CreatePost(ctx, c.owner, core.PostInput{BrandID: b, Content: &model.Content{Body: "streamed"},
		PublishAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	created := next(t, all, "post.created")
	var v struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object struct {
				ID string `json:"id"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(created.data), &v); err != nil || v.ID != created.id || v.Type != "post.created" ||
		v.Data.Object.ID != id.Format(id.Post, p.ID) {
		t.Fatalf("the event: %+v %v\n%s", v, err, created.data)
	}
	if _, err := c.s.CancelPost(ctx, c.owner, p.ID); err != nil {
		t.Fatal(err)
	}
	if e := next(t, only, "post.canceled"); e.id == created.id {
		t.Fatal("the filtered stream sent the created event")
	}
	// Reconnecting after the created event resumes with what came next.
	resumed := stream(t, ctx, srv, c.key, "", created.id)
	next(t, resumed, "post.canceled")

	// Unknown types are refused before the stream starts.
	status, got := c.do(http.MethodGet, "/v1/events/stream?types=post.nope", "", nil, nil)
	if status != http.StatusBadRequest || got["param"] != "types" {
		t.Fatalf("an unknown type: %d %v", status, got)
	}
}

// TestEventStreamEndsWithItsCredential checks an open stream ends once its
// key is revoked, rather than going on for as long as the client stays.
func TestEventStreamEndsWithItsCredential(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	h, ok := c.h.(*api.Handler)
	if !ok {
		t.Fatalf("the handler is a %T", c.h)
	}
	h.StreamRecheck = 100 * time.Millisecond
	srv := httptest.NewServer(h)
	defer srv.Close()
	plain, k, err := c.s.CreateOperatorAPIKey(t.Context(), c.owner, core.APIKeyInput{Name: "streaming"})
	if err != nil {
		t.Fatal(err)
	}
	events := stream(t, t.Context(), srv, plain, "", "")
	if err := c.s.RevokeAPIKey(t.Context(), c.owner, k.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case _, open := <-events:
		if open {
			t.Fatal("an event arrived instead of the stream ending")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived its revoked key")
	}
}

// TestEventStreamSendsLateCommits checks an event committed after a later
// one was sent, with an earlier ID (it was emitted first), still arrives.
func TestEventStreamSendsLateCommits(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	srv := httptest.NewServer(c.h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events := stream(t, ctx, srv, c.key, "", "")
	b, err := id.Parse(id.Brand, c.brand)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // the late event is emitted after the stream starts
	if _, err := c.s.CreatePost(ctx, c.owner, core.PostInput{BrandID: b, Content: &model.Content{Body: "first"},
		PublishAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	sent := next(t, events, "post.created")
	sentID, err := id.Parse(id.Event, sent.id)
	if err != nil {
		t.Fatal(err)
	}
	// An event emitted just before it, whose transaction commits now.
	lateID := id.Before(id.Time(sentID).Add(-200 * time.Millisecond))
	if _, err := shared.Pool().Exec(ctx, `INSERT INTO events (id, org_id, livemode, type, data) VALUES ($1, $2, false, 'post.canceled', '{}')`,
		lateID, c.owner.OrgID); err != nil {
		t.Fatal(err)
	}
	if e := next(t, events, "post.canceled"); e.id != id.Format(id.Event, lateID) {
		t.Fatalf("got %s, want the late event %s", e.id, id.Format(id.Event, lateID))
	}
}
