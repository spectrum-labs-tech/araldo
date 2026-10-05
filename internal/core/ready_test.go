// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "testing"

// TestSchemaReady checks that a pod serves on its own schema or a newer one,
// even after a newer migration failed, so a failed deploy never takes the
// running release out of service (ADR 0029).
func TestSchemaReady(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		v      uint
		dirty  bool
		latest uint
		ready  bool
	}{
		{"current", 19, false, 19, true},
		{"behind", 18, false, 19, false},
		{"its own migration failed", 19, true, 19, false},
		{"a newer release migrated", 20, false, 19, true},
		{"a newer release's migration failed", 20, true, 19, true},
		{"failed while catching up", 18, true, 19, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := schemaReady(tt.v, tt.dirty, tt.latest); (err == nil) != tt.ready {
				t.Errorf("schemaReady(%d, %t, %d) = %v, want ready %t", tt.v, tt.dirty, tt.latest, err, tt.ready)
			}
		})
	}
}
