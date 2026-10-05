// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// firstCheckedMigration is the first migration written under ADR 0029;
// earlier ones shipped and are never edited.
const firstCheckedMigration = 15

var (
	createTableRE = regexp.MustCompile(`(?i)\bCREATE TABLE (?:IF NOT EXISTS )?(\w+)`)
	createIndexRE = regexp.MustCompile(`(?i)\bCREATE (?:UNIQUE )?INDEX (CONCURRENTLY )?(?:IF NOT EXISTS )?\w+ ON (\w+)`)
	addCheckRE    = regexp.MustCompile(`(?i)\bADD (?:CONSTRAINT \w+ )?(?:CHECK|FOREIGN KEY)\b`)
	addUniqueRE   = regexp.MustCompile(`(?i)\bADD (?:CONSTRAINT \w+ )?(?:UNIQUE|PRIMARY KEY)\b`)
	contractRE    = regexp.MustCompile(`(?i)\b(DROP COLUMN|DROP TABLE|RENAME|SET NOT NULL|TYPE \w+)\b`)
)

// statements splits a migration into statements, each with the comments
// before it, dropping blank ones.
func statements(sql string) []string {
	var out []string
	for st := range strings.SplitSeq(sql, ";") {
		if code(st) != "" {
			out = append(out, st)
		}
	}
	return out
}

// code is a statement without its comment lines.
func code(st string) string {
	var lines []string
	for l := range strings.SplitSeq(st, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "--") {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, " ")
}

// checkMigration returns how an up migration breaks ADR 0029's rules.
func checkMigration(sql string) []string {
	var problems []string
	sts := statements(sql)
	if len(sts) == 0 {
		return nil
	}
	created := map[string]bool{}
	for _, st := range sts {
		if m := createTableRE.FindStringSubmatch(code(st)); m != nil {
			created[strings.ToLower(m[1])] = true
		}
	}
	concurrent := false
	for _, st := range sts {
		if m := createIndexRE.FindStringSubmatch(code(st)); m != nil && !created[strings.ToLower(m[2])] {
			if m[1] == "" {
				problems = append(problems, "an index on existing table "+m[2]+" is built without CONCURRENTLY")
			}
			concurrent = true
		}
	}
	if concurrent {
		if len(sts) > 1 {
			problems = append(problems, "CREATE INDEX CONCURRENTLY must be the only statement in its migration")
		}
		return problems
	}
	if !strings.HasPrefix(strings.ToUpper(code(sts[0])), "SET LOCAL LOCK_TIMEOUT") {
		problems = append(problems, "it does not start with SET LOCAL lock_timeout")
	}
	notValid, validates := false, false
	for _, st := range sts {
		c := code(st)
		upper := strings.ToUpper(c)
		if !strings.Contains(upper, "CREATE TABLE") {
			if addCheckRE.MatchString(c) {
				if !strings.Contains(upper, "NOT VALID") {
					problems = append(problems, fmt.Sprintf("%q adds a constraint without NOT VALID", c))
				}
				notValid = true
			}
			if addUniqueRE.MatchString(c) && !strings.Contains(upper, "USING INDEX") {
				problems = append(problems, fmt.Sprintf("%q builds a unique index under a lock: create it CONCURRENTLY first", c))
			}
		}
		if strings.Contains(upper, "VALIDATE CONSTRAINT") {
			validates = true
		}
		if m := contractRE.FindString(c); m != "" && !strings.Contains(st, "-- contract:") {
			problems = append(problems, fmt.Sprintf("%q contracts the schema (%s) without a -- contract: comment", c, m))
		}
	}
	if notValid && validates {
		problems = append(problems, "it validates a constraint in the migration that adds it: validate in the next one")
	}
	return problems
}

func TestMigrationsAreBackwardCompatible(t *testing.T) {
	t.Parallel()
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		var v uint
		if _, err := fmt.Sscanf(e.Name(), "%d_", &v); err != nil || v < firstCheckedMigration || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		b, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range checkMigration(string(b)) {
			t.Errorf("%s: %s (ADR 0029)", e.Name(), p)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no migrations checked")
	}
}

// TestCheckMigration checks the checker on migrations that break each rule.
func TestCheckMigration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		sql  string
		want string // a problem containing this; empty for none
	}{
		{"a new table", "SET LOCAL lock_timeout = '5s';\nCREATE TABLE t (id uuid, FOREIGN KEY (id) REFERENCES u (id));\nCREATE INDEX t_idx ON t (id);", ""},
		{"no lock_timeout", "ALTER TABLE t ADD COLUMN c int;", "SET LOCAL lock_timeout"},
		{"a constraint scanned under a lock", "SET LOCAL lock_timeout = '5s';\nALTER TABLE t ADD CONSTRAINT c CHECK (x > 0);", "without NOT VALID"},
		{"added NOT VALID", "SET LOCAL lock_timeout = '5s';\nALTER TABLE t ADD CONSTRAINT c FOREIGN KEY (a) REFERENCES u (a) NOT VALID;", ""},
		{"validated in the same migration", "SET LOCAL lock_timeout = '5s';\nALTER TABLE t ADD CONSTRAINT c CHECK (x > 0) NOT VALID;\nALTER TABLE t VALIDATE CONSTRAINT c;", "validate in the next one"},
		{"validated on its own", "SET LOCAL lock_timeout = '5s';\nALTER TABLE t VALIDATE CONSTRAINT c;", ""},
		{"an index under a lock", "SET LOCAL lock_timeout = '5s';\nCREATE INDEX i ON t (a);", "without CONCURRENTLY"},
		{"a concurrent index", "-- why\nCREATE UNIQUE INDEX CONCURRENTLY i ON t (a, b);", ""},
		{"a concurrent index with company", "SET LOCAL lock_timeout = '5s';\nCREATE INDEX CONCURRENTLY i ON t (a);", "only statement"},
		{"a unique constraint under a lock", "SET LOCAL lock_timeout = '5s';\nALTER TABLE t ADD CONSTRAINT k UNIQUE (a, b);", "CONCURRENTLY first"},
		{"a column dropped", "SET LOCAL lock_timeout = '5s';\nALTER TABLE t DROP COLUMN c;", "contract"},
		{"a column dropped, as a contract", "SET LOCAL lock_timeout = '5s';\n-- contract: v0.9.0 stopped reading c.\nALTER TABLE t DROP COLUMN c;", ""},
		{"made NOT NULL", "SET LOCAL lock_timeout = '5s';\nALTER TABLE t ALTER COLUMN c SET NOT NULL;", "contract"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := strings.Join(checkMigration(tt.sql), "; ")
			if tt.want == "" && got != "" || tt.want != "" && !strings.Contains(got, tt.want) {
				t.Errorf("problems %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFailedMigrationRecovery(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		v    uint
		want int
	}{{1, -1}, {2, 1}, {19, 18}} {
		if got := previousVersion(tt.v); got != tt.want {
			t.Errorf("previousVersion(%d) = %d, want %d", tt.v, got, tt.want)
		}
	}
	sql, err := migrationSQL(15)
	if err != nil {
		t.Fatal(err)
	}
	if m := concurrentIndexRE.FindStringSubmatch(sql); m == nil || m[1] != "channels_mode_key" {
		t.Errorf("the index migration 15 builds: %v", m)
	}
	if sql, err = migrationSQL(18); err != nil || concurrentIndexRE.MatchString(sql) {
		t.Errorf("migration 18 builds no index concurrently: %v", err)
	}
	if _, err := migrationSQL(9999); err == nil {
		t.Error("migrationSQL found a migration that does not exist")
	}
}
