// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

func TestInvitedIs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, invited, user string
		ok                  bool
	}{
		{"same", "ada@example.com", "ada@example.com", true},
		{"same, other case and spaces", "Ada@Example.com", " ada@example.com ", true},
		{"someone else", "ada@example.com", "bob@example.com", false},
		{"both invalid", "not an address", "also not one", false},
		{"invitation invalid", "", "ada@example.com", false},
		{"account invalid", "ada@example.com", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := invitedIs(&model.Invitation{Email: tt.invited}, &model.User{Email: tt.user})
			if (err == nil) != tt.ok {
				t.Fatalf("invitedIs(%q, %q) = %v, want ok %v", tt.invited, tt.user, err, tt.ok)
			}
		})
	}
}
