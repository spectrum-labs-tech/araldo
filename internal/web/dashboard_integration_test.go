// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform/linkedin"
	"github.com/spectrum-labs-tech/araldo/internal/platform/threads"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/sandbox"
	"github.com/spectrum-labs-tech/araldo/internal/store"
	"github.com/spectrum-labs-tech/araldo/internal/web"
)

// One store for the package's tests: a pool per test would exhaust
// Postgres's connections when every package runs at once.
var (
	setupOnce sync.Once
	shared    *store.Store
	setupErr  error
)

// dash is a signed-in owner's dashboard over a fresh org with one brand
// and a sandbox channel imitating Bluesky. Tests never clean up.
type dash struct {
	s     *core.Service
	st    *store.Store
	login *core.LoginResult
	owner core.Actor
	brand *model.Brand
	send  func(*http.Request) *httptest.ResponseRecorder
	// srv serves requests with no session.
	srv http.Handler
}

// newDash is a dashboard signed in as a new org's owner, in test mode, with
// the sandbox and any extra adapters.
func newDash(t *testing.T, extra ...platform.Adapter) *dash {
	t.Helper()
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	ctx := t.Context()
	setupOnce.Do(func() {
		shared, setupErr = store.Open(context.Background(), dsn)
		if setupErr == nil {
			setupErr = shared.Migrate(context.Background())
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	st := shared
	mk, err := keyring.ParseMasterKeys("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	adapters := append([]platform.Adapter{sandbox.New("https://araldo.test")}, extra...)
	kr := keyring.New(mk, st)
	s := core.New(st, kr, platform.NewRegistry(adapters...), log, core.Config{BaseURL: "https://araldo.test"})
	email := fmt.Sprintf("web-%s@example.com", uuid.NewString()[:8])
	u, err := s.CreateUser(ctx, email, "Owner", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	org, err := s.CreateOrg(ctx, u.ID, "Org "+email)
	if err != nil {
		t.Fatal(err)
	}
	login, err := s.Login(ctx, email, "correct horse battery", "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := s.MemberActor(ctx, u.ID, org.ID, false, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBrand(ctx, owner, core.BrandInput{Name: "Araldo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConnectChannel(ctx, owner, core.ConnectInput{BrandID: b.ID, Provider: platform.Sandbox, Fields: map[string]string{"emulates": "bluesky"}}); err != nil {
		t.Fatal(err)
	}
	srv, err := web.New(s, log, web.Config{})
	if err != nil {
		t.Fatal(err)
	}
	send := func(r *http.Request) *httptest.ResponseRecorder {
		r.AddCookie(&http.Cookie{Name: "araldo_session", Value: login.Token})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		return rec
	}
	return &dash{s: s, st: st, login: login, owner: owner, brand: b, send: send, srv: srv}
}

// TestPostFormCarriesImages drives the new-post form as a browser would: an
// upload with a preview, which must keep the image (browsers forget files),
// then scheduling with an edited alt text.
func TestPostFormCarriesImages(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	s, login, b, owner, send := d.s, d.login, d.brand, d.owner, d.send
	post := func(fields [][2]string, file []byte) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("csrf", login.Session.CSRFToken)
		for _, f := range fields {
			_ = w.WriteField(f[0], f[1])
		}
		name := "build.png"
		if file == nil {
			name = "" // what browsers send for an empty file input
		}
		fw, _ := w.CreateFormFile("images", name)
		_, _ = fw.Write(file)
		_ = w.Close()
		r := httptest.NewRequest(http.MethodPost, "/posts", &buf)
		r.Header.Set("Content-Type", w.FormDataContentType())
		return send(r)
	}
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 64, 48))); err != nil {
		t.Fatal(err)
	}
	form := [][2]string{{"mode", "content"}, {"brand", id.Format(id.Brand, b.ID)}, {"body", "New build"}, {"fit", "error"}, {"publish", "now"}}

	rec := post(append(form, [2]string{"action", "preview"}, [2]string{"new_alt", "Front"}), img.Bytes())
	page := rec.Body.String()
	m := regexp.MustCompile(`name="media" value="(media_[0-9a-z]+)"`).FindStringSubmatch(page)
	if rec.Code != http.StatusOK || m == nil || !strings.Contains(page, `src="/media/`+m[1]+`"`) || !strings.Contains(page, `value="Front"`) {
		t.Fatalf("preview: %d, image kept: %v\n%s", rec.Code, m != nil, page)
	}
	mediaRef := m[1]

	rec = send(httptest.NewRequest(http.MethodGet, "/media/"+mediaRef, nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), img.Bytes()) {
		t.Fatalf("GET /media: %d %q, %d bytes", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}

	rec = post(append(form, [2]string{"action", "create"}, [2]string{"media", mediaRef}, [2]string{"alt_" + mediaRef, "Front view"}), nil)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/posts/post_") {
		t.Fatalf("create: %d %q\n%s", rec.Code, loc, rec.Body)
	}
	pid, err := id.Parse(id.Post, strings.SplitN(strings.TrimPrefix(loc, "/posts/"), "?", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Post(ctx, owner, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Media) != 1 || id.Format(id.Media, p.Media[0].ID) != mediaRef || p.Media[0].Alt != "Front view" {
		t.Fatalf("post media %+v", p.Media)
	}
	rec = send(httptest.NewRequest(http.MethodGet, loc, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `src="/media/`+mediaRef+`"`) {
		t.Fatalf("post page: %d, shows the image: %v", rec.Code, strings.Contains(rec.Body.String(), mediaRef))
	}
}

func TestPerformancePage(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	p, err := d.s.CreatePost(ctx, d.owner, core.PostInput{BrandID: d.brand.ID, Content: &model.Content{Body: "Launch day for the new site"}})
	if err != nil {
		t.Fatal(err)
	}
	var tg model.Target
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := d.s.PublishDue(ctx, "web-test"); err != nil {
			t.Fatal(err)
		}
		got, err := d.s.Post(ctx, d.owner, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if tg = got.Targets[0]; tg.Status == model.TargetPublished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not published: %s", tg.Status)
		}
	}

	// Before any reading the pages say so.
	rec := d.send(httptest.NewRequest(http.MethodGet, "/performance", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Nothing read yet") {
		t.Fatalf("performance before readings: %d\n%s", rec.Code, rec.Body)
	}

	// The collector's work, on this test's own target.
	views := int64(900)
	if err := d.st.RecordEngagement(ctx, tg.OrgID, tg.ID, time.Now(), platform.Counts{Likes: 41, Reposts: 7, Replies: 3, Quotes: 1, Views: &views}, nil); err != nil {
		t.Fatal(err)
	}
	rec = d.send(httptest.NewRequest(http.MethodGet, "/performance?days=7&brand="+id.Format(id.Brand, d.brand.ID), nil))
	page := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(page, "Launch day for the new site") || !strings.Contains(page, `<td class="text-right">52</td>`) ||
		!strings.Contains(page, "Written by hand") || !strings.Contains(page, "Sandbox Bluesky") {
		t.Fatalf("performance with a reading: %d\n%s", rec.Code, page)
	}
	rec = d.send(httptest.NewRequest(http.MethodGet, "/posts/"+id.Format(id.Post, p.ID), nil))
	if !strings.Contains(rec.Body.String(), "41 likes · 7 reposts · 3 replies · 1 quotes · 900 views") {
		t.Fatalf("post page lacks its engagement:\n%s", rec.Body)
	}
	rec = d.send(httptest.NewRequest(http.MethodGet, "/posts", nil))
	if !strings.Contains(rec.Body.String(), "<td>52</td>") {
		t.Fatalf("posts list lacks the engagement total:\n%s", rec.Body)
	}
}

func TestRollKeyKeepsTheOldSecretForADay(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	oldKey, k, err := d.s.CreateAPIKey(ctx, d.owner, d.login.Session, core.APIKeyInput{Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf": {d.login.Session.CSRFToken}}
	r := httptest.NewRequest(http.MethodPost, "/keys/"+id.Format(id.APIKey, k.ID)+"/roll", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := d.send(r)
	page := rec.Body.String()
	m := regexp.MustCompile(`id="new-key" class="copy-box">(ald_test_[^<]+)<`).FindStringSubmatch(page)
	if rec.Code != http.StatusOK || m == nil || !strings.Contains(page, "keeps working for 24 hours") {
		t.Fatalf("roll: %d\n%s", rec.Code, page)
	}
	if _, err := d.s.AuthenticateKey(ctx, m[1], ""); err != nil {
		t.Fatalf("the new key: %v", err)
	}
	if _, err := d.s.AuthenticateKey(ctx, oldKey, ""); err != nil {
		t.Fatalf("the old key during the overlap: %v", err)
	}
}

func TestCreatorsLinkToTheirPages(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	plain, k, err := d.s.CreateOperatorAPIKey(ctx, d.owner, core.APIKeyInput{Name: "araldo.dev staging", Scopes: []string{"posts:read", "posts:write"}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := d.s.AuthenticateKey(ctx, plain, "")
	if err != nil {
		t.Fatal(err)
	}
	byKey, err := d.s.CreatePost(ctx, key, core.PostInput{BrandID: d.brand.ID, Content: &model.Content{Body: "Posted by the key"}, PublishAt: "next_slot"})
	if err != nil {
		t.Fatal(err)
	}
	byOwner, err := d.s.CreatePost(ctx, d.owner, core.PostInput{BrandID: d.brand.ID, Content: &model.Content{Body: "Posted by a person"}, PublishAt: "next_slot"})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d\n%s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	keyHref := "/keys/" + id.Format(id.APIKey, k.ID)
	memberHref := "/org/members/" + id.Format(id.User, *d.owner.UserID)

	if page := get("/posts/" + id.Format(id.Post, byKey.ID)); !strings.Contains(page, `API key <a href="`+keyHref+`">araldo.dev staging</a>`) {
		t.Fatalf("post by a key does not link to it:\n%s", page)
	}
	if page := get("/posts/" + id.Format(id.Post, byOwner.ID)); !strings.Contains(page, `by <a href="`+memberHref+`">`) {
		t.Fatalf("post by a member does not link to them:\n%s", page)
	}
	page := get(keyHref)
	if !strings.Contains(page, "posts:write") || !strings.Contains(page, "Posted by the key") || strings.Contains(page, "Posted by a person") ||
		!strings.Contains(page, `action="`+keyHref+`/revoke"`) {
		t.Fatalf("key page:\n%s", page)
	}
	page = get(memberHref)
	if !strings.Contains(page, "Posted by a person") || strings.Contains(page, "Posted by the key") || !strings.Contains(page, ">you<") {
		t.Fatalf("member page:\n%s", page)
	}
}

func TestPostsListFiltersAndPages(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	other, err := d.s.CreateBrand(ctx, d.owner, core.BrandInput{Name: "Open B00KS " + uuid.NewString()[:6]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.s.ConnectChannel(ctx, d.owner, core.ConnectInput{BrandID: other.ID, Provider: platform.Sandbox, Fields: map[string]string{"emulates": "bluesky"}}); err != nil {
		t.Fatal(err)
	}
	for i := range 27 {
		if _, err := d.s.CreatePost(ctx, d.owner, core.PostInput{BrandID: d.brand.ID, Content: &model.Content{Body: fmt.Sprintf("Build %02d", i)}, PublishAt: "next_slot"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.s.CreatePost(ctx, d.owner, core.PostInput{BrandID: other.ID, Content: &model.Content{Body: "Open B00KS launch"}, PublishAt: "next_slot"}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d\n%s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	rows := func(page string) int { return strings.Count(page, `<td><a href="/posts/post_`) }
	href := regexp.MustCompile(`href="(/posts\?[^"]+)" rel="(prev|next)"`)
	links := func(page string) map[string]string {
		out := map[string]string{}
		for _, m := range href.FindAllStringSubmatch(page, -1) {
			out[m[2]] = html.UnescapeString(m[1])
		}
		return out
	}

	first := get("/posts")
	if rows(first) != 25 || links(first)["next"] == "" || links(first)["prev"] != "" || !strings.Contains(first, `<span class="chip-count">28</span>`) {
		t.Fatalf("first page: %d rows, links %v", rows(first), links(first))
	}
	second := get(links(first)["next"])
	if rows(second) != 3 || links(second)["prev"] == "" || links(second)["next"] != "" {
		t.Fatalf("second page: %d rows, links %v", rows(second), links(second))
	}
	back := get(links(second)["prev"])
	if rows(back) != 25 || !strings.Contains(back, "Open B00KS launch") {
		t.Fatalf("back to the first page: %d rows", rows(back))
	}

	// Filters live in the URL and survive paging.
	page := get("/posts?brand=" + other.Slug)
	if rows(page) != 1 || !strings.Contains(page, "Open B00KS launch") || !strings.Contains(page, `selected>`+other.Name) {
		t.Fatalf("brand filter: %d rows", rows(page))
	}
	page = get("/posts?q=build+0")
	if rows(page) != 10 || strings.Contains(page, "Open B00KS launch") || !strings.Contains(page, `value="build 0"`) {
		t.Fatalf("search: %d rows", rows(page))
	}
	page = get("/posts?status=scheduled&q=build")
	if rows(page) != 25 || !strings.Contains(links(page)["next"], "q=build") || !strings.Contains(links(page)["next"], "status=scheduled") {
		t.Fatalf("filtered paging: %d rows, links %v", rows(page), links(page))
	}
	if !strings.Contains(page, `class="chip no-underline hover:no-underline" aria-current="true">scheduled`) {
		t.Fatal("the chosen status chip is not marked current")
	}
	if page := get("/posts?status=failed"); rows(page) != 0 || !strings.Contains(page, "No posts match") {
		t.Fatal("an empty filter does not say so")
	}
}

// The page `araldo auth login` opens (ADR 0028): the password first, then a
// short form, then the key alone.
// TestAppsPageLinksPerPlatform checks that each platform's redirect URI
// and developer site sit in a block shown only while that platform is
// chosen, and that the select drives them, so the form shows one
// platform's links at a time (all of them without JavaScript).
func TestAppsPageLinksPerPlatform(t *testing.T) {
	t.Parallel()
	d := newDash(t, threads.New(http.DefaultClient), linkedin.New(http.DefaultClient))
	rec := d.send(httptest.NewRequest(http.MethodGet, "/channels/apps", nil))
	page := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(page, `<select name="provider" data-toggle>`) {
		t.Fatalf("apps page: %d\n%s", rec.Code, page)
	}
	for _, p := range []string{"threads", "linkedin"} {
		block := regexp.MustCompile(`(?s)<div data-when="provider=` + p + `"[^>]*>(.*?)</div>`).FindStringSubmatch(page)
		if block == nil || !strings.Contains(block[1], `id="redirect-`+p+`"`) || !strings.Contains(block[1], "Register the app at") {
			t.Errorf("no block of %s's links: %v", p, block)
		} else if !strings.Contains(block[1], `<ol class="app-guide">`) || !strings.Contains(block[1], "<strong>Client secret</strong> is") {
			t.Errorf("%s's block has no setup guide: %s", p, block[1])
		}
		if !strings.Contains(page, `<li data-when="provider=`+p+`">`) {
			t.Errorf("the guide's developer site for %s is not tied to the choice", p)
		}
	}
	if strings.Contains(page, "max-w-xl") {
		t.Error("the form is still narrower than its card")
	}
}

// TestInviteSomeone drives an invitation through the dashboard: the owner
// invites, gets the link once, and the invitee, with no account, opens it,
// creates one and lands in the org; the link then stops working.
func TestInviteSomeone(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	post := func(send func(*http.Request) *httptest.ResponseRecorder, path string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return send(r)
	}
	anon := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		d.srv.ServeHTTP(rec, r)
		return rec
	}
	invitee := fmt.Sprintf("invitee-%s@example.com", uuid.NewString()[:8])
	rec := post(d.send, "/org/invitations", url.Values{"csrf": {d.login.Session.CSRFToken}, "email": {invitee}, "role": {"editor"}})
	page := rec.Body.String()
	m := regexp.MustCompile(`id="invite-link" class="copy-box">https://araldo\.test(/invite/[^<]+)<`).FindStringSubmatch(page)
	if rec.Code != http.StatusOK || m == nil || !strings.Contains(page, "Open invitations") {
		t.Fatalf("inviting: %d\n%s", rec.Code, page)
	}
	for _, issue := range a11yIssues(page, true) {
		t.Errorf("/org with an invitation: %s", issue)
	}
	link := m[1]

	// The owner, signed in as someone else, is told whose invitation it is.
	if page = d.send(httptest.NewRequest(http.MethodGet, link, nil)).Body.String(); !strings.Contains(page, "Sign in as them") &&
		!strings.Contains(page, "sign in as "+invitee) {
		t.Fatalf("the invitation, signed in as someone else:\n%s", page)
	}

	// The invitee has no account: the page offers one.
	rec = anon(httptest.NewRequest(http.MethodGet, link, nil))
	if page = rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(page, "Create account and join") || !strings.Contains(page, invitee) {
		t.Fatalf("the invitation page: %d\n%s", rec.Code, page)
	}
	for _, issue := range a11yIssues(page, false) {
		t.Errorf("%s: %s", link, issue)
	}
	rec = post(anon, link, url.Values{"name": {"Invitee"}, "password": {"a long enough password"}, "confirm": {"not the same password"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "do not match") {
		t.Fatalf("mismatched passwords: %d", rec.Code)
	}
	rec = post(anon, link, url.Values{"name": {"Invitee"}, "password": {"a long enough password"}, "confirm": {"a long enough password"}})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.Contains(loc, "notice=Welcome") || len(rec.Result().Cookies()) == 0 {
		t.Fatalf("creating the account: %d %q", rec.Code, loc)
	}
	u, err := d.s.UserByEmail(t.Context(), invitee)
	if err != nil {
		t.Fatal(err)
	}
	if mem, err := d.s.Member(t.Context(), d.owner, u.ID); err != nil || mem.Role != model.RoleEditor {
		t.Fatalf("the invitee's membership: %+v, %v", mem, err)
	}
	if rec = anon(httptest.NewRequest(http.MethodGet, link, nil)); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not valid") {
		t.Fatalf("the used link: %d", rec.Code)
	}
}

// TestApproveADevice drives the dashboard's side of the CLI's sign-in: the
// person opens the code's page, sees what is asking, approves it, and
// later signs the device out from their account.
func TestApproveADevice(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	start, err := d.s.StartDevice(ctx, "araldo CLI on laptop", true, "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		form.Set("csrf", d.login.Session.CSRFToken)
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return d.send(r)
	}

	// Typing the code shows what asks before anything is approved.
	rec := post("/device", url.Values{"code": {strings.ToLower(start.UserCode)}})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/device?code=") {
		t.Fatalf("entering the code: %d %q", rec.Code, loc)
	}
	for _, path := range []string{"/device", "/device?code=" + start.UserCode} {
		rec = d.send(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		for _, issue := range a11yIssues(rec.Body.String(), true) {
			t.Errorf("%s: %s", path, issue)
		}
	}
	if page := rec.Body.String(); !strings.Contains(page, "araldo CLI on laptop") || !strings.Contains(page, "203.0.113.7") ||
		!strings.Contains(page, "<strong>live</strong>") {
		t.Fatalf("what asks:\n%s", page)
	}
	if rec = d.send(httptest.NewRequest(http.MethodGet, "/device?code=ZZZZ-ZZZZ", nil)); rec.Code == http.StatusOK && !strings.Contains(rec.Body.String(), "No sign-in is waiting") {
		t.Fatalf("an unknown code: %d", rec.Code)
	}

	rec = post("/device", url.Values{"code": {start.UserCode}, "action": {"approve"}})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.Contains(loc, "is+signed+in") {
		t.Fatalf("approving: %d %q", rec.Code, loc)
	}
	if _, _, err := d.s.PollDevice(ctx, start.DeviceCode); err != nil {
		t.Fatalf("the CLI's token after approval: %v", err)
	}
	page := d.send(httptest.NewRequest(http.MethodGet, "/account", nil)).Body.String()
	m := regexp.MustCompile(`action="/account/devices/(utok_[^/]+)/revoke"`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, "araldo CLI on laptop") {
		t.Fatalf("the account's devices:\n%s", page)
	}
	for _, issue := range a11yIssues(page, true) {
		t.Errorf("/account with a device: %s", issue)
	}
	if rec = post("/account/devices/"+m[1]+"/revoke", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("signing the device out: %d", rec.Code)
	}
	if page = d.send(httptest.NewRequest(http.MethodGet, "/account", nil)).Body.String(); strings.Contains(page, "araldo CLI on laptop") {
		t.Fatal("a signed-out device is still listed")
	}
}

func TestConnectTheCLI(t *testing.T) {
	t.Parallel()
	d := newDash(t)

	// Outside the password window it asks for the password before the form,
	// and comes back here with the device.
	d.s.Now = func() time.Time { return time.Now().Add(time.Hour) }
	rec := d.send(httptest.NewRequest(http.MethodGet, "/cli?device=chris-laptop", nil))
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther ||
		!strings.HasPrefix(loc, "/confirm?next="+url.QueryEscape("/cli?device=chris-laptop")+"&") {
		t.Fatalf("outside the window: %d %q", rec.Code, loc)
	}
	d.s.Now = time.Now

	rec = d.send(httptest.NewRequest(http.MethodGet, "/cli?device=chris-laptop", nil))
	page := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(page, "<h1>Connect the araldo CLI</h1>") ||
		!strings.Contains(page, `value="araldo CLI on chris-laptop"`) || !strings.Contains(page, "test mode") ||
		strings.Contains(page, "<table") {
		t.Fatalf("the form: %d\n%s", rec.Code, page)
	}
	form := url.Values{"csrf": {d.login.Session.CSRFToken}, "name": {"araldo CLI on chris-laptop"}, "device": {"chris-laptop"},
		"brand": {id.Format(id.Brand, d.brand.ID)}}
	r := httptest.NewRequest(http.MethodPost, "/cli", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = d.send(r)
	page = rec.Body.String()
	m := regexp.MustCompile(`id="new-key" class="copy-box">(ald_test_[^<]+)<`).FindStringSubmatch(page)
	if rec.Code != http.StatusOK || m == nil || !strings.Contains(page, "Paste this test key into your terminal") ||
		strings.Contains(page, "curl") || strings.Contains(page, `action="/cli"`) {
		t.Fatalf("the key: %d\n%s", rec.Code, page)
	}
	a, err := d.s.AuthenticateKey(t.Context(), m[1], "")
	if err != nil || a.BrandID == nil || *a.BrandID != d.brand.ID || a.Livemode {
		t.Fatalf("the new key: %+v, %v (want a test key limited to the brand)", a, err)
	}

	// --live asks for a live key, whichever mode the dashboard shows, and
	// keeps asking for one through the password confirmation.
	d.s.Now = func() time.Time { return time.Now().Add(time.Hour) }
	rec = d.send(httptest.NewRequest(http.MethodGet, "/cli?device=chris-laptop&mode=live", nil))
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/confirm?next="+url.QueryEscape("/cli?device=chris-laptop&mode=live")+"&") {
		t.Fatalf("live, outside the window: %d %q", rec.Code, loc)
	}
	d.s.Now = time.Now
	rec = d.send(httptest.NewRequest(http.MethodGet, "/cli?device=chris-laptop&mode=live", nil))
	if page = rec.Body.String(); !strings.Contains(page, "araldo auth login --live") || !strings.Contains(page, `name="mode" value="live"`) {
		t.Fatalf("the live form: %d\n%s", rec.Code, page)
	}
	form.Set("mode", "live")
	form.Del("brand")
	r = httptest.NewRequest(http.MethodPost, "/cli", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	page = d.send(r).Body.String()
	m = regexp.MustCompile(`id="new-key" class="copy-box">(ald_live_[^<]+)<`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, "Paste this live key") {
		t.Fatalf("the live key:\n%s", page)
	}
	if a, err = d.s.AuthenticateKey(t.Context(), m[1], ""); err != nil || !a.Livemode {
		t.Fatalf("the new live key: %+v, %v", a, err)
	}
}
