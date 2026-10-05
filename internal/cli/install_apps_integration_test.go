// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestAdminApps drives `araldo admin apps` end to end. It sets environment
// variables and stdin, which the commands read, so it cannot run in
// parallel.
func TestAdminApps(t *testing.T) {
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	t.Setenv("ARALDO_DATABASE_URL", dsn)
	t.Setenv("ARALDO_MASTER_KEYS", "test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("ARALDO_BASE_URL", "https://araldo.test")
	old := stdin
	t.Cleanup(func() { stdin = old })
	name := "LinkedIn " + uuid.NewString()[:8]
	run := func(input string, args ...string) (string, string, int) {
		t.Helper()
		stdin = strings.NewReader(input)
		var stdout, stderr bytes.Buffer
		code := Run(t.Context(), args, &stdout, &stderr)
		return stdout.String(), stderr.String(), code
	}

	if _, errOut, code := run("", "admin", "apps", "add", "--provider", "linkedin", "--name", name); code == ExitOK {
		t.Fatalf("added without a client ID: %s", errOut)
	}
	if _, errOut, code := run("", "admin", "apps", "add", "--provider", "linkedin", "--name", name, "--client-id", "cid"); code == ExitOK {
		t.Fatalf("added without a secret on stdin: %s", errOut)
	}
	out, errOut, code := run("the-secret\n", "admin", "apps", "add", "--provider", "linkedin", "--name", name, "--client-id", "cid-"+name)
	appID := strings.TrimSpace(out)
	if code != ExitOK || !strings.HasPrefix(appID, "app_") || !strings.Contains(errOut, "https://araldo.test/connect/linkedin/callback") {
		t.Fatalf("add: exit %d, %q / %q", code, out, errOut)
	}
	out, _, _ = run("", "admin", "apps", "list")
	if !strings.Contains(out, appID) || !strings.Contains(out, "cid-"+name) || strings.Contains(out, "the-secret") {
		t.Fatalf("list after adding:\n%s", out)
	}
	if _, errOut, code := run("", "admin", "apps", "rename", "--app", appID, "--name", name+" posting"); code != ExitOK || !strings.Contains(errOut, name+" posting") {
		t.Fatalf("rename: exit %d, %q", code, errOut)
	}
	if _, errOut, code := run("", "admin", "apps", "remove", "--app", appID); code != ExitOK {
		t.Fatalf("remove: exit %d, %q", code, errOut)
	}
	if out, _, _ = run("", "admin", "apps", "list"); strings.Contains(out, appID) {
		t.Fatalf("list after removing:\n%s", out)
	}
	if _, _, code := run("", "admin", "apps", "remove", "--app", "not-an-id"); code == ExitOK {
		t.Fatal("removed an app by a malformed ID")
	}
}
