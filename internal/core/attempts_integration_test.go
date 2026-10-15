// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"errors"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// TestFinishedAttemptIsKept checks an attempt's recorded outcome cannot be
// rewritten: not once it has finished, and never from another org.
func TestFinishedAttemptIsKept(t *testing.T) {
	t.Parallel()
	w, other := newWorld(t), newWorld(t)
	ctx := t.Context()
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "attempts"}})
	if err != nil {
		t.Fatal(err)
	}
	tg := settle(t, w, p.ID).Targets[0]
	attempts, err := w.s.Attempts(ctx, w.owner, tg.ID)
	if err != nil || len(attempts) == 0 {
		t.Fatalf("attempts: %v, %v", attempts, err)
	}
	a, st := attempts[0], open(t)
	for _, tt := range []struct {
		name string
		org  *model.Org
	}{{"from another org", other.org}, {"once finished", w.org}} {
		if err := st.FinishAttempt(ctx, tt.org.ID, a.ID, time.Now(), "failed", "rewritten", "rewritten"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("finishing the attempt %s: %v, want not found", tt.name, err)
		}
	}
	again, err := w.s.Attempts(ctx, w.owner, tg.ID)
	if err != nil || again[0].Outcome != a.Outcome || again[0].ErrorCode == "rewritten" {
		t.Errorf("the attempt changed: %+v, %v", again, err)
	}
}
