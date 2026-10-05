// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"
	"time"
)

type drainKey struct{}

// TestDrain checks that work outlives the stop signal by the grace period,
// so a stopping worker finishes the publishes it has started.
func TestDrain(t *testing.T) {
	t.Parallel()
	const grace = 50 * time.Millisecond
	ended := func(ctx context.Context, within time.Duration) bool {
		select {
		case <-ctx.Done():
			return true
		case <-time.After(within):
			return false
		}
	}

	t.Run("work continues for the grace period after the stop", func(t *testing.T) {
		t.Parallel()
		ctx, stopSignal := context.WithCancel(context.WithValue(t.Context(), drainKey{}, "kept"))
		work, stop := drain(ctx, grace)
		defer stop()
		if work.Value(drainKey{}) != "kept" {
			t.Fatal("work lost the context's values")
		}
		stopSignal()
		if ended(work, grace/2) {
			t.Fatal("work ended with the stop signal, not after the grace period")
		}
		if !ended(work, 5*grace) {
			t.Fatal("work outlived the grace period")
		}
	})

	t.Run("stop ends work at once", func(t *testing.T) {
		t.Parallel()
		work, stop := drain(t.Context(), time.Hour)
		stop()
		if !ended(work, grace) {
			t.Fatal("work went on after stop")
		}
	})
}
