// SPDX-License-Identifier: AGPL-3.0-or-later

package apperr

import (
	"errors"
	"fmt"
	"testing"
)

// errRefusedByTest is the error the check this test registers recognizes.
var errRefusedByTest = errors.New("apperr test: refused by the data")

// TestAs registers a check, so it cannot share its globals with another
// test that does; no other test in the package registers one.
func TestAs(t *testing.T) {
	RegisterInvalid(func(err error) bool { return errors.Is(err, errRefusedByTest) })
	tests := []struct {
		name string
		err  error
		want Kind
	}{
		{"an application error", NotFound("post"), KindNotFound},
		{"one wrapped", fmt.Errorf("x: %w", Conflict("c", "taken")), KindConflict},
		{"one the data refused", fmt.Errorf("insert: %w", errRefusedByTest), KindInvalid},
		{"anything else", errors.New("boom"), KindInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := As(tt.err); got.Kind != tt.want {
				t.Errorf("As(%v).Kind = %v, want %v", tt.err, got.Kind, tt.want)
			}
		})
	}
}
