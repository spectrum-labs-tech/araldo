// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// TestPublishAttemptsAreSpans checks a publish attempt is a span with the
// provider, mode, attempt and outcome, and nothing of the post: not its
// text, not an ID (ADR 0014).
func TestPublishAttemptsAreSpans(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	rec := tracetest.NewSpanRecorder()
	w.s.Trace(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	ctx, stop := context.WithCancel(t.Context())
	const text = "Unannounced launch: Atlas ships Friday"
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: text},
		PublishAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	// Due in an hour by the clock, but now by this service's: no other
	// test's publisher, on the real clock, takes it.
	w.s.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	done := make(chan struct{})
	go func() { _ = core.RunPublisherOrg(ctx, w.s, "traces-"+uuid.NewString()[:8], w.org.ID); close(done) }()
	defer func() { stop(); <-done }()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		got, err := w.s.Post(ctx, w.owner, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Targets[0].Status == model.TargetPublished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the post stayed %s", got.Targets[0].Status)
		}
	}

	var publish sdktrace.ReadOnlySpan
	for _, sp := range rec.Ended() {
		if sp.Name() == "publish" {
			publish = sp
		}
	}
	if publish == nil {
		t.Fatal("no publish span")
	}
	attrs := map[string]string{}
	for _, kv := range publish.Attributes() {
		v := kv.Value.String()
		attrs[string(kv.Key)] = v
		if strings.Contains(v, "Atlas") || strings.Contains(v, p.ID.String()) {
			t.Errorf("%s carries the post: %s", kv.Key, v)
		}
	}
	if attrs["araldo.provider"] != "sandbox" || attrs["araldo.mode"] != "test" || attrs["araldo.attempt"] != "1" ||
		attrs["araldo.outcome"] != "published" || attrs["araldo.error_kind"] != "none" || len(attrs) != 5 {
		t.Fatalf("attributes: %v", attrs)
	}
}
