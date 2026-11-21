// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration && shots

package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/platform/threads"
)

// TestScreenshots serves the dashboard over TestPagesAreAccessible's data
// and has scripts/shots.mjs photograph every page and run axe on it (task
// web:shots). It is for looking at the dashboard, not for CI, so it needs
// its own tag and a Chrome install.
func TestScreenshots(t *testing.T) {
	d := newDash(t, threads.New(http.DefaultClient))
	pages := seedPages(t, d)
	srv := httptest.NewServer(d.srv)
	defer srv.Close()

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := json.Marshal(map[string]any{"base": srv.URL, "token": d.login.Token, "pages": append(pages, "/login")})
	if err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(planFile, plan, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "node", "scripts/shots.mjs", planFile)
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("shots: %v", err)
	}
}
