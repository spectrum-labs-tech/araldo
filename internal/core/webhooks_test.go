// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

func TestJitter(t *testing.T) {
	t.Parallel()
	for _, d := range []time.Duration{0, time.Minute, 8 * time.Hour} {
		for range 100 {
			if got := jitter(d); got < d || got > d+d/10 {
				t.Fatalf("jitter(%v) = %v, want within a tenth above", d, got)
			}
		}
	}
}

// TestEndpointEventsKeepTheURL checks the endpoint an event shows other
// endpoints carries its host, never its URL, whose path may hold a secret.
func TestEndpointEventsKeepTheURL(t *testing.T) {
	t.Parallel()
	w := &model.WebhookEndpoint{ID: id.New(), URL: "https://hooks.example/in/s3cr3t-token?key=also-secret", Status: model.EndpointDisabled}
	b, err := json.Marshal(ViewEndpointEvent(w))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); strings.Contains(got, "s3cr3t") || strings.Contains(got, "also-secret") || !strings.Contains(got, `"host":"hooks.example"`) {
		t.Fatalf("the event's endpoint: %s", got)
	}
}
