// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/authn"
)

// testOperatorKey is the only key fakeOperator takes; revokedOperatorKey
// is well formed, but refused.
var testOperatorKey, revokedOperatorKey = authn.NewOperatorKey(), authn.NewOperatorKey()

// fakeOperator answers the operator API routes the admin commands use, for
// testOperatorKey only, and records each request as "METHOD path?query
// body". Its master keys need three batches of 100 to rotate, and leave
// unconfigured data keys if asked.
type fakeOperator struct {
	mu           sync.Mutex
	requests     []string
	remaining    int
	unconfigured int
}

func (f *fakeOperator) log() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.requests, "\n")
}

func (f *fakeOperator) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+string(body)))
		if r.Header.Get("Authorization") != "Bearer "+testOperatorKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":"operator_key_invalid","detail":"Invalid operator key.","status":401}`)
			return
		}
		q := r.URL.Query()
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/operator/orgs":
			switch q.Get("name") {
			case "Spectrum Labs":
				_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"org_1","name":"Spectrum Labs"}]}`)
			case "Twin":
				_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"org_2","name":"Twin"},{"id":"org_3","name":"Twin"}]}`)
			default:
				_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
			}
		case "GET /v1/operator/users":
			if q.Get("email") == "them@example.com" {
				_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"user_7","email":"them@example.com"}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
		case "POST /v1/operator/users":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"user_8","object":"user","email":"new@example.com"}`)
		case "POST /v1/operator/users/user_7/password":
			_, _ = io.WriteString(w, `{"id":"user_7","object":"user","email":"them@example.com"}`)
		case "POST /v1/operator/orgs/org_1/api_keys":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"key_1","object":"api_key","name":"ci","hint":"…abcd","secret":"ald_test_secret"}`)
		case "POST /v1/operator/orgs/org_1/members", "POST /v1/operator/orgs/org_1/members/user_7":
			_, _ = io.WriteString(w, `{"id":"user_7","object":"member","role":"admin"}`)
		case "GET /v1/operator/master_keys":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"k2","configured":true,"primary":true,"data_keys":3},`+
				`{"id":"k1","configured":true,"primary":false,"data_keys":0}]}`)
		case "POST /v1/operator/master_keys/rewrap":
			n := min(f.remaining, 100)
			f.remaining -= n
			_, _ = fmt.Fprintf(w, `{"object":"master_key_rewrap","rewrapped":%d,"remaining":%d,"unconfigured":%d}`, n, f.remaining, f.unconfigured)
		case "GET /v1/operator/operator_keys":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"opkey_1","name":"billing","hint":"…wxyz","last_used_at":"2026-11-01T10:00:00Z"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"route_unknown","detail":"No such route.","status":404}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// operatorEnv points admin commands at srv with key.
func operatorEnv(t *testing.T, srv *httptest.Server, key string) {
	t.Helper()
	t.Setenv("ARALDO_OPERATOR_HOST", srv.URL)
	t.Setenv("ARALDO_OPERATOR_KEY", key)
}

// Admin commands need an operator key from the environment, never a flag,
// and say where to get one. They set environment variables, so cannot run
// in parallel.
func TestAdminNeedsAnOperatorKey(t *testing.T) {
	f := &fakeOperator{}
	srv := f.serve(t)
	tests := []struct {
		name, key string
		wantCode  int
		want      string
	}{
		{"no key", "", ExitUsage, "araldo admin bootstrap --operator-key"},
		{"an org key", "ald_live_abc", ExitUsage, "ARALDO_OPERATOR_KEY is not an operator key"},
		{"a mistyped key", testOperatorKey[:len(testOperatorKey)-1], ExitUsage, "copied in full"},
		{"a revoked key", revokedOperatorKey, ExitError, "the operator key is refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			operatorEnv(t, srv, tt.key)
			code, _, errOut := runCLI(t, "", "admin", "operator-keys", "list")
			if code != tt.wantCode || !strings.Contains(errOut, tt.want) {
				t.Fatalf("exit %d %q, want %d with %q", code, errOut, tt.wantCode, tt.want)
			}
		})
	}
	// The key is a header, never in the URL.
	if strings.Contains(f.log(), revokedOperatorKey) {
		t.Fatalf("the key went into a request line or body:\n%s", f.log())
	}
	// --hostname wins over ARALDO_OPERATOR_HOST.
	operatorEnv(t, srv, testOperatorKey)
	t.Setenv("ARALDO_OPERATOR_HOST", "http://127.0.0.1:1")
	if code, out, errOut := runCLI(t, "", "admin", "operator-keys", "list", "--hostname", srv.URL); code != ExitOK || !strings.Contains(out, "opkey_1") {
		t.Fatalf("--hostname: exit %d %q %q", code, out, errOut)
	}
}

// Admin commands call the operator API, keeping their output: the key
// alone on stdout, an org found by name, and --as refused.
func TestAdminOverTheOperatorAPI(t *testing.T) {
	f := &fakeOperator{}
	operatorEnv(t, f.serve(t), testOperatorKey)

	code, out, errOut := runCLI(t, "", "admin", "apikeys", "create", "--org", "Spectrum Labs", "--name", "ci", "--scopes", "posts:write", "--live")
	if code != ExitOK || out != "ald_test_secret\n" || !strings.Contains(errOut, `Created API key "ci" (…abcd) in Spectrum Labs.`) {
		t.Fatalf("apikeys create: exit %d %q %q", code, out, errOut)
	}
	if !strings.Contains(f.log(), `POST /v1/operator/orgs/org_1/api_keys {"livemode":true,"name":"ci","scopes":["posts:write"]}`) {
		t.Fatalf("apikeys create sent:\n%s", f.log())
	}
	if code, _, errOut = runCLI(t, "", "admin", "apikeys", "create", "--org", "Twin", "--name", "ci"); code != ExitError || !strings.Contains(errOut, "org_2, org_3") {
		t.Fatalf("an ambiguous org: exit %d %q", code, errOut)
	}
	if code, _, errOut = runCLI(t, "", "admin", "apikeys", "create", "--org", "Nowhere", "--name", "ci"); code != ExitError || !strings.Contains(errOut, `no org is named "Nowhere"`) {
		t.Fatalf("no such org: exit %d %q", code, errOut)
	}
	if code, _, errOut = runCLI(t, "", "admin", "members", "list", "--as", "you@example.com"); code != ExitUsage || !strings.Contains(errOut, "--as is no longer supported") {
		t.Fatalf("--as: exit %d %q", code, errOut)
	}

	// Members: the person is found by email, then changed by ID.
	if code, _, errOut = runCLI(t, "", "admin", "members", "role", "--org", "Spectrum Labs", "--email", "them@example.com", "--role", "admin"); code != ExitOK ||
		!strings.Contains(errOut, "them@example.com is now admin in Spectrum Labs.") || !strings.Contains(f.log(), `POST /v1/operator/orgs/org_1/members/user_7 {"role":"admin"}`) {
		t.Fatalf("members role: exit %d %q\n%s", code, errOut, f.log())
	}
	// Someone without an account gets a temporary password, sent once and
	// printed alone on stdout.
	code, out, errOut = runCLI(t, "", "admin", "members", "add", "--org", "Spectrum Labs", "--email", "new@example.com", "--role", "editor")
	if code != ExitOK || len(strings.TrimSpace(out)) != 23 || !strings.Contains(errOut, "temporary password") ||
		!strings.Contains(f.log(), `"password":"`+strings.TrimSpace(out)+`"`) {
		t.Fatalf("members add: exit %d %q %q", code, out, errOut)
	}

	// Users: a password from stdin is sent, never printed.
	if code, out, _ = runCLI(t, "a long chosen password\n", "admin", "users", "reset-password", "--email", "them@example.com", "--password-stdin"); code != ExitOK ||
		strings.Contains(out, "a long chosen password") || !strings.Contains(f.log(), `POST /v1/operator/users/user_7/password {"password":"a long chosen password"}`) {
		t.Fatalf("users reset-password: exit %d %q", code, out)
	}
	if code, _, errOut = runCLI(t, "", "admin", "users", "reset-password", "--email", "nobody@example.com"); code != ExitError || !strings.Contains(errOut, "no user has the email") {
		t.Fatalf("resetting nobody: exit %d %q", code, errOut)
	}
	if code, out, _ = runCLI(t, "", "admin", "users", "create", "--email", "new@example.com"); code != ExitOK || !strings.Contains(out, "Created new@example.com.\nPassword: ") {
		t.Fatalf("users create: exit %d %q", code, out)
	}

	// Master keys: status as before, rotation in batches until done.
	if code, out, _ = runCLI(t, "", "admin", "keys", "status"); code != ExitOK || !strings.Contains(out, "primary") || !strings.Contains(out, "wraps nothing: can be removed") {
		t.Fatalf("keys status: exit %d %q", code, out)
	}
	f.mu.Lock()
	f.remaining = 250
	f.mu.Unlock()
	code, out, errOut = runCLI(t, "", "admin", "keys", "rotate")
	if code != ExitOK || !strings.HasPrefix(out, "Rewrapped 250 data keys under the primary master key.") || !strings.Contains(errOut, "Rewrapped 200 data keys; 50 to go.") ||
		strings.Count(f.log(), `master_keys/rewrap {"batch_size":100}`) != 3 {
		t.Fatalf("keys rotate: exit %d %q %q\n%s", code, out, errOut, f.log())
	}
	f.mu.Lock()
	f.unconfigured = 2
	f.mu.Unlock()
	if code, out, errOut = runCLI(t, "", "admin", "keys", "rotate", "--batch-size", "10"); code != ExitError || !strings.Contains(out, "Rewrapped 0") ||
		!strings.Contains(errOut, "2 data keys are wrapped by a master key the server does not have") {
		t.Fatalf("keys rotate with keys it cannot read: exit %d %q %q", code, out, errOut)
	}
	if code, _, _ = runCLI(t, "", "admin", "keys", "rotate", "--batch-size", "0"); code != ExitUsage {
		t.Fatalf("a batch of 0: exit %d", code)
	}
}

// bootstrap's flags: --operator-key alone needs no owner, anything else
// still does. It stops before opening the database.
func TestBootstrapFlags(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	if code := Run(t.Context(), []string{"admin", "bootstrap", "--org", "Araldo"}, &stdout, &stderr); code != ExitUsage ||
		!strings.Contains(stderr.String(), "--operator-key alone") {
		t.Fatalf("without an email: exit %d %q", code, stderr.String())
	}
	if code := Run(t.Context(), []string{"admin", "bootstrap", "--operator-key", "--brand", "x"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("a brand with no owner: exit %d", code)
	}
}
