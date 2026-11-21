// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package cli

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestAdminApps drives `araldo admin apps` end to end, over the operator
// API (ADR 0035). It sets environment variables and stdin, which the
// commands read, so it cannot run in parallel.
func TestAdminApps(t *testing.T) {
	run := adminServer(t)
	name := "LinkedIn " + uuid.NewString()[:8]

	if _, errOut, code := run("", "admin", "apps", "add", "--provider", "linkedin", "--name", name); code == ExitOK {
		t.Fatalf("added without a client ID: %s", errOut)
	}
	if _, errOut, code := run("", "admin", "apps", "add", "--provider", "linkedin", "--name", name, "--client-id", "cid"); code == ExitOK {
		t.Fatalf("added without a secret on stdin: %s", errOut)
	}
	out, errOut := run.must(t, "the-secret\n", "admin", "apps", "add", "--provider", "linkedin", "--name", name, "--client-id", "cid-"+name)
	appID := strings.TrimSpace(out)
	if !strings.HasPrefix(appID, "app_") || !strings.Contains(errOut, "https://araldo.test/connect/linkedin/callback") || !strings.Contains(errOut, "connect LinkedIn") {
		t.Fatalf("add: %q / %q", out, errOut)
	}
	out, _ = run.must(t, "", "admin", "apps", "list")
	if !strings.Contains(out, appID) || !strings.Contains(out, "cid-"+name) || strings.Contains(out, "the-secret") {
		t.Fatalf("list after adding:\n%s", out)
	}
	if _, errOut = run.must(t, "", "admin", "apps", "rename", "--app", appID, "--name", name+" posting"); !strings.Contains(errOut, name+" posting") {
		t.Fatalf("rename: %q", errOut)
	}
	run.must(t, "", "admin", "apps", "remove", "--app", appID)
	if out, _ = run.must(t, "", "admin", "apps", "list"); strings.Contains(out, appID) {
		t.Fatalf("list after removing:\n%s", out)
	}
	if _, _, code := run("", "admin", "apps", "remove", "--app", appID); code != ExitError {
		t.Fatalf("removing it again: exit %d", code)
	}
	if _, _, code := run("", "admin", "apps", "remove", "--app", "not-an-id"); code != ExitUsage {
		t.Fatalf("removing an app by a malformed ID: exit %d", code)
	}
}
