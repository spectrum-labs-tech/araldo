// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestLayering checks ADR 0002's rule: the API, the dashboard and the CLI
// call core, never the store, not even for its types (core re-exports
// those it hands out).
func TestLayering(t *testing.T) {
	t.Parallel()
	const storePkg = "github.com/spectrum-labs-tech/araldo/internal/store"
	for _, dir := range []string{".", "../api", "../cli"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range parsed.Imports {
				if path, _ := strconv.Unquote(imp.Path.Value); path == storePkg {
					t.Errorf("%s imports the store; use core", f)
				}
			}
		}
	}
}
