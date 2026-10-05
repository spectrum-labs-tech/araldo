// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// TestMembersAndOrg drives the admin commands end to end. It sets
// environment variables, which the commands read, so it cannot run in
// parallel.
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
	org := "Org " + suffix
	run := func(args ...string) (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), args, &stdout, &stderr); code != ExitOK {
			t.Fatalf("araldo %s: exit %d\n%s", strings.Join(args, " "), code, stderr.String())
		}
		return stdout.String(), stderr.String()
	}
	run("admin", "bootstrap", "--email", owner, "--org", org, "--brand", "Brand "+suffix)

	out, errOut := run("admin", "members", "add", "--org", org, "--email", newcomer, "--role", "editor")
	if strings.TrimSpace(out) == "" || !strings.Contains(errOut, "temporary password") {
		t.Fatalf("add printed %q / %q, want a temporary password", out, errOut)
	}
	run("admin", "members", "role", "--org", org, "--email", newcomer, "--role", "admin")
	out, _ = run("admin", "members", "list", "--org", org)
	if !hasRow(out, newcomer, "admin") || !hasRow(out, owner, "owner") {
		t.Fatalf("list after the role change:\n%s", out)
	}
	run("admin", "members", "remove", "--org", org, "--email", newcomer)
	out, _ = run("admin", "members", "list", "--org", org)
	if strings.Contains(out, newcomer) {
		t.Fatalf("list after removing:\n%s", out)
	}

	// --as still works, and says it is no longer needed.
	_, errOut = run("admin", "org", "update", "--as", owner, "--name", "Renamed "+suffix, "--require-mfa", "false")
	for _, want := range []string{"--as is no longer needed", "Renamed " + suffix, "two-factor required: false"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("org update with --as: %q, want %q", errOut, want)
		}
	}
	org = "Renamed " + suffix

	// The core's rules still apply: the last owner cannot be demoted.
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"admin", "members", "role", "--org", org, "--email", owner, "--role", "viewer"}, &stdout, &stderr); code == ExitOK {
		t.Fatalf("demoted the only owner: %s", stderr.String())
	}

	// The audit log names the operator and the command, not a member.
	st, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var withUser int
	var commands []string
	rows, err := st.Pool().Query(t.Context(), `SELECT a.actor_user IS NOT NULL, a.detail->>'operator_command' FROM audit_events a
		JOIN orgs o ON o.id = a.org_id WHERE o.name = $1 AND a.action LIKE 'member.%'`, org)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var hasUser bool
		var cmd *string
		if err := rows.Scan(&hasUser, &cmd); err != nil {
			t.Fatal(err)
		}
		if hasUser {
			withUser++
		}
		if cmd != nil {
			commands = append(commands, *cmd)
		}
	}
	if withUser != 0 || !strings.Contains(strings.Join(commands, ";"), "araldo admin members add") {
		t.Fatalf("member changes audited with a member %d times; commands %v", withUser, commands)
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
