// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

func TestNewslettersInTheDashboard(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf", d.login.Session.CSRFToken)
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return d.send(r)
	}
	get := func(path string) string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	if got := get("/newsletters"); !strings.Contains(got, "No senders yet.") || !strings.Contains(got, `href="/mail-accounts/new"`) ||
		strings.Contains(got, `<li class="done">`) || !strings.Contains(got, `<a class="btn btn-primary" href="/newsletters/new`) {
		t.Fatalf("an empty page offers to connect an account and a new issue, nothing done:\n%s", got)
	}
	if got := get("/mail-accounts/new"); !strings.Contains(got, `action="/mail-accounts"`) || !strings.Contains(got, `<aside class="guide"`) {
		t.Fatalf("the connect page has the form and the guide:\n%s", got)
	}

	// Connecting leads to choosing the account's audiences.
	rec := post("/mail-accounts", url.Values{"provider": {"sandbox"}, "brand": {id.Format(id.Brand, d.brand.ID)}, "from_name": {"News"},
		"from_email": {"news@dash.example"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/mail-accounts/mailacct_") {
		t.Fatalf("connect: %d %s\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	acctPath := strings.SplitN(rec.Header().Get("Location"), "?", 2)[0]
	if got := get(acctPath); !strings.Contains(got, `value="list-1"`) || !strings.Contains(got, "1200 subscribers") {
		t.Fatalf("the account page lists the provider's audiences:\n%s", got)
	}
	if rec := post(acctPath, url.Values{"from_name": {"News"}, "from_email": {"news@dash.example"}, "audience": {"list-1", "segment-1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving the account: %d\n%s", rec.Code, rec.Body)
	}

	// The theme, on the brand page.
	if rec := post("/brands/"+id.Format(id.Brand, d.brand.ID)+"/email_theme", url.Values{"accent": {"#fde047"}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "dark enough to read on white") {
		t.Fatalf("a light accent: %d", rec.Code)
	}
	if rec := post("/brands/"+id.Format(id.Brand, d.brand.ID)+"/email_theme", url.Values{"accent": {"#1d4ed8"}, "postal_address": {"1 Main St"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving the theme: %d\n%s", rec.Code, rec.Body)
	}

	// A new issue goes to the account's defaults, previewed in a sandboxed frame.
	form := url.Values{"brand": {id.Format(id.Brand, d.brand.ID)}, "subject": {"October"}, "preview_text": {"What shipped"},
		"body": {"# October\n\n[Read more](https://dash.example){.button}"}}
	prev := post("/newsletters/preview", form)
	if prev.Code != http.StatusOK || !strings.Contains(prev.Header().Get("Content-Security-Policy"), "sandbox") ||
		prev.Header().Get("X-Frame-Options") != "SAMEORIGIN" || !strings.Contains(prev.Body.String(), "Read more</a>") {
		t.Fatalf("preview: %d %v", prev.Code, prev.Header())
	}
	if bad := post("/newsletters/preview", url.Values{"brand": {id.Format(id.Brand, d.brand.ID)}, "subject": {"x"}, "body": {"![x](media_nope)"}}); bad.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(bad.Body.String(), "Line 1") {
		t.Fatalf("a preview of a broken body: %d\n%s", bad.Code, bad.Body)
	}
	editor := get("/newsletters/new")
	if !strings.Contains(editor, `name="account" value="mailacct_`) || !strings.Contains(editor, `value="segment-1" checked`) {
		t.Fatalf("the editor offers the account and its defaults:\n%s", editor)
	}
	form.Set("account", strings.TrimPrefix(acctPath, "/mail-accounts/"))
	form["audience_"+strings.TrimPrefix(acctPath, "/mail-accounts/")] = []string{"list-2"}
	rec = post("/newsletters", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d\n%s", rec.Code, rec.Body)
	}
	issuePath := strings.SplitN(rec.Header().Get("Location"), "?", 2)[0]
	iid, err := id.Parse(id.Issue, strings.TrimPrefix(issuePath, "/newsletters/"))
	if err != nil {
		t.Fatal(err)
	}
	is, err := d.s.Issue(ctx, d.owner, iid)
	if err != nil || len(is.Deliveries) != 1 || len(is.Deliveries[0].Audiences) != 1 || is.Deliveries[0].Audiences[0].ID != "list-2" {
		t.Fatalf("created %+v, %v", is, err)
	}
	if got := get(issuePath); !strings.Contains(got, `src="`+issuePath+`/preview"`) || !strings.Contains(got, "Unsubscribe: #unsubscribe") {
		t.Fatalf("the issue page frames its preview and shows the plain text:\n%s", got)
	}
	// The preview is light, as most mail apps show it, whatever the
	// viewer's own mode; the dark one is asked for.
	if got := get(issuePath + "/preview"); !strings.Contains(got, "@media not all {") || strings.Contains(got, "prefers-color-scheme") {
		t.Fatalf("the preview is not fixed to light:\n%s", got)
	}
	if got := get(issuePath + "/preview?scheme=dark"); !strings.Contains(got, "@media all {") {
		t.Fatalf("the dark preview:\n%s", got)
	}

	action := func(values url.Values) *httptest.ResponseRecorder {
		t.Helper()
		return post(issuePath+"/action", values)
	}
	if rec := action(url.Values{"action": {"test"}, "test_account": {strings.TrimPrefix(acctPath, "/mail-accounts/")}, "test_to": {"me@dash.example, you@dash.example"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("test: %d\n%s", rec.Code, rec.Body)
	}
	if rec := action(url.Values{"action": {"schedule"}, "send_at": {"2020-01-01T09:00"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("scheduling in the past: %d", rec.Code)
	}
	at := time.Now().Add(72 * time.Hour).In(time.UTC).Format("2006-01-02T15:04")
	if rec := action(url.Values{"action": {"schedule"}, "send_at": {at}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("schedule: %d\n%s", rec.Code, rec.Body)
	}
	if is, _ = d.s.Issue(ctx, d.owner, iid); is.Status != model.IssueScheduled {
		t.Fatalf("scheduled: %s", is.Status)
	}
	if got := get(issuePath); !strings.Contains(got, ">Move</button>") || !strings.Contains(got, "Back to draft") || strings.Contains(got, `name="body"`) ||
		!strings.Contains(got, `<button class="btn-danger">Cancel this issue</button>`) {
		t.Fatalf("a scheduled issue offers moving, back to draft and canceling, not editing:\n%s", got)
	}
	if got := get("/newsletters"); strings.Count(got, `<li class="done">`) != 4 {
		t.Fatalf("every guide step but the domain is done:\n%s", got)
	}
	if rec := action(url.Values{"action": {"cancel"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("cancel: %d\n%s", rec.Code, rec.Body)
	}
	if is, _ = d.s.Issue(ctx, d.owner, iid); is.Status != model.IssueCanceled {
		t.Fatalf("canceled: %s", is.Status)
	}
	if rec := post(acctPath+"/delete", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("disconnect: %d\n%s", rec.Code, rec.Body)
	}
	if accts, err := d.s.MailAccounts(ctx, d.owner, nil); err != nil || len(accts) != 0 {
		t.Fatalf("after disconnecting: %d, %v", len(accts), err)
	}
}

// TestInsertTopPosts checks the editor's top posts button shows the editor
// again with what was typed and saves nothing, saying why when the brand
// has no posts with engagement yet.
func TestInsertTopPosts(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	form := url.Values{"csrf": {d.login.Session.CSRFToken}, "brand": {id.Format(id.Brand, d.brand.ID)}, "subject": {"Weekly"},
		"body": {"Hello there"}, "insert": {"top_posts"}, "top_days": {"7"}, "top_count": {"3"}}
	r := httptest.NewRequest(http.MethodPost, "/newsletters", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := d.send(r)
	page := rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(page, "No post of this brand has engagement") ||
		!strings.Contains(page, ">Hello there</textarea>") || !strings.Contains(page, `value="Weekly"`) {
		t.Fatalf("insert with nothing to insert: %d\n%s", rec.Code, page)
	}
	if issues, _, err := d.s.Issues(t.Context(), d.owner, core.IssueFilter{BrandID: &d.brand.ID}, store.Page{Limit: 10}); err != nil || len(issues) != 0 {
		t.Fatalf("inserting saved an issue: %d, %v", len(issues), err)
	}
	// The editor offers it, and asks for the preview to follow the writing.
	page = d.send(httptest.NewRequest(http.MethodGet, "/newsletters/new", nil)).Body.String()
	if !strings.Contains(page, `<button name="insert" value="top_posts" formnovalidate>Insert top posts</button>`) || !strings.Contains(page, "data-live-preview") {
		t.Fatalf("the editor:\n%s", page)
	}
	// Enter in a field presses the form's first button: it must save.
	if strings.Index(page, `<button class="btn-primary">Save draft</button>`) > strings.Index(page, `name="insert"`) {
		t.Fatal("the insert button comes before Save draft, so Enter would insert rather than save")
	}
}

// An issue still waiting for approval past its send time says, where it is
// reviewed, that approving sends it now; before then it does not.
func TestOverdueApprovalWarns(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	if _, err := d.s.UpdateBrand(ctx, d.owner, d.brand.ID, core.BrandInput{Name: d.brand.Name, Timezone: d.brand.Timezone,
		ApprovalPolicy: model.ApprovalAll}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.s.ConnectMailAccount(ctx, d.owner, core.MailAccountInput{BrandID: d.brand.ID, Provider: email.Sandbox, FromName: "News",
		FromEmail: "news@araldo.dev", DefaultAudiences: []string{"list-1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.s.SetEmailTheme(ctx, d.owner, d.brand.ID, core.EmailThemeInput{PostalAddress: "1 Main St"}); err != nil {
		t.Fatal(err)
	}
	is, err := d.s.CreateIssue(ctx, d.owner, core.IssueInput{BrandID: d.brand.ID, Subject: "Today's offers", Body: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	sendAt := time.Now().Add(time.Hour)
	if is, err = d.s.ScheduleIssue(ctx, d.owner, is.ID, sendAt); err != nil || is.Status != model.IssuePendingApproval {
		t.Fatalf("schedule: %v, %+v", err, is)
	}
	path := "/newsletters/" + id.Format(id.Issue, is.ID)
	page := func() string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	const warning = "Approving sends it now."
	if got := page(); strings.Contains(got, warning) || !strings.Contains(got, `value="approve"`) {
		t.Fatalf("before its send time:\n%s", got)
	}

	d.s.Now = func() time.Time { return sendAt.Add(9 * time.Hour) }
	got := page()
	if !strings.Contains(got, warning) || !strings.Contains(got, "9h ago") {
		t.Fatalf("nine hours past its send time:\n%s", got)
	}
	for _, issue := range a11yIssues(got, true) {
		t.Errorf("%s (overdue): %s", path, issue)
	}
}

// A second sender takes the connection the first one made, chosen on the
// form; the connection's page lists both, renames it, replaces its key,
// and refuses deletion until its senders are gone.
func TestMailConnectionsInTheDashboard(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf", d.login.Session.CSRFToken)
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return d.send(r)
	}
	get := func(path string) string {
		t.Helper()
		rec := d.send(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	brand := id.Format(id.Brand, d.brand.ID)

	// With no connection, the sender form connects a provider account too.
	if got := get("/mail-accounts/new"); !strings.Contains(got, `name="connection" value="new"`) || strings.Contains(got, `<select name="connection"`) {
		t.Fatalf("the first sender's form:\n%s", got)
	}
	if rec := post("/mail-accounts", url.Values{"connection": {"new"}, "provider": {"sandbox"}, "brand": {brand}, "from_name": {"News"},
		"from_email": {"news@dash.example"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("the first sender: %d\n%s", rec.Code, rec.Body)
	}
	conns, err := d.s.MailConnections(t.Context(), d.owner)
	if err != nil || len(conns) != 1 {
		t.Fatalf("connections: %v, %v", conns, err)
	}
	conn := id.Format(id.MailConnection, conns[0].ID)

	// Then it offers that connection, and the key is not asked for.
	got := get("/mail-accounts/new?connection=" + conn)
	if !strings.Contains(got, `<select name="connection"`) || !strings.Contains(got, `value="`+conn+`" selected`) || strings.Contains(got, `name="provider"`) {
		t.Fatalf("the second sender's form:\n%s", got)
	}
	if rec := post("/mail-accounts", url.Values{"connection": {conn}, "brand": {brand}, "from_name": {"Updates"},
		"from_email": {"updates@dash.example"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("the second sender: %d\n%s", rec.Code, rec.Body)
	}
	if got := get("/newsletters"); !strings.Contains(got, `href="/mail-connections/`+conn+`"`) || !strings.Contains(got, `href="/mail-connections/new"`) {
		t.Fatalf("the newsletters page lists the connection:\n%s", got)
	}

	connPath := "/mail-connections/" + conn
	got = get(connPath)
	if !strings.Contains(got, "news@dash.example") || !strings.Contains(got, "updates@dash.example") || !strings.Contains(got, "Remove its senders first") {
		t.Fatalf("the connection's page:\n%s", got)
	}
	if rec := post(connPath, url.Values{"action": {"rename"}, "name": {"Dash sandbox"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("rename: %d\n%s", rec.Code, rec.Body)
	}
	if rec := post(connPath, url.Values{"action": {"rename"}, "name": {""}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an empty name: %d", rec.Code)
	}
	if rec := post(connPath, url.Values{"action": {"replace"}, "field_simulate": {""}}); rec.Code != http.StatusSeeOther ||
		!strings.Contains(noticeOf(rec), "Every sender") {
		t.Fatalf("replace the key: %d %q\n%s", rec.Code, noticeOf(rec), rec.Body)
	}
	if rec := post(connPath+"/delete", url.Values{}); rec.Code == http.StatusSeeOther {
		t.Fatal("deleted a connection in use")
	}
	if got := get(connPath); !strings.Contains(got, "<h1>Dash sandbox</h1>") {
		t.Fatalf("after renaming:\n%s", got)
	}

	// A connection made on its own leads to adding a sender through it.
	rec := post("/mail-connections", url.Values{"provider": {"sandbox"}, "name": {"Spare"}})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/mail-accounts/new?connection=mailconn_") {
		t.Fatalf("create: %d %q\n%s", rec.Code, loc, rec.Body)
	}
	spare := strings.TrimPrefix(rec.Header().Get("Location"), "/mail-accounts/new?connection=")
	if rec := post("/mail-connections/"+spare+"/delete", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete an unused connection: %d\n%s", rec.Code, rec.Body)
	}
}
