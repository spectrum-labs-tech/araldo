// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/spectrum-labs-tech/araldo/internal/app"
	"github.com/spectrum-labs-tech/araldo/internal/config"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/netguard"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

type harness struct {
	t      *testing.T
	a      *app.App
	srv    *httptest.Server
	client *http.Client
	email  string
	pw     string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set")
	}
	cfg := config.Config{DatabaseURL: dsn, MasterKeys: "test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", BaseURL: "http://araldo.test",
		AutoMigrate: true, InsecureCookies: true, PrivateNetworks: netguard.Policy{All: true}, PrivateWebhooks: netguard.Policy{All: true}}
	a, err := app.Open(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	email := fmt.Sprintf("ui-%s@example.com", uuid.NewString()[:8])
	return &harness{t: t, a: a, srv: srv, client: client, email: email, pw: "a long test password"}
}

func (h *harness) get(path string) (int, string) {
	h.t.Helper()
	resp, err := h.client.Get(h.srv.URL + path) //nolint:noctx // test
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (h *harness) post(path string, form url.Values) (int, string, string) {
	h.t.Helper()
	resp, err := h.client.PostForm(h.srv.URL+path, form) //nolint:noctx // test
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (h *harness) csrf(page string) string {
	h.t.Helper()
	_, body := h.get(page)
	m := csrfRE.FindStringSubmatch(body)
	if m == nil {
		h.t.Fatalf("no CSRF token on %s", page)
	}
	return m[1]
}

func TestDashboardEndToEnd(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := t.Context()
	u, err := h.a.Svc.CreateUser(ctx, h.email, "UI", h.pw)
	if err != nil {
		t.Fatal(err)
	}
	org, err := h.a.Svc.CreateOrg(ctx, u.ID, "UI org")
	if err != nil {
		t.Fatal(err)
	}

	// Signed out: pages redirect to sign-in.
	if code, _ := h.get("/posts"); code != http.StatusSeeOther {
		t.Fatalf("signed-out /posts: %d", code)
	}
	if code, body := h.get("/login"); code != http.StatusOK || !strings.Contains(body, "Sign in") {
		t.Fatalf("/login: %d", code)
	}
	if code, _, body := h.post("/login", url.Values{"email": {h.email}, "password": {"wrong password!!"}}); code != http.StatusUnauthorized ||
		!strings.Contains(body, "incorrect") {
		t.Fatalf("bad login: %d", code)
	}
	if code, loc, _ := h.post("/login", url.Values{"email": {h.email}, "password": {h.pw}}); code != http.StatusSeeOther || loc != "/" {
		t.Fatalf("login: %d → %s", code, loc)
	}

	// Brand, channel, template, post through the forms.
	csrf := h.csrf("/brands/new")
	code, loc, body := h.post("/brands", url.Values{"csrf": {csrf}, "name": {"Araldo"}, "timezone": {"America/Denver"}, "approval_policy": {"none"}})
	if code != http.StatusSeeOther {
		t.Fatalf("create brand: %d %s", code, body)
	}
	brandRef := strings.TrimPrefix(strings.Split(loc, "&")[0], "/channels/new?brand=")
	code, _, body = h.post("/channels", url.Values{"csrf": {csrf}, "provider": {"sandbox"}, "brand": {brandRef}, "field_emulates": {"bluesky"}})
	if code != http.StatusSeeOther {
		t.Fatalf("connect channel: %d %s", code, body)
	}
	tplForm := url.Values{"csrf": {csrf}, "brand": {brandRef}, "key": {"featured-build"}, "name": {"Featured build"},
		"body":      {"Featured: {{.name}} {{.url}}"},
		"variables": {`{"type":"object","required":["name","url"],"properties":{"name":{"type":"string"},"url":{"type":"string"}}}`},
		"example":   {`{"name":"Atlas","url":"https://araldo.dev/b/1"}`}}
	code, loc, body = h.post("/templates", tplForm)
	if code != http.StatusSeeOther {
		t.Fatalf("create template: %d %s", code, body)
	}
	tplPath := strings.Split(loc, "?")[0]
	// Live preview.
	code, _, body = h.post("/preview", tplForm)
	if code != http.StatusOK || !strings.Contains(body, "Featured: Atlas https://araldo.dev/b/1") {
		t.Fatalf("preview: %d %s", code, body)
	}
	tplID := strings.TrimPrefix(tplPath, "/templates/")
	code, loc, body = h.post("/posts", url.Values{"csrf": {csrf}, "mode": {"template"}, "template": {tplID},
		"data": {`{"name":"Atlas","url":"https://araldo.dev/b/1"}`}, "publish": {"now"}, "action": {"create"}})
	if code != http.StatusSeeOther {
		t.Fatalf("create post: %d %s", code, body)
	}
	postPath := strings.Split(loc, "?")[0]
	code, _, body = h.post("/posts", url.Values{"csrf": {csrf}, "mode": {"content"}, "brand": {brandRef}, "body": {strings.Repeat("too long ", 50)},
		"publish": {"now"}, "action": {"preview"}})
	if code != http.StatusOK || !strings.Contains(body, "Bluesky allows 300") {
		t.Fatalf("preview post: %d", code)
	}
	// Publishing claims due posts in every org, and other tests publish at the
	// same time, so any worker may take this post; wait until it is done.
	actor, _, _ := h.a.Svc.MemberActor(ctx, u.ID, org.ID, false, "")
	pid, _ := id.Parse(id.Post, strings.TrimPrefix(postPath, "/posts/"))
	var p *model.Post
	for deadline := time.Now().Add(15 * time.Second); ; {
		if _, err := h.a.Svc.PublishDue(ctx, "ui-test"); err != nil {
			t.Fatal(err)
		}
		if p, err = h.a.Svc.Post(ctx, actor, pid); err != nil {
			t.Fatal(err)
		}
		if p.Status == model.PostPublished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("post not published: %s %+v", p.Status, p.Targets)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Every page renders.
	pages := []string{"/", "/posts", "/posts/new", postPath, "/templates", "/templates/new", tplPath, "/channels", "/channels/new",
		"/channels/new?provider=sandbox", "/brands", "/brands/new", "/brands/" + brandRef, "/keys", "/webhooks", "/events", "/org",
		"/org/audit", "/org/tasks", "/account", "/posts?status=published"}
	for _, p := range pages {
		code, body := h.get(p)
		if code != http.StatusOK {
			t.Errorf("GET %s: %d %.300s", p, code, body)
		}
		if strings.Contains(body, "html/template") || strings.Contains(body, "executing \"") || strings.Contains(body, "Something went wrong") {
			t.Errorf("GET %s rendered an error: %.300s", p, body)
		}
	}
	if _, body := h.get(postPath); !strings.Contains(body, ">published<") {
		t.Errorf("post page does not show published: %.500s", body)
	}

	// Sandbox permalink. Whichever worker published it used its own base URL,
	// so only the path is ours to follow.
	link, err := url.Parse(p.Targets[0].Permalink)
	if err != nil {
		t.Fatal(err)
	}
	if code, body := h.get(link.Path); code != http.StatusOK || !strings.Contains(body, "Atlas") {
		t.Errorf("sandbox page %s: %d", link.Path, code)
	}

	// MFA setup shows a QR code (the login gave sudo mode).
	code, _, body = h.post("/account/mfa/begin", url.Values{"csrf": {csrf}})
	if code != http.StatusOK || !strings.Contains(body, "data:image/png;base64,") {
		t.Errorf("MFA setup: %d", code)
	}

	// Creating an API key shows it once.
	code, _, body = h.post("/keys", url.Values{"csrf": {csrf}, "name": {"ci"}, "access": {"full"}})
	if code != http.StatusOK || !strings.Contains(body, "ald_test_") {
		t.Fatalf("create key: %d", code)
	}

	// CSRF is enforced.
	if code, _, _ := h.post("/brands", url.Values{"name": {"x"}}); code != http.StatusForbidden {
		t.Errorf("POST without CSRF: %d", code)
	}

	// Switching to live mode shows the banner-free live view.
	if code, _, _ := h.post("/context", url.Values{"csrf": {csrf}, "mode": {"live"}, "back": {"/channels"}}); code != http.StatusSeeOther {
		t.Errorf("switch to live: %d", code)
	}
	if _, body := h.get("/channels/new"); !strings.Contains(body, "Bluesky") || strings.Contains(body, "Test mode · posts go") {
		t.Errorf("live connect page wrong")
	}

	// Sign out.
	if code, _, _ := h.post("/logout", url.Values{"csrf": {csrf}}); code != http.StatusSeeOther {
		t.Errorf("logout: %d", code)
	}
	if code, _ := h.get("/"); code != http.StatusSeeOther {
		t.Errorf("after logout: %d", code)
	}
}

func TestAPIEndToEnd(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := t.Context()
	u, err := h.a.Svc.CreateUser(ctx, h.email, "", h.pw)
	if err != nil {
		t.Fatal(err)
	}
	org, err := h.a.Svc.CreateOrg(ctx, u.ID, "API org")
	if err != nil {
		t.Fatal(err)
	}
	login, err := h.a.Svc.Login(ctx, h.email, h.pw, "", "")
	if err != nil {
		t.Fatal(err)
	}
	owner, _, _ := h.a.Svc.MemberActor(ctx, u.ID, org.ID, false, "")
	key, _, err := h.a.Svc.CreateAPIKey(ctx, owner, login.Session, core.APIKeyInput{Name: "t"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, idem string) (int, http.Header, map[string]any) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, h.srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		if idem != "" {
			req.Header.Set("Idempotency-Key", idem)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, resp.Header, out
	}
	code, _, b := call("POST", "/v1/brands", `{"name":"Northwind","timezone":"UTC"}`, "")
	if code != 201 || b["slug"] != "northwind" {
		t.Fatalf("create brand: %d %v", code, b)
	}
	code, _, ch := call("POST", "/v1/channels", `{"brand":"northwind","provider":"sandbox","emulates":"x"}`, "")
	if code != 201 || ch["emulates"] != "x" {
		t.Fatalf("create channel: %d %v", code, ch)
	}
	code, _, b = call("POST", "/v1/posts/preview", `{"brand":"northwind","content":{"body":"`+strings.Repeat("a", 300)+`"}}`, "")
	if code != 200 || b["valid"] != false {
		t.Fatalf("preview: %d %v", code, b)
	}
	body := `{"brand":"northwind","content":{"body":"Launch day"},"metadata":{"release":"v1"}}`
	code, h1, first := call("POST", "/v1/posts", body, "launch-1")
	if code != 201 || h1.Get("Idempotent-Replayed") != "" {
		t.Fatalf("create post: %d %v", code, first)
	}
	code, h2, again := call("POST", "/v1/posts", body, "launch-1")
	if code != 201 || h2.Get("Idempotent-Replayed") != "true" || again["id"] != first["id"] {
		t.Fatalf("replay: %d %v %v", code, h2, again["id"])
	}
	if code, _, b := call("POST", "/v1/posts", `{"brand":"northwind","content":{"body":"other"}}`, "launch-1"); code != 409 || b["code"] != "idempotency_key_reused" {
		t.Fatalf("reused key: %d %v", code, b)
	}
	if code, _, b := call("POST", "/v1/posts", `{"brand":"northwind","contnet":{}}`, ""); code != 400 || b["code"] != "parameter_unknown" {
		t.Fatalf("typo: %d %v", code, b)
	}
	if code, _, b := call("GET", "/v1/posts?metadata[release]=v1", "", ""); code != 200 || len(b["data"].([]any)) != 1 {
		t.Fatalf("list by metadata: %d %v", code, b)
	}
	if code, _, _ := call("GET", "/v1/posts/"+id.Make(id.Post), "", ""); code != 404 {
		t.Fatalf("missing post: %d", code)
	}
	if code, _, b := call("GET", "/v1/platforms", "", ""); code != 200 || len(b["data"].([]any)) != len(platform.Emulable()) {
		t.Fatalf("platforms: %d", code)
	}
	code, _, ep := call("POST", "/v1/webhook_endpoints", `{"url":"http://127.0.0.1:9/hook","enabled_events":["post.published"]}`, "")
	if code != 201 || !strings.HasPrefix(fmt.Sprint(ep["secret"]), "whsec_") {
		t.Fatalf("webhook: %d %v", code, ep)
	}
	if code, _, b := call("GET", "/v1/events?limit=5", "", ""); code != 200 || b["object"] != "list" {
		t.Fatalf("events: %d %v", code, b)
	}
}

// TestOpenDoesNotWaitLongForAMigration checks startup goes on while
// another process holds the migration lock (a long index build in a
// rolling deploy), instead of blocking until it is free. It holds the
// shared lock and shortens MigrateWait, so it does not run in parallel.
func TestOpenDoesNotWaitLongForAMigration(t *testing.T) {
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set")
	}
	old := app.MigrateWait
	app.MigrateWait = 300 * time.Millisecond
	t.Cleanup(func() { app.MigrateWait = old })
	pc, err := pgxpool.ParseConfig(dsn) // the test URL carries pool settings
	if err != nil {
		t.Fatal(err)
	}
	holder, err := pgx.ConnectConfig(t.Context(), pc.ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close(context.Background()) }()
	// Polled, never waited for in one statement: a statement waiting for
	// the lock is an open transaction, which another package's migration,
	// building an index concurrently, would wait for in turn (see
	// store.Migrate).
	for got := false; !got; {
		if err := holder.QueryRow(t.Context(), `SELECT pg_try_advisory_lock($1)`, int64(0x61726c646f6d67)).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if !got {
			time.Sleep(50 * time.Millisecond)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	start := time.Now()
	a, err := app.Open(ctx, config.Config{DatabaseURL: dsn, MasterKeys: "test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		BaseURL: "http://araldo.test", AutoMigrate: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("Open waited %s for a migration lock held elsewhere", waited)
	}
	cancel() // the background migration gives up the wait for the lock
	if _, err := holder.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(0x61726c646f6d67)); err != nil {
		t.Fatal(err)
	}
}
