// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/authn"
)

// TestAdminOperator drives `araldo admin operator-keys`, an org's status
// and limits through `admin org update`, and master keys, over the
// operator API (ADR 0035). It sets environment variables, which the
// commands read, so it cannot run in parallel.
func TestAdminOperator(t *testing.T) {
	suffix := uuid.NewString()[:8]
	org := "Org " + suffix
	run := adminServer(t, "--email", "op-"+suffix+"@example.com", "--org", org)

	name := "billing " + uuid.NewString()[:8]
	out, errOut := run.must(t, "", "admin", "operator-keys", "create", "--name", name)
	key := strings.TrimSpace(out)
	if !authn.IsOperatorKey(key) {
		t.Fatalf("create: %q / %q", out, errOut)
	}
	keyID := errOut[strings.Index(errOut, "(opkey_")+1 : strings.Index(errOut, ")")]
	if out, _ = run.must(t, "", "admin", "operator-keys", "list"); !strings.Contains(out, keyID) || !strings.Contains(out, name) || strings.Contains(out, key) {
		t.Fatalf("list:\n%s", out)
	}
	// The new key works, and cannot revoke itself.
	boot := os.Getenv("ARALDO_OPERATOR_KEY")
	t.Setenv("ARALDO_OPERATOR_KEY", key)
	if _, errOut, code := run("", "admin", "operator-keys", "revoke", "--key", keyID); code != ExitError || !strings.Contains(errOut, "operator_key_self") {
		t.Fatalf("a key revoking itself: exit %d, %q", code, errOut)
	}
	// Bootstrap's key revokes it.
	t.Setenv("ARALDO_OPERATOR_KEY", boot)
	run.must(t, "", "admin", "operator-keys", "revoke", "--key", keyID)
	if out, _ = run.must(t, "", "admin", "operator-keys", "list"); strings.Contains(out, keyID) {
		t.Fatalf("list after revoking:\n%s", out)
	}

	_, errOut = run.must(t, "", "admin", "org", "update", "--org", org, "--status", "read_only", "--status-note", "Past due",
		"--limits", "brands=2,posts_per_month=100", "--external-ref", "cus_"+suffix)
	if !strings.Contains(errOut, "status: read_only") {
		t.Fatalf("update: %q", errOut)
	}
	if _, errOut, code := run("", "admin", "org", "update", "--org", org, "--status", "closed"); code == ExitOK {
		t.Fatalf("an unknown status: %q", errOut)
	}
	if _, errOut, code := run("", "admin", "org", "update", "--org", org, "--limits", "seats=3"); code != ExitUsage {
		t.Fatalf("an unknown limit: %q", errOut)
	}

	// Master keys: the test server has one, so rotating finds nothing to
	// move. Keys other local runs left under master keys the server does
	// not have are reported, not moved.
	if out, _, _ := run("", "admin", "keys", "status"); !strings.Contains(out, "test") || !strings.Contains(out, "primary") {
		t.Fatalf("keys status:\n%s", out)
	}
	if out, errOut, _ := run("", "admin", "keys", "rotate"); !strings.HasPrefix(out, "Rewrapped 0 data keys") {
		t.Fatalf("keys rotate: %q / %q", out, errOut)
	}
}
