// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
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
)

func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var types []string
	for t := range strings.SplitSeq(r.URL.Query().Get("types"), ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if !slices.Contains(core.EventTypes, t) {
			return badRequest("parameter_invalid", "types", "Unknown event type %q.", t)
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
	evs, err := h.svc.EventsSince(r.Context(), a, cursor, streamBatch)
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
	quiet := time.Now()
	ticker := time.NewTicker(streamPoll)
	defer ticker.Stop()
	for {
		for _, e := range evs {
			cursor = e.ID
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
		if evs, err = h.svc.EventsSince(r.Context(), a, cursor, streamBatch); err != nil {
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
