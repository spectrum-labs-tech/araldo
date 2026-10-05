// SPDX-License-Identifier: AGPL-3.0-or-later

package authn

import (
	"strings"
	"testing"
)

func TestOperatorKeys(t *testing.T) {
	t.Parallel()
	k := NewOperatorKey()
	if !strings.HasPrefix(k, OperatorKeyPrefix) || !IsOperatorKey(k) {
		t.Fatalf("a new key %q is not one", k)
	}
	other := "0" // a wrong checksum character
	if strings.HasSuffix(k, "0") {
		other = "1"
	}
	flipped := k[:len(k)-1] + other
	for _, bad := range []string{"", OperatorKeyPrefix, flipped, NewUserToken(), NewAPIKey(true), k + "x", strings.Replace(k, "ald_op_", "ald_xx_", 1)} {
		if IsOperatorKey(bad) {
			t.Errorf("IsOperatorKey(%q) = true", bad)
		}
	}
}
