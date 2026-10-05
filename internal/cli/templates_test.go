// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTemplatesCommands checks araldo templates finds templates by key or
// ID and shows what a post needs to use one. It sets environment variables
// (cliEnv), so it cannot run in parallel.
func TestTemplatesCommands(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		queries = append(queries, r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Path {
		case "/v1/templates":
			if r.URL.Query().Get("key") == "nope" {
				_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
				return
			}
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"tmpl_1","key":"release","name":"Release","approval":"inherit","latest_version":3}]}`)
		case "/v1/templates/tmpl_1":
			_, _ = io.WriteString(w, `{"id":"tmpl_1","key":"release","name":"Release","approval":"inherit","latest_version":3,`+
				`"latest":{"version":3,"body":"Shipped {{.version}}","overrides":{"x":"v{{.version}} is out"},`+
				`"variables":{"type":"object","required":["version"]},"examples":[{"version":"1.0"}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"resource_missing","detail":"No such template.","status":404}`)
		}
	}))
	defer srv.Close()
	cliEnv(t, srv, true)

	code, out, errOut := runCLI(t, "", "templates", "list", "--brand", "araldo")
	if code != ExitOK || out != "release\tRelease\tv3\tinherit\ttmpl_1\n" {
		t.Fatalf("list: exit %d %q %q", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "", "templates", "get", "release", "--brand", "araldo")
	for _, want := range []string{"release  Release  v3", "Shipped {{.version}}", "for x:\nv{{.version}} is out", `"required"`, `example data: {"version":"1.0"}`} {
		if code != ExitOK || !strings.Contains(out, want) {
			t.Errorf("get lacks %q: exit %d\n%s%s", want, code, out, errOut)
		}
	}
	if code, out, _ = runCLI(t, "", "templates", "get", "tmpl_1", "--version", "2", "--json", "key"); code != ExitOK {
		t.Fatalf("get by ID: exit %d %q", code, out)
	}
	var picked []map[string]any
	if json.Unmarshal([]byte(out), &picked) != nil || len(picked) != 1 || picked[0]["key"] != "release" {
		t.Fatalf("get --json: %q", out)
	}
	if last := queries[len(queries)-1]; last != "/v1/templates/tmpl_1?version=2" {
		t.Fatalf("get by ID asked %q", last)
	}
	if code, _, errOut = runCLI(t, "", "templates", "get", "nope", "--brand", "araldo"); code == ExitOK || !strings.Contains(errOut, `no template "nope"`) {
		t.Fatalf("an unknown key: exit %d %q", code, errOut)
	}
	for _, bad := range [][]string{{"templates"}, {"templates", "get"}, {"templates", "get", "release"}, {"templates", "edit"}} {
		if code, _, errOut := runCLI(t, "", bad...); code != ExitUsage {
			t.Errorf("%v: exit %d %q", bad, code, errOut)
		}
	}
}
