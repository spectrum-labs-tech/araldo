// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

const testToken = "ald_live_cli-test"

// fakeAraldo answers the /v1 routes the client commands use, for the test
// token only, and remembers the last request body.
func fakeAraldo(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":"api_key_invalid","detail":"The key is malformed or unknown.","status":401}`)
			return
		}
		lastBody = nil
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&lastBody)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/me":
			_, _ = io.WriteString(w, `{"object":"me","livemode":true,"org":{"id":"org_1","name":"Spectrum Labs"},`+
				`"api_key":{"id":"key_1","object":"api_key","name":"cli","hint":"ald_live_…test","livemode":true,"scopes":[]}}`)
		case "GET /v1/channels":
			if r.URL.Query().Get("brand") == "empty" {
				_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
				return
			}
			if r.URL.Query().Get("brand") == "nope" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"code":"resource_missing","detail":"No such brand.","status":404}`)
				return
			}
			_, _ = io.WriteString(w, `{"object":"list","data":[`+
				`{"id":"chan_1","brand":"brand_1","provider":"bluesky","handle":"getotium.ai","status":"active","checked_at":"2026-10-04T19:40:23Z"},`+
				`{"id":"chan_2","brand":"brand_1","provider":"x","display_name":"Otium","status":"needs_reauth","status_note":"token revoked",`+
				`"checked_at":"2026-10-04T19:40:23Z","check_error":"401 from X"},`+
				`{"id":"chan_3","brand":"brand_9","provider":"sandbox","emulates":"mastodon","handle":"test","status":"active"}]}`)
		case "GET /v1/brands":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"brand_1","name":"Otium"}]}`)
		case "POST /v1/brands":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"brand_2","object":"brand","name":"New"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"resource_missing","detail":"No such route.","status":404}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &lastBody
}

// cliEnv points the client commands at srv with a throwaway config
// directory, so nothing reaches the real keychain. It sets environment
// variables, so the tests using it cannot run in parallel.
func cliEnv(t *testing.T, srv *httptest.Server, withToken bool) {
	t.Helper()
	t.Setenv("ARALDO_CONFIG_DIR", t.TempDir())
	t.Setenv("ARALDO_HOST", srv.URL)
	t.Setenv("ARALDO_URL", "")
	t.Setenv("ARALDO_API_KEY", "")
	t.Setenv("ARALDO_TOKEN", "")
	if withToken {
		t.Setenv("ARALDO_TOKEN", testToken)
	}
}

func runCLI(t *testing.T, input string, args ...string) (code int, out, errOut string) {
	t.Helper()
	old := stdin
	stdin = strings.NewReader(input)
	defer func() { stdin = old }()
	var stdout, stderr bytes.Buffer
	code = Run(t.Context(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestChannelsListThroughTheAPI(t *testing.T) {
	srv, _ := fakeAraldo(t)
	cliEnv(t, srv, true)

	// Piped, it is tab-separated rows without a header, as gh prints.
	code, out, errOut := runCLI(t, "", "channels", "list")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"Otium\tbluesky\tgetotium.ai\tactive\t2026-10-04 19:40:23 (ok)\tchan_1",
		"Otium\tx\tOtium\tneeds_reauth: token revoked\t2026-10-04 19:40:23 (401 from X)\tchan_2",
		"brand_9\tsandbox (mastodon)\ttest\tactive\tnever\tchan_3",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "BRAND") {
		t.Error("piped output has a header")
	}

	code, out, _ = runCLI(t, "", "channels", "list", "--json", "handle,status")
	var picked []map[string]any
	if code != ExitOK || json.Unmarshal([]byte(out), &picked) != nil || len(picked) != 3 || len(picked[0]) != 2 || picked[0]["handle"] != "getotium.ai" {
		t.Fatalf("--json: exit %d\n%s", code, out)
	}
	if code, out, _ = runCLI(t, "", "channels", "list", "--json", "handle", "--jq", ".[0].handle"); code != ExitOK || out != "getotium.ai\n" {
		t.Fatalf("--jq: exit %d %q", code, out)
	}
	if code, _, errOut = runCLI(t, "", "channels", "list", "--json", "secret"); code != ExitUsage || !strings.Contains(errOut, "status_note") {
		t.Fatalf("an unknown field: exit %d %q", code, errOut)
	}
	if code, _, errOut = runCLI(t, "", "channels", "list", "--brand", "nope"); code == ExitOK || !strings.Contains(errOut, "No such brand.") {
		t.Fatalf("an unknown brand: exit %d %q", code, errOut)
	}
	// As gh: an empty list piped prints nothing, and a bare --json lists the fields.
	if code, out, errOut = runCLI(t, "", "channels", "list", "--brand", "empty"); code != ExitOK || out != "" || errOut != "" {
		t.Fatalf("an empty list: exit %d %q %q", code, out, errOut)
	}
	for _, args := range [][]string{{"channels", "list", "--json"}, {"channels", "list", "--json", "--brand", "x"}} {
		if code, _, errOut = runCLI(t, "", args...); code != ExitUsage || !strings.Contains(errOut, "fields for --json") || !strings.Contains(errOut, "status_note") {
			t.Fatalf("%v: exit %d %q", args, code, errOut)
		}
	}
}

// auth login checks the token with the server and keeps it; status shows
// who it is; token prints it; logout forgets it.
func TestAuthLoginStatusLogout(t *testing.T) {
	srv, _ := fakeAraldo(t)
	cliEnv(t, srv, false)

	if code, _, errOut := runCLI(t, "ald_live_wrong\n", "auth", "login", "--with-token", "--insecure-storage"); code == ExitOK ||
		!strings.Contains(errOut, "malformed or unknown") {
		t.Fatalf("a bad token: exit %d %q", code, errOut)
	}
	code, out, errOut := runCLI(t, testToken+"\n", "auth", "login", "--with-token", "--insecure-storage")
	if code != ExitOK || !strings.Contains(out, `Logged in to`) || !strings.Contains(out, `Spectrum Labs, live API key "cli" (ald_live_…test)`) {
		t.Fatalf("login: exit %d %q %q", code, out, errOut)
	}
	if code, out, _ = runCLI(t, "", "auth", "status"); code != ExitOK || !strings.Contains(out, "✓ Logged in as Spectrum Labs") ||
		!strings.Contains(out, "(file)") {
		t.Fatalf("status: exit %d %q", code, out)
	}
	if code, out, _ = runCLI(t, "", "auth", "token"); code != ExitOK || out != testToken+"\n" {
		t.Fatalf("token: exit %d %q", code, out)
	}
	if code, _, errOut = runCLI(t, "", "channels", "list"); code != ExitOK {
		t.Fatalf("channels with the stored token: exit %d %q", code, errOut)
	}
	if code, _, errOut = runCLI(t, "", "auth", "logout"); code != ExitOK {
		t.Fatalf("logout: exit %d %q", code, errOut)
	}
	if code, _, _ = runCLI(t, "", "channels", "list"); code == ExitOK {
		t.Fatal("channels list worked after logging out")
	}
}

// Without --with-token, login opens the dashboard's key page, naming this
// computer, and reads the key pasted back.
func TestAuthLoginThroughTheBrowser(t *testing.T) {
	srv, _ := fakeAraldo(t)
	cliEnv(t, srv, false)
	var opened string
	old := openBrowser
	openBrowser = func(u string) error { opened = u; return nil }
	defer func() { openBrowser = old }()

	code, out, errOut := runCLI(t, testToken+"\n", "auth", "login", "--insecure-storage")
	if code != ExitOK || !strings.Contains(out, "Logged in to") {
		t.Fatalf("login: exit %d %q %q", code, out, errOut)
	}
	host, _ := os.Hostname()
	if want := srv.URL + "/cli?device=" + url.QueryEscape(host); opened != want {
		t.Fatalf("opened %q, want %q", opened, want)
	}
	if !strings.Contains(errOut, "Paste your API key") || strings.Contains(errOut, testToken) {
		t.Fatalf("prompt: %q", errOut)
	}
	// Without a browser, it prints the page to open instead.
	openBrowser = func(string) error { return errors.New("no browser") }
	if _, _, errOut = runCLI(t, testToken+"\n", "auth", "login", "--insecure-storage"); !strings.Contains(errOut, "Open this page in a browser: "+srv.URL+"/cli?device=") {
		t.Fatalf("without a browser: %q", errOut)
	}
	if code, _, errOut = runCLI(t, "\n", "auth", "login", "--insecure-storage"); code != ExitUsage {
		t.Fatalf("nothing pasted: exit %d %q", code, errOut)
	}
}

// araldo api: GET by default with fields as the query, POST with fields as
// the body, paths with or without /v1, and --jq.
func TestAPICommand(t *testing.T) {
	srv, lastBody := fakeAraldo(t)
	cliEnv(t, srv, true)

	if code, out, errOut := runCLI(t, "", "api", "channels", "--jq", ".data[0].handle"); code != ExitOK || out != "getotium.ai\n" {
		t.Fatalf("api channels: exit %d %q %q", code, out, errOut)
	}
	if code, out, _ := runCLI(t, "", "api", "-f", "name=New", "/v1/brands"); code != ExitOK || !strings.Contains(out, `"brand_2"`) ||
		(*lastBody)["name"] != "New" {
		t.Fatalf("api POST: exit %d %q body %v", code, out, *lastBody)
	}
	if code, out, _ := runCLI(t, "", "api", "nowhere"); code == ExitOK || !strings.Contains(out, "resource_missing") {
		t.Fatalf("api on a missing route: exit %d %q", code, out)
	}
}
