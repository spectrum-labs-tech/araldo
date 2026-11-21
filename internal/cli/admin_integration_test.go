// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package cli

import (
	"bytes"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/app"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/config"
)

// adminRun runs araldo commands, with input on stdin, and returns what
// they printed and their exit code.
type adminRun func(input string, args ...string) (stdout, stderr string, code int)

// adminServer runs an Araldo on the test database and points the admin
// commands at it, with an operator key from `araldo admin bootstrap
// --operator-key`, as an operator would; bootstrap is also given
// bootstrapArgs (an owner and org to create). It sets environment
// variables and stdin, so tests that use it cannot run in parallel.
func adminServer(t *testing.T, bootstrapArgs ...string) adminRun {
	t.Helper()
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	t.Setenv("ARALDO_DATABASE_URL", dsn)
	t.Setenv("ARALDO_MASTER_KEYS", "test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("ARALDO_BASE_URL", "https://araldo.test")
	t.Setenv("ARALDO_OPERATOR_KEY", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(t.Context(), cfg, app.Logger("error"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	h, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	old := stdin
	t.Cleanup(func() { stdin = old })
	run := func(input string, args ...string) (string, string, int) {
		t.Helper()
		stdin = strings.NewReader(input)
		var stdout, stderr bytes.Buffer
		code := Run(t.Context(), args, &stdout, &stderr)
		return stdout.String(), stderr.String(), code
	}
	out, errOut, code := run("", append([]string{"admin", "bootstrap", "--operator-key"}, bootstrapArgs...)...)
	key := strings.TrimSpace(out)
	if code != ExitOK || !authn.IsOperatorKey(key) || !strings.Contains(errOut, "Created operator key") {
		t.Fatalf("bootstrap --operator-key: exit %d, %q / %q", code, out, errOut)
	}
	t.Setenv("ARALDO_OPERATOR_HOST", srv.URL)
	t.Setenv("ARALDO_OPERATOR_KEY", key)
	// Nothing below may open the database: point it nowhere.
	t.Setenv("ARALDO_DATABASE_URL", "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	return run
}

// must runs args and fails the test unless they succeed.
func (run adminRun) must(t *testing.T, input string, args ...string) (string, string) {
	t.Helper()
	out, errOut, code := run(input, args...)
	if code != ExitOK {
		t.Fatalf("araldo %s: exit %d\n%s", strings.Join(args, " "), code, errOut)
	}
	return out, errOut
}
