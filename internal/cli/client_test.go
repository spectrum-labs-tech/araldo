// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// testToken and liveToken are the only keys fakeAraldo knows, one per mode;
// userToken is a person's token from its device sign-in, and multiToken one
// of a person in two orgs.
const (
	testToken  = "ald_test_cli-test"
	liveToken  = "ald_live_cli-test"
	userToken  = "ald_user_cli-test"
	multiToken = "ald_user_cli-multi"
)

// fakeAraldo answers the /v1 routes the client commands use, for its
// credentials only, and remembers the last request body. Its device
// sign-in is approved on the second poll.
func fakeAraldo(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	var lastBody map[string]any
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/auth/device":
			_ = json.NewDecoder(r.Body).Decode(&lastBody)
			_, _ = io.WriteString(w, `{"device_code":"dc","user_code":"BDFG-HJKL","verification_uri":"`+"http://"+r.Host+`/device",`+
				`"verification_uri_complete":"`+"http://"+r.Host+`/device?code=BDFG-HJKL","expires_in":900,"interval":1}`)
			return
		case "POST /v1/auth/device/token":
			if polls++; polls == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"code":"authorization_pending","detail":"Waiting.","status":400}`)
				return
			}
			_, _ = io.WriteString(w, `{"object":"user_token","access_token":"`+userToken+`","token_type":"bearer"}`)
			return
		}
		if auth != testToken && auth != liveToken && auth != userToken && auth != multiToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":"api_key_invalid","detail":"The key is malformed or unknown.","status":401}`)
			return
		}
		if r.Method != http.MethodGet { // a read keeps the last write's body to look at
			lastBody = nil
			if r.Body != nil {
				_ = json.NewDecoder(r.Body).Decode(&lastBody)
			}
		}
		person := auth == userToken || auth == multiToken
		if auth == multiToken && r.Header.Get("Araldo-Org") == "" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"code":"org_required","detail":"You belong to 2 orgs: name one in the Araldo-Org header (Spectrum Labs, Other).","status":422}`)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/me":
			if person {
				_, _ = io.WriteString(w, `{"object":"me","livemode":false,"org":{"id":"org_1","name":"Spectrum Labs"},"role":"owner",`+
					`"user":{"id":"user_1","email":"you@example.com"},"user_token":{"id":"utok_1","name":"laptop","hint":"ald_user_…test"}}`)
				return
			}
			live := auth == liveToken
			_, _ = fmt.Fprintf(w, `{"object":"me","livemode":%t,"org":{"id":"org_1","name":"Spectrum Labs"},`+
				`"api_key":{"id":"key_1","object":"api_key","name":"cli","hint":%q,"livemode":%t,"scopes":[]}}`, live, auth[:9]+"…test", live)
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
				`{"id":"chan_1","brand":"brand_1","provider":"bluesky","handle":"araldo.dev","status":"active","checked_at":"2026-10-04T19:40:23Z"},`+
				`{"id":"chan_2","brand":"brand_1","provider":"x","display_name":"Araldo","status":"needs_reauth","status_note":"token revoked",`+
				`"checked_at":"2026-10-04T19:40:23Z","check_error":"401 from X"},`+
				`{"id":"chan_3","brand":"brand_9","provider":"sandbox","emulates":"mastodon","handle":"test","status":"active"}]}`)
		case "GET /v1/posts":
			// Five posts, two to a page, after starting_after.
			start := 0
			if after := r.URL.Query().Get("starting_after"); after != "" {
				_, _ = fmt.Sscanf(after, "post_%d", &start)
			}
			var items []string
			for i := start + 1; i <= start+2 && i <= 5; i++ {
				items = append(items, fmt.Sprintf(`{"id":"post_%d"}`, i))
			}
			_, _ = fmt.Fprintf(w, `{"object":"list","data":[%s],"has_more":%t}`, strings.Join(items, ","), start+2 < 5)
		case "DELETE /v1/auth/token":
			lastBody = map[string]any{"revoked": auth}
			_, _ = io.WriteString(w, `{"id":"utok_1","object":"user_token","deleted":true}`)
		case "GET /v1/members":
			if !person {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"code":"forbidden","detail":"API keys cannot manage the org or its members.","status":403}`)
				return
			}
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"user_1","email":"you@example.com","role":"owner","mfa":true},`+
				`{"id":"user_2","email":"them@example.com","name":"Them","role":"editor","mfa":false}]}`)
		case "POST /v1/invitations":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"inv_1","object":"invitation","email":"new@example.com","role":"viewer","url":"https://araldo.example/invite/tok"}`)
		case "POST /v1/members/user_2":
			_, _ = io.WriteString(w, `{"id":"user_2","object":"member","role":"admin"}`)
		case "DELETE /v1/members/user_2":
			_, _ = io.WriteString(w, `{"id":"user_2","object":"member","deleted":true}`)
		case "GET /v1/org", "POST /v1/org":
			name := "Spectrum Labs"
			if n, _ := lastBody["name"].(string); n != "" {
				name = n
			}
			_, _ = fmt.Fprintf(w, `{"id":"org_1","object":"org","name":%q,"require_mfa":true}`, name)
		case "GET /v1/brands":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"brand_1","name":"Araldo"}]}`)
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
		"Araldo\tbluesky\taraldo.dev\tactive\t2026-10-04 19:40:23 (ok)\tchan_1",
		"Araldo\tx\tAraldo\tneeds_reauth: token revoked\t2026-10-04 19:40:23 (401 from X)\tchan_2",
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
	if code != ExitOK || json.Unmarshal([]byte(out), &picked) != nil || len(picked) != 3 || len(picked[0]) != 2 || picked[0]["handle"] != "araldo.dev" {
		t.Fatalf("--json: exit %d\n%s", code, out)
	}
	if code, out, _ = runCLI(t, "", "channels", "list", "--json", "handle", "--jq", ".[0].handle"); code != ExitOK || out != "araldo.dev\n" {
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
	if code != ExitOK || !strings.Contains(out, `Logged in to`) || !strings.Contains(out, `Spectrum Labs, test API key "cli" (ald_test_…test)`) {
		t.Fatalf("login: exit %d %q %q", code, out, errOut)
	}
	if code, out, _ = runCLI(t, "", "auth", "status"); code != ExitOK || !strings.Contains(out, "✓ test: Spectrum Labs") ||
		!strings.Contains(out, "(file)") || !strings.Contains(out, "- live: not signed in (araldo auth login --hostname 127.0.0.1") {
		t.Fatalf("status with a test key: exit %d %q", code, out)
	}
	if code, _, errOut = runCLI(t, "", "channels", "list", "--live"); code == ExitOK || !strings.Contains(errOut, "--live") {
		t.Fatalf("--live without a live key: exit %d %q", code, errOut)
	}
	// A live key read from stdin goes in the live slot, beside the test key.
	if code, _, errOut = runCLI(t, liveToken+"\n", "auth", "login", "--with-token", "--insecure-storage"); code != ExitOK ||
		!strings.Contains(errOut, "unless given --live") {
		t.Fatalf("live login: exit %d %q", code, errOut)
	}
	// --live insists on a live key.
	if code, _, errOut = runCLI(t, testToken+"\n", "auth", "login", "--live", "--with-token", "--insecure-storage"); code != ExitUsage ||
		!strings.Contains(errOut, "that is a test credential") {
		t.Fatalf("a test key for --live: exit %d %q", code, errOut)
	}
	if code, out, _ = runCLI(t, "", "auth", "status"); code != ExitOK || !strings.Contains(out, "✓ test: ") || !strings.Contains(out, "✓ live: ") {
		t.Fatalf("status with both keys: exit %d %q", code, out)
	}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"auth", "token"}, testToken},
		{[]string{"auth", "token", "--live"}, liveToken},
	} {
		if code, out, _ = runCLI(t, "", tt.args...); code != ExitOK || out != tt.want+"\n" {
			t.Fatalf("%v: exit %d %q", tt.args, code, out)
		}
	}
	if code, _, errOut = runCLI(t, "", "channels", "list", "--live"); code != ExitOK {
		t.Fatalf("channels --live: exit %d %q", code, errOut)
	}
	if code, _, errOut = runCLI(t, "", "channels", "list"); code != ExitOK {
		t.Fatalf("channels with the stored token: exit %d %q", code, errOut)
	}
	if code, _, errOut = runCLI(t, "", "auth", "logout"); code != ExitOK {
		t.Fatalf("logout: exit %d %q", code, errOut)
	}
	for _, args := range [][]string{{"channels", "list"}, {"channels", "list", "--live"}} {
		if code, _, _ = runCLI(t, "", args...); code == ExitOK {
			t.Fatalf("%v worked after logging out", args)
		}
	}
}

// Without --with-token, login signs in with a device code, as gh does: it
// shows the code, opens the page to approve it, and polls until it is.
func TestAuthLoginWithADeviceCode(t *testing.T) {
	srv, lastBody := fakeAraldo(t)
	cliEnv(t, srv, false)
	var opened string
	old := openBrowser
	openBrowser = func(u string) error { opened = u; return nil }
	defer func() { openBrowser = old }()

	code, out, errOut := runCLI(t, "", "auth", "login", "--insecure-storage")
	if code != ExitOK || !strings.Contains(out, "Logged in to") || !strings.Contains(out, "you@example.com (owner of Spectrum Labs), test token ald_user_…test") {
		t.Fatalf("login: exit %d %q %q", code, out, errOut)
	}
	if !strings.Contains(errOut, "First copy your one-time code: BDFG-HJKL") || opened != srv.URL+"/device?code=BDFG-HJKL" {
		t.Fatalf("the code and page: opened %q, %q", opened, errOut)
	}
	host, _ := os.Hostname()
	if (*lastBody)["device_name"] != "araldo CLI on "+host || (*lastBody)["livemode"] != false {
		t.Fatalf("the sign-in asked for %v", *lastBody)
	}
	if code, out, _ = runCLI(t, "", "auth", "status"); code != ExitOK || !strings.Contains(out, "✓ test: you@example.com (owner of Spectrum Labs)") {
		t.Fatalf("status: exit %d %q", code, out)
	}
	// Signing out revokes the token on the server first.
	if code, _, errOut = runCLI(t, "", "auth", "logout"); code != ExitOK || (*lastBody)["revoked"] != userToken {
		t.Fatalf("logout: exit %d %q, revoked %v", code, errOut, *lastBody)
	}

	// Without a browser, it prints the page to open instead.
	openBrowser = func(string) error { return errors.New("no browser") }
	if _, _, errOut = runCLI(t, "", "auth", "login", "--insecure-storage"); !strings.Contains(errOut, "Open this page in a browser and enter it: "+srv.URL+"/device") {
		t.Fatalf("without a browser: %q", errOut)
	}
}

// A person in several orgs names one: --org, or a default set at sign-in.
func TestOrgForAPersonInSeveral(t *testing.T) {
	srv, _ := fakeAraldo(t)
	cliEnv(t, srv, false)
	code, _, errOut := runCLI(t, multiToken+"\n", "auth", "login", "--with-token", "--insecure-storage")
	if code != ExitOK || !strings.Contains(errOut, "belong to 2 orgs") || !strings.Contains(errOut, "--org") {
		t.Fatalf("login without an org: exit %d %q", code, errOut)
	}
	if code, _, errOut = runCLI(t, "", "members", "list"); code == ExitOK || !strings.Contains(errOut, "org_required") {
		t.Fatalf("a command without an org: exit %d %q", code, errOut)
	}
	if code, out, errOut := runCLI(t, "", "members", "list", "--org", "Spectrum Labs"); code != ExitOK || !strings.Contains(out, "them@example.com") {
		t.Fatalf("--org: exit %d %q %q", code, out, errOut)
	}
	if code, _, errOut = runCLI(t, multiToken+"\n", "auth", "login", "--with-token", "--insecure-storage", "--org", "Spectrum Labs"); code != ExitOK {
		t.Fatalf("login with a default org: exit %d %q", code, errOut)
	}
	if code, out, errOut := runCLI(t, "", "members", "list"); code != ExitOK || !strings.Contains(out, "them@example.com") {
		t.Fatalf("the default org: exit %d %q %q", code, out, errOut)
	}
	if code, out, _ := runCLI(t, "", "auth", "status"); code != ExitOK || !strings.Contains(out, "Default org: Spectrum Labs") {
		t.Fatalf("status shows the default org: %q", out)
	}
}

// members and org act as the person signed in, through the API.
func TestMembersAndOrgCommands(t *testing.T) {
	srv, lastBody := fakeAraldo(t)
	cliEnv(t, srv, false)
	t.Setenv("ARALDO_TOKEN", userToken)

	code, out, errOut := runCLI(t, "", "members", "list")
	if code != ExitOK || !strings.Contains(out, "them@example.com\tThem\teditor\toff") {
		t.Fatalf("members list: exit %d %q %q", code, out, errOut)
	}
	if code, out, errOut = runCLI(t, "", "members", "invite", "--email", "new@example.com", "--role", "viewer"); code != ExitOK ||
		out != "https://araldo.example/invite/tok\n" || (*lastBody)["email"] != "new@example.com" || !strings.Contains(errOut, "works once") {
		t.Fatalf("invite: exit %d %q %q", code, out, errOut)
	}
	if code, _, errOut = runCLI(t, "", "members", "role", "--email", "THEM@example.com", "--role", "admin"); code != ExitOK || (*lastBody)["role"] != "admin" {
		t.Fatalf("role: exit %d %q", code, errOut)
	}
	if code, _, errOut = runCLI(t, "", "members", "remove", "--email", "them@example.com"); code != ExitOK || !strings.Contains(errOut, "Removed") {
		t.Fatalf("remove: exit %d %q", code, errOut)
	}
	if code, _, errOut = runCLI(t, "", "members", "remove", "--email", "stranger@example.com"); code == ExitOK || !strings.Contains(errOut, "not a member") {
		t.Fatalf("removing a stranger: exit %d %q", code, errOut)
	}
	if code, _, errOut = runCLI(t, "", "members", "list", "--as", "you@example.com"); code != ExitUsage || !strings.Contains(errOut, "araldo admin members") {
		t.Fatalf("--as, from before: exit %d %q", code, errOut)
	}
	if code, out, _ = runCLI(t, "", "org", "view"); code != ExitOK || !strings.Contains(out, "Spectrum Labs") || !strings.Contains(out, "Two-factor authentication: required") {
		t.Fatalf("org view: exit %d %q", code, out)
	}
	if code, out, _ = runCLI(t, "", "org", "update", "--name", "Renamed"); code != ExitOK || !strings.HasPrefix(out, "Renamed\n") {
		t.Fatalf("org update: exit %d %q", code, out)
	}
	// An API key is refused, by the server.
	t.Setenv("ARALDO_TOKEN", testToken)
	if code, _, errOut = runCLI(t, "", "members", "list"); code == ExitOK || !strings.Contains(errOut, "API keys cannot manage") {
		t.Fatalf("members with an API key: exit %d %q", code, errOut)
	}
}

// araldo api: GET by default with fields as the query, POST with fields as
// the body, paths with or without /v1, and --jq.
func TestAPICommand(t *testing.T) {
	srv, lastBody := fakeAraldo(t)
	cliEnv(t, srv, true)

	if code, out, errOut := runCLI(t, "", "api", "channels", "--jq", ".data[0].handle"); code != ExitOK || out != "araldo.dev\n" {
		t.Fatalf("api channels: exit %d %q %q", code, out, errOut)
	}
	if code, out, _ := runCLI(t, "", "api", "-f", "name=New", "/v1/brands"); code != ExitOK || !strings.Contains(out, `"brand_2"`) ||
		(*lastBody)["name"] != "New" {
		t.Fatalf("api POST: exit %d %q body %v", code, out, *lastBody)
	}
	if code, out, _ := runCLI(t, "", "api", "nowhere"); code == ExitOK || !strings.Contains(out, "resource_missing") {
		t.Fatalf("api on a missing route: exit %d %q", code, out)
	}
	// --paginate follows has_more with starting_after and prints one list.
	if code, out, errOut := runCLI(t, "", "api", "posts", "--paginate", "--jq", "[.data[].id] | join(\",\")"); code != ExitOK ||
		out != "post_1,post_2,post_3,post_4,post_5\n" {
		t.Fatalf("--paginate: exit %d %q %q", code, out, errOut)
	}
	if code, _, errOut := runCLI(t, "", "api", "-X", "POST", "posts", "--paginate"); code != ExitUsage || !strings.Contains(errOut, "GET") {
		t.Fatalf("--paginate on a POST: exit %d %q", code, errOut)
	}
}
