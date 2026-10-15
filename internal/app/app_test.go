// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/spectrum-labs-tech/araldo/internal/telemetry"
)

// TestTelemetryThatCannotStartIsSkipped checks a telemetry setup that
// fails leaves Araldo running without metrics and traces, and says why.
func TestTelemetryThatCannotStartIsSkipped(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	started := telemetry.Disabled()
	for _, tt := range []struct {
		name   string
		start  func() (*telemetry.Telemetry, error)
		want   *telemetry.Telemetry // nil: a disabled one
		warned bool
	}{
		{"it starts", func() (*telemetry.Telemetry, error) { return started, nil }, started, false},
		{"it cannot", func() (*telemetry.Telemetry, error) { return nil, errors.New("unknown exporter \"bogus\"") }, nil, true},
	} {
		logs.Reset()
		got := telemetryOrNone(t.Context(), log, tt.start)
		switch {
		case got == nil:
			t.Fatalf("%s: no telemetry", tt.name)
		case tt.want != nil && got != tt.want:
			t.Errorf("%s: not the started telemetry", tt.name)
		case tt.want == nil:
			if _, ok := got.MeterProvider().(noop.MeterProvider); !ok {
				t.Errorf("%s: meter provider %T, want a no-op", tt.name, got.MeterProvider())
			}
		}
		if warned := strings.Contains(logs.String(), "bogus"); warned != tt.warned {
			t.Errorf("%s: logged %q", tt.name, logs.String())
		}
	}
}

// TestMigrateFailsWithoutADatabase checks `araldo migrate` reports a
// database it cannot use, rather than succeeding as a background
// migration would.
func TestMigrateFailsWithoutADatabase(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, url string }{
		{"a malformed URL", "postgres://%zz"},
		// Nothing listens on port 1 of the loopback: refused at once.
		{"no server", "postgres://araldo@127.0.0.1:1/araldo?connect_timeout=5"},
	} {
		if v, err := Migrate(t.Context(), tt.url); err == nil {
			t.Errorf("%s: migrated to version %d", tt.name, v)
		}
	}
}
