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

// TestMembersAndOrg drives the commands end to end. It sets environment
// variables, which the commands read, so it cannot run in parallel.
func TestMembersAndOrg(t *testing.T) {
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	t.Setenv("ARALDO_DATABASE_URL", dsn)
	t.Setenv("ARALDO_MASTER_KEYS", "test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("ARALDO_BASE_URL", "https://araldo.test")
	suffix := uuid.NewString()[:8]
	owner := "owner-" + suffix + "@example.com"
	newcomer := "new-" + suffix + "@example.com"
	run := func(args ...string) (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), args, &stdout, &stderr); code != ExitOK {
			t.Fatalf("araldo %s: exit %d\n%s", strings.Join(args, " "), code, stderr.String())
		}
		return stdout.String(), stderr.String()
	}
	run("bootstrap", "--email", owner, "--org", "Org "+suffix, "--brand", "Brand "+suffix)

	out, errOut := run("members", "add", "--as", owner, "--email", newcomer, "--role", "editor")
	if strings.TrimSpace(out) == "" || !strings.Contains(errOut, "temporary password") {
		t.Fatalf("add printed %q / %q, want a temporary password", out, errOut)
	}
	run("members", "role", "--as", owner, "--email", newcomer, "--role", "admin")
	out, _ = run("members", "list", "--as", owner)
	if !hasRow(out, newcomer, "admin") || !hasRow(out, owner, "owner") {
		t.Fatalf("list after the role change:\n%s", out)
	}
	run("members", "remove", "--as", owner, "--email", newcomer)
	out, _ = run("members", "list", "--as", owner)
	if strings.Contains(out, newcomer) {
		t.Fatalf("list after removing:\n%s", out)
	}

	_, errOut = run("org", "update", "--as", owner, "--name", "Renamed "+suffix, "--require-mfa", "false")
	if !strings.Contains(errOut, "Renamed "+suffix) || !strings.Contains(errOut, "two-factor required: false") {
		t.Fatalf("org update: %q", errOut)
	}
	// The core's rules still apply: an owner without two-factor cannot
	// require it.
	var mfaOut, mfaErr bytes.Buffer
	if code := Run(t.Context(), []string{"org", "update", "--as", owner, "--require-mfa", "true"}, &mfaOut, &mfaErr); code == ExitOK ||
		!strings.Contains(mfaErr.String(), "two-factor authentication for yourself") {
		t.Fatalf("requiring MFA without it: exit %d, %q", code, mfaErr.String())
	}

	// The acting member's role still decides: an editor cannot add members.
	run("members", "add", "--as", owner, "--email", newcomer, "--role", "editor")
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"members", "add", "--as", newcomer, "--email", "x-" + suffix + "@example.com", "--role", "viewer"}, &stdout, &stderr); code == ExitOK {
		t.Fatalf("an editor added a member: %s", stderr.String())
	}
}

// hasRow reports whether a listing has a row for email with role.
func hasRow(out, email, role string) bool {
	for line := range strings.Lines(out) {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == email && f[1] == role {
			return true
		}
	}
	return false
}
