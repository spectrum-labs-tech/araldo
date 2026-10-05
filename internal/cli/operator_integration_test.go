// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/authn"
)

// TestAdminOperator drives `araldo admin operator-keys` and an org's
// status and limits through `admin org update`. It sets environment
// variables, which the commands read, so it cannot run in parallel.
func TestAdminOperator(t *testing.T) {
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	t.Setenv("ARALDO_DATABASE_URL", dsn)
	t.Setenv("ARALDO_MASTER_KEYS", "test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("ARALDO_BASE_URL", "https://araldo.test")
	run := func(args ...string) (string, string, int) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := Run(t.Context(), args, &stdout, &stderr)
		return stdout.String(), stderr.String(), code
	}
	name := "billing " + uuid.NewString()[:8]
	out, errOut, code := run("admin", "operator-keys", "create", "--name", name)
	key := strings.TrimSpace(out)
	if code != ExitOK || !authn.IsOperatorKey(key) {
		t.Fatalf("create: exit %d, %q / %q", code, out, errOut)
	}
	keyID := errOut[strings.Index(errOut, "(opkey_")+1 : strings.Index(errOut, ")")]
	if out, _, _ = run("admin", "operator-keys", "list"); !strings.Contains(out, keyID) || !strings.Contains(out, name) || strings.Contains(out, key) {
		t.Fatalf("list:\n%s", out)
	}
	if _, errOut, code := run("admin", "operator-keys", "revoke", "--key", keyID); code != ExitOK {
		t.Fatalf("revoke: exit %d, %q", code, errOut)
	}
	if out, _, _ = run("admin", "operator-keys", "list"); strings.Contains(out, keyID) {
		t.Fatalf("list after revoking:\n%s", out)
	}

	suffix := uuid.NewString()[:8]
	org := "Org " + suffix
	if _, errOut, code := run("admin", "bootstrap", "--email", "op-"+suffix+"@example.com", "--org", org); code != ExitOK {
		t.Fatalf("bootstrap: %s", errOut)
	}
	_, errOut, code = run("admin", "org", "update", "--org", org, "--status", "read_only", "--status-note", "Past due",
		"--limits", "brands=2,posts_per_month=100", "--external-ref", "cus_"+suffix)
	if code != ExitOK || !strings.Contains(errOut, "status: read_only") {
		t.Fatalf("update: exit %d, %q", code, errOut)
	}
	if _, errOut, code := run("admin", "org", "update", "--org", org, "--status", "closed"); code == ExitOK {
		t.Fatalf("an unknown status: %q", errOut)
	}
	if _, errOut, code := run("admin", "org", "update", "--org", org, "--limits", "seats=3"); code == ExitOK {
		t.Fatalf("an unknown limit: %q", errOut)
	}
}
