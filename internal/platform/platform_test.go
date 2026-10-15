// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"context"
	"testing"
	"time"
)

type fakeAdapter struct {
	idempotent bool
}

func (fakeAdapter) Provider() Provider { return Sandbox }
func (fakeAdapter) Rules() Rules       { return Rules{} }
func (fakeAdapter) Fields() []Field    { return nil }
func (fakeAdapter) Verify(context.Context, Credentials) (Account, error) {
	return Account{}, nil
}
func (fakeAdapter) Publish(context.Context, Credentials, Payload, func(RemoteRef) error) (Result, error) {
	return Result{}, nil
}
func (a fakeAdapter) Idempotent() bool { return a.idempotent }

type windowed struct{ fakeAdapter }

func (windowed) IdempotencyWindow() time.Duration { return time.Hour }

func TestRetryUncertain(t *testing.T) {
	t.Parallel()
	first := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		a    Adapter
		age  time.Duration
		want bool
	}{
		{"not idempotent", fakeAdapter{}, time.Minute, false},
		{"idempotent for ever", fakeAdapter{idempotent: true}, 48 * time.Hour, true},
		{"within the window", windowed{fakeAdapter{idempotent: true}}, 59 * time.Minute, true},
		{"after the window", windowed{fakeAdapter{idempotent: true}}, time.Hour, false},
	}
	for _, tt := range tests {
		if got := RetryUncertain(tt.a, first, first.Add(tt.age)); got != tt.want {
			t.Errorf("%s: RetryUncertain = %v, want %v", tt.name, got, tt.want)
		}
	}
}
