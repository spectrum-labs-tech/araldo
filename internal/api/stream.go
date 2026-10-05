// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// The event stream (ADR 0012): the org's events in the credential's mode,
// as server-sent events, for araldo listen (like stripe listen).

const (
	// streamPoll is how often the stream looks for new events.
	streamPoll = time.Second
	// streamHeartbeat keeps proxies from closing a quiet stream.
	streamHeartbeat = 15 * time.Second
	// streamBatch bounds the events read at a time.
	streamBatch = 100
	// streamLookback is how far behind the newest event sent the stream
	// reads again. An event's ID is made when it is emitted, not when its
	// transaction commits, so an event can appear after a later one was
	// sent; re-reading the last seconds, skipping what was sent, finds it.
	streamLookback = 10 * time.Second
)

// streamReader reads the events a stream has not sent, in ID order.
type streamReader struct {
	h *Handler
	// floor is where the stream started: nothing at or before it is sent.
	floor, cursor uuid.UUID
	sent          map[uuid.UUID]bool
}

func (sr *streamReader) next(ctx context.Context, a core.Actor) ([]*model.Event, error) {
	low := id.Before(id.Time(sr.cursor).Add(-streamLookback))
	if bytes.Compare(low[:], sr.floor[:]) < 0 {
		low = sr.floor
	}
	var out []*model.Event
	for {
		page, err := sr.h.svc.EventsSince(ctx, a, low, streamBatch)
		if err != nil {
			return nil, err
		}
		for _, e := range page {
			if !sr.sent[e.ID] {
				out = append(out, e)
			}
		}
		if len(page) < streamBatch {
			break
		}
		low = page[len(page)-1].ID
	}
	return out, nil
}

// mark records e as sent, and forgets what fell out of the lookback.
func (sr *streamReader) mark(e *model.Event) {
	sr.sent[e.ID] = true
	if bytes.Compare(e.ID[:], sr.cursor[:]) > 0 {
		sr.cursor = e.ID
		edge := id.Time(sr.cursor).Add(-2 * streamLookback)
		for u := range sr.sent {
			if id.Time(u).Before(edge) {
				delete(sr.sent, u)
			}
		}
	}
}

func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var types []string
	for t := range strings.SplitSeq(r.URL.Query().Get("types"), ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if !slices.Contains(core.EventTypes, t) {
			return badRequest("parameter_invalid", "types", "Unknown event type %q: the types are the EventType enum in /v1/openapi.yaml.", t)
		}
		types = append(types, t)
	}
	// A reconnecting client resumes after the last event it saw (the SSE
	// Last-Event-ID header, or starting_after); otherwise the stream starts now.
	cursor := id.Before(time.Now())
	if last := first(r.Header.Get("Last-Event-ID"), r.URL.Query().Get("starting_after")); last != "" {
		u, err := id.Parse(id.Event, last)
		if err != nil {
			return badRequest("parameter_invalid", "starting_after", "%q is not an event ID.", last)
		}
		cursor = u
	}
	// The first read also checks the credential may read events, while a
	// refusal can still be a problem response.
	sr := &streamReader{h: h, floor: cursor, cursor: cursor, sent: map[uuid.UUID]bool{}}
	evs, err := sr.next(r.Context(), a)
	if err != nil {
		return err
	}
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n: connected\n\n")
	_ = rc.Flush()
	quiet, checked := time.Now(), time.Now()
	ticker := time.NewTicker(streamPoll)
	defer ticker.Stop()
	for {
		for _, e := range evs {
			sr.mark(e)
			if len(types) > 0 && !slices.Contains(types, e.Type) {
				continue
			}
			if err := writeEvent(w, e.Type, core.ViewEvent(e)); err != nil {
				return nil //nolint:nilerr // the client went away: the stream is over
			}
			quiet = time.Now()
		}
		if time.Since(quiet) >= streamHeartbeat {
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil //nolint:nilerr // the client went away
			}
			quiet = time.Now()
		}
		_ = rc.Flush()
		select {
		case <-r.Context().Done():
			return nil
		case <-ticker.C:
		}
		// A stream lasts as long as its credential: a revoked key or token,
		// a removed member or a suspended org ends it, and the client's
		// reconnection is refused with the reason. A role change applies.
		if time.Since(checked) >= h.StreamRecheck {
			if a, err = h.authenticate(r, false); err != nil {
				return nil //nolint:nilerr // headers are sent: ending the stream is the answer
			}
			checked = time.Now()
		}
		if evs, err = sr.next(r.Context(), a); err != nil {
			return nil //nolint:nilerr // headers are sent: ending the stream is the answer; the client reconnects
		}
	}
}

// writeEvent writes one server-sent event.
func writeEvent(w http.ResponseWriter, typ string, v core.EventView) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", v.ID, typ, b) //nolint:gosec // G705: text/event-stream of JSON, never HTML
	return err
}

func first(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
