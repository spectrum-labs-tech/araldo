// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	contract "github.com/spectrum-labs-tech/araldo/api"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

func TestContractIsValidOpenAPI(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(t.Context()); err != nil {
		t.Fatalf("contract is not valid OpenAPI: %v", err)
	}
}

// The contract's event types are the events Araldo emits, which webhook
// endpoints can subscribe to.
func TestEventTypesMatchContract(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	var inContract []string
	for _, v := range doc.Components.Schemas["EventType"].Value.Enum {
		inContract = append(inContract, v.(string))
	}
	inCode := slices.Clone(core.EventTypes)
	sort.Strings(inContract)
	sort.Strings(inCode)
	if !slices.Equal(inContract, inCode) {
		t.Fatalf("EventType in api/openapi.yaml:\n%v\ncore.EventTypes:\n%v", inContract, inCode)
	}
}

// Every route the server has is in the contract, and every operation in
// the contract has a route (ADR 0005).
func TestRoutesMatchContract(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	var inContract []string
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			inContract = append(inContract, method+" "+path)
		}
	}
	h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	inServer := slices.Clone(h.Routes)
	sort.Strings(inContract)
	sort.Strings(inServer)
	for _, r := range inServer {
		if !slices.Contains(inContract, r) {
			t.Errorf("route %q is served but not in api/openapi.yaml", r)
		}
	}
	for _, r := range inContract {
		if !slices.Contains(inServer, r) {
			t.Errorf("operation %q is in api/openapi.yaml but not served", r)
		}
	}
}

// Every route takes exactly the query parameters the contract lists, so a
// parameter the server would ignore is refused instead (ADR 0019).
func TestQueryParametersMatchContract(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, route := range h.Routes {
		method, path, _ := strings.Cut(route, " ")
		item := doc.Paths.Value(path)
		if item == nil || item.GetOperation(method) == nil {
			continue // TestRoutesMatchContract reports it
		}
		var inContract []string
		for _, p := range append(item.Parameters, item.GetOperation(method).Parameters...) {
			if p.Value != nil && p.Value.In == "query" {
				inContract = append(inContract, p.Value.Name)
			}
		}
		served := slices.Clone(h.Query[route])
		sort.Strings(inContract)
		sort.Strings(served)
		if !slices.Equal(inContract, served) {
			t.Errorf("%s: the server takes query parameters %v, the contract lists %v", route, served, inContract)
		}
	}
}

func TestUnknownQueryParametersAreRefused(t *testing.T) {
	t.Parallel()
	allowed := paged("brand", "metadata")
	tests := map[string]string{
		"/x?brand=a&limit=5":         "",
		"/x?metadata[build_id]=8812": "",
		"/x?brnd=a":                  "parameter_unknown",
		"/x?metadata=1":              "",
		"/x?metadatafoo=1":           "parameter_unknown",
	}
	for target, want := range tests {
		err := checkQuery(httptest.NewRequest(http.MethodGet, target, nil), allowed)
		if got := apperr.As(err).Code; err != nil && got != want || err == nil && want != "" {
			t.Errorf("%s: %v, want %q", target, err, want)
		}
	}
	if err := checkQuery(httptest.NewRequest(http.MethodGet, "/x?brand=a", nil), nil); apperr.As(err).Code != "parameter_unknown" {
		t.Errorf("a route without query parameters accepted one: %v", err)
	}
}

func TestUnauthenticatedRequestsGetAProblem(t *testing.T) {
	t.Parallel()
	h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/posts", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"code":"api_key_missing"`) {
		t.Fatalf("body %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/openapi.yaml", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "openapi: 3.0.3") {
		t.Fatalf("contract endpoint: %d", rec.Code)
	}
}

func TestDecodeIsStrict(t *testing.T) {
	t.Parallel()
	var v struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	tests := map[string]struct{ code, param string }{
		`{"nme":"x"}`:                  {"parameter_unknown", "nme"},
		`{"n":"x"}`:                    {"parameter_invalid", "n"},
		`{`:                            {"json_invalid", ""},
		`{"name":"a"} {"n":1}`:         {"json_invalid", ""}, // a second value
		`{"name":"a"} trailing`:        {"json_invalid", ""},
		"{\"name\":\"a\"}\n\t ":        {"", ""}, // only space after it
		strings.Repeat(" ", maxBody+1): {"body_too_large", ""},
	}
	for body, want := range tests {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		err := decode(r, &v)
		if ae := apperr.As(err); want.code == "" && err != nil || want.code != "" && (ae.Code != want.code || ae.Param != want.param) {
			t.Errorf("decode(%.40q) = %v, want code %q param %q", body, err, want.code, want.param)
		}
	}
	// A body that cannot be read is not "too large".
	r := httptest.NewRequest(http.MethodPost, "/", iotest.ErrReader(errors.New("connection reset")))
	if err := decode(r, &v); apperr.As(err).Code != "body_unreadable" {
		t.Errorf("an unreadable body: %v", err)
	}
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	if err := decode(r, &v); err != nil {
		t.Errorf("empty body: %v", err)
	}
}

func TestPage(t *testing.T) {
	t.Parallel()
	good := id.Make(id.Post)
	if p, err := page(httptest.NewRequest(http.MethodGet, "/?limit=5&starting_after="+good, nil), id.Post); err != nil || p.Limit != 5 {
		t.Fatalf("page = %+v, %v", p, err)
	}
	for _, q := range []string{"limit=0", "limit=101", "limit=x", "starting_after=tmpl_01h455vb4pex5vsknk084sn02q",
		"starting_after=" + good + "&ending_before=" + good} {
		if _, err := page(httptest.NewRequest(http.MethodGet, "/?"+q, nil), id.Post); err == nil {
			t.Errorf("page(%s) accepted", q)
		}
	}
}

func TestLimiter(t *testing.T) {
	t.Parallel()
	l := newLimiter(1, 3, false)
	now := time.Unix(1000, 0)
	for i := range 3 {
		if _, _, _, ok := l.take("k", now); !ok {
			t.Fatalf("request %d refused within the burst", i+1)
		}
	}
	if _, remaining, reset, ok := l.take("k", now); ok || remaining != 0 || reset < 1 {
		t.Fatalf("4th request: ok %v remaining %d reset %d", ok, remaining, reset)
	}
	if _, _, _, ok := l.take("other", now); !ok {
		t.Fatal("another key was limited")
	}
	if _, _, _, ok := l.take("k", now.Add(time.Second)); !ok {
		t.Fatal("no token after refill")
	}
}

// TestLimiterForgetsOneByOne checks a full limiter forgets only buckets
// that have refilled, never an active one's count; a strict one refuses a
// new key while all are in use, a lenient one tracks it anyway.
func TestLimiterForgetsOneByOne(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	for _, strict := range []bool{true, false} {
		l := newLimiter(1, 3, strict)
		l.tracked = 2
		for range 3 {
			l.take("busy", now)
		}
		l.take("idle", now.Add(-time.Hour)) // refilled long ago
		if _, _, _, ok := l.take("new", now); !ok {
			t.Fatalf("strict %v: a new key refused while an idle bucket could go", strict)
		}
		if _, _, _, ok := l.take("busy", now); ok {
			t.Fatalf("strict %v: the busy key's count was reset", strict)
		}
		_, _, _, ok := l.take("another", now) // busy and new are both in use
		if ok == strict {
			t.Fatalf("strict %v: a new key past the cap allowed %v", strict, ok)
		}
	}
}

// A path no route takes is a problem like any other error, not net/http's
// plain text: 405 with Allow when the path takes other methods, else 404.
func TestUnknownRoutesGetAProblem(t *testing.T) {
	t.Parallel()
	h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tests := []struct {
		method, path string
		status       int
		code, allow  string
	}{
		{http.MethodGet, "/v1/nope", http.StatusNotFound, `"code":"route_unknown"`, ""},
		{http.MethodDelete, "/v1/brands", http.StatusMethodNotAllowed, `"code":"method_not_allowed"`, "GET, POST"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
		if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.code) || rec.Header().Get("Allow") != tt.allow ||
			rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s %s = %d %q (Allow %q, %s)", tt.method, tt.path, rec.Code, rec.Body.String(), rec.Header().Get("Allow"),
				rec.Header().Get("Content-Type"))
		}
	}
}
