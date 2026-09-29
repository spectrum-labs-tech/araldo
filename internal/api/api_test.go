// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	contract "github.com/spectrum-labs-tech/araldo/api"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
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
	tests := map[string]string{
		`{"nme":"x"}`: "parameter_unknown",
		`{"n":"x"}`:   "parameter_invalid",
		`{`:           "json_invalid",
	}
	for body, want := range tests {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		err := decode(r, &v)
		if ae := apperr.As(err); ae.Code != want {
			t.Errorf("decode(%s) code %q, want %q (%v)", body, ae.Code, want, err)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
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
	l := newLimiter(1, 3)
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
