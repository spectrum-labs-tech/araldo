// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// TestMembersAndOrg drives the admin commands for accounts, members, org
// settings and API keys end to end, over the operator API (ADR 0035). It
// sets environment variables, which the commands read, so it cannot run in
// parallel.
func TestMembersAndOrg(t *testing.T) {
	suffix := uuid.NewString()[:8]
	owner := "owner-" + suffix + "@example.com"
	newcomer := "new-" + suffix + "@example.com"
	org := "Org " + suffix
	run := adminServer(t, "--email", owner, "--org", org, "--brand", "Brand "+suffix)

	out, errOut := run.must(t, "", "admin", "members", "add", "--org", org, "--email", newcomer, "--role", "editor")
	if strings.TrimSpace(out) == "" || !strings.Contains(errOut, "temporary password") {
		t.Fatalf("add printed %q / %q, want a temporary password", out, errOut)
	}
	run.must(t, "", "admin", "members", "role", "--org", org, "--email", newcomer, "--role", "admin")
	out, _ = run.must(t, "", "admin", "members", "list", "--org", org)
	if !hasRow(out, newcomer, "admin") || !hasRow(out, owner, "owner") {
		t.Fatalf("list after the role change:\n%s", out)
	}
	run.must(t, "", "admin", "members", "remove", "--org", org, "--email", newcomer)
	out, _ = run.must(t, "", "admin", "members", "list", "--org", org)
	if strings.Contains(out, newcomer) {
		t.Fatalf("list after removing:\n%s", out)
	}

	// Accounts: a password from stdin, then a generated one on reset.
	other := "made-" + suffix + "@example.com"
	if out, _ = run.must(t, "a long chosen password\n", "admin", "users", "create", "--email", other, "--password-stdin"); out != "Created "+other+".\n" {
		t.Fatalf("users create: %q", out)
	}
	if out, _ = run.must(t, "", "admin", "users", "reset-password", "--email", other); !strings.Contains(out, "Password reset for "+other) || !strings.Contains(out, "Password: ") {
		t.Fatalf("users reset-password: %q", out)
	}

	// An API key, alone on stdout.
	out, errOut = run.must(t, "", "admin", "apikeys", "create", "--org", org, "--brand", "brand-"+suffix, "--name", "ci", "--scopes", "posts:write", "--expires", "24h")
	if !strings.HasPrefix(out, "ald_test_") || strings.Count(out, "\n") != 1 || !strings.Contains(errOut, `Created API key "ci"`) {
		t.Fatalf("apikeys create: %q / %q", out, errOut)
	}

	_, errOut = run.must(t, "", "admin", "org", "update", "--org", org, "--name", "Renamed "+suffix, "--require-mfa", "false")
	if !strings.Contains(errOut, "Renamed "+suffix) || !strings.Contains(errOut, "two-factor required: false") {
		t.Errorf("org update: %q", errOut)
	}
	org = "Renamed " + suffix

	// The core's rules still apply: the last owner cannot be demoted.
	if _, errOut, code := run("", "admin", "members", "role", "--org", org, "--email", owner, "--role", "viewer"); code == ExitOK || !strings.Contains(errOut, "last_owner") {
		t.Fatalf("demoted the only owner: %s", errOut)
	}

	// The audit log names the operator's key, not a member.
	st, err := store.Open(t.Context(), os.Getenv("ARALDO_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var withUser, withKey int
	rows, err := st.Pool().Query(t.Context(), `SELECT a.actor_user IS NOT NULL, a.detail->>'operator_key' IS NOT NULL FROM audit_events a
		JOIN orgs o ON o.id = a.org_id WHERE o.name = $1 AND a.action LIKE 'member.%'`, org)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var hasUser, hasKey bool
		if err := rows.Scan(&hasUser, &hasKey); err != nil {
			t.Fatal(err)
		}
		if hasUser {
			withUser++
		}
		if hasKey {
			withKey++
		}
	}
	if withUser != 0 || withKey != 3 {
		t.Fatalf("member changes: %d audited with a member, %d with the operator key; want 0 and 3", withUser, withKey)
	}

	// The operator deletes the org, naming it twice.
	if _, _, code := run("", "admin", "org", "delete", "--org", org, "--confirm", "not it"); code == ExitOK {
		t.Fatal("deleted with the wrong name")
	}
	if _, errOut = run.must(t, "", "admin", "org", "delete", "--org", org, "--confirm", org); !strings.Contains(errOut, "Deleted "+org) {
		t.Fatalf("delete: %q", errOut)
	}
	if _, _, code := run("", "admin", "members", "list", "--org", org); code == ExitOK {
		t.Fatal("the deleted org is still there")
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
