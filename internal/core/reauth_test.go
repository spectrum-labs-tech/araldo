// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"fmt"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
)

func TestNeedsReauth(t *testing.T) {
	t.Parallel()
	s := &Service{}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"no session", s.requireSudo(nil), true},
		{"wrapped", fmt.Errorf("saving: %w", s.requireSudo(nil)), true},
		{"refused for another reason", apperr.Forbidden("No."), false},
		{"unexpected", errors.New("boom"), false},
		{"none", nil, false},
		{"single sign-on session", errSSOSession, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NeedsReauth(tt.err); got != tt.want {
				t.Fatalf("NeedsReauth(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
