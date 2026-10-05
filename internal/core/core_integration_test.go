// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/netguard"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/sandbox"
	"github.com/spectrum-labs-tech/araldo/internal/store"
	"github.com/spectrum-labs-tech/araldo/internal/tmpl"
)

var (
	setupOnce sync.Once
	shared    *store.Store
	setupErr  error
)

// open returns a migrated store on ARALDO_TEST_DSN. Tests never clean up:
// each creates its own org and asserts only on its own rows.
func open(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("ARALDO_TEST_DSN")
	if dsn == "" {
		t.Skip("ARALDO_TEST_DSN not set (task db:up && task test:integration)")
	}
	setupOnce.Do(func() {
		shared, setupErr = store.Open(t.Context(), dsn)
		if setupErr == nil {
			setupErr = shared.Migrate(t.Context())
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	return shared
}

const testMasterKey = "test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// option changes a test service's configuration or adds adapters.
type option func(cfg *core.Config, adapters *[]platform.Adapter)

func service(t *testing.T, opts ...option) *core.Service {
	t.Helper()
	st := open(t)
	mk, err := keyring.ParseMasterKeys(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	adapters := []platform.Adapter{sandbox.New("https://araldo.test")}
	cfg := core.Config{BaseURL: "https://araldo.test", PrivateWebhooks: netguard.Policy{All: true}}
	for _, o := range opts {
		o(&cfg, &adapters)
	}
	kr := keyring.New(mk, st)
	return core.New(st, kr, platform.NewRegistry(adapters...), slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
}

type world struct {
	s       *core.Service
	user    *model.User
	session *model.Session
	org     *model.Org
	owner   core.Actor
	brand   *model.Brand
	channel *model.Channel
}

func newWorld(t *testing.T, opts ...option) *world {
	t.Helper()
	s := service(t, opts...)
	ctx := t.Context()
	email := fmt.Sprintf("owner-%s@example.com", uuid.NewString()[:8])
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
	b, err := s.CreateBrand(ctx, owner, core.BrandInput{Name: "Araldo", Timezone: "America/Denver"})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := s.ConnectChannel(ctx, owner, core.ConnectInput{BrandID: b.ID, Provider: platform.Sandbox, Fields: map[string]string{"emulates": "bluesky"}})
	if err != nil {
		t.Fatal(err)
	}
	return &world{s: s, user: u, session: login.Session, org: org, owner: owner, brand: b, channel: ch}
}

func kind(err error) apperr.Kind {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Kind
	}
	return -1
}

func TestPostPublishesThroughTheSandbox(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	_, _, err := w.s.CreateTemplate(ctx, w.owner, core.TemplateInput{BrandID: w.brand.ID, Key: "featured-build", Name: "Featured build", Source: tmpl.Source{
		Variables: json.RawMessage(`{"type":"object","required":["name","url"],"properties":{"name":{"type":"string"},"url":{"type":"string"}}}`),
		Examples:  []json.RawMessage{json.RawMessage(`{"name":"Atlas","url":"https://araldo.dev/b/1"}`)},
		Body:      "Featured build: {{.name}} {{.url}}",
	}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Template: "featured-build",
		Data: json.RawMessage(`{"name":"Atlas","url":"https://araldo.dev/b/1"}`), PublishAt: "now", Metadata: map[string]string{"build_id": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != model.PostScheduled || len(p.Targets) != 1 || p.Targets[0].Parts[0] != "Featured build: Atlas https://araldo.dev/b/1" {
		t.Fatalf("post = %+v", p)
	}
	settle(t, w, p.ID)
	got, err := w.s.Post(ctx, w.owner, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.PostPublished || got.Targets[0].Permalink == "" {
		t.Fatalf("after publishing: status %s targets %+v", got.Status, got.Targets)
	}
	events, _, err := w.s.Events(ctx, w.owner, "", store.Page{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range events {
		types = append(types, e.Type)
	}
	for _, want := range []string{"post.created", "post_target.published", "post.published", "channel.connected", "template.version_created"} {
		if !strings.Contains(strings.Join(types, ","), want) {
			t.Errorf("events %v lack %s", types, want)
		}
	}
	// Filter by caller metadata.
	list, _, err := w.s.Posts(ctx, w.owner, core.PostFilter{Metadata: map[string]string{"build_id": "1"}}, store.Page{})
	if err != nil || len(list) != 1 {
		t.Fatalf("metadata filter: %d posts, %v", len(list), err)
	}
}

// settle runs publishing rounds until none of the post's targets is due or
// being published. Publishing claims due targets in every org, and tests run
// in parallel (and other packages' tests at the same time), so another
// worker may publish this post; that is fine, as every worker runs the same
// code. Only this post's state is waited for.
func settle(t *testing.T, w *world, postID uuid.UUID) *model.Post {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := w.s.PublishDue(t.Context(), "test-worker"); err != nil {
			t.Fatal(err)
		}
		p, err := w.s.Post(t.Context(), w.owner, postID)
		if err != nil {
			t.Fatal(err)
		}
		busy := false
		for _, tg := range p.Targets {
			due := tg.Status == model.TargetQueued && !tg.NextAttemptAt.After(w.s.Now())
			busy = busy || due || tg.Status == model.TargetPublishing
		}
		if !busy {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("post %s did not settle: %+v", postID, p.Targets)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRulesAreEnforcedPerChannel(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	long := strings.Repeat("word ", 70) // 350 graphemes: too long for Bluesky
	_, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: long}})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.KindInvalid || ae.Problems[0].Code != "too_long" {
		t.Fatalf("CreatePost(too long) = %v", err)
	}
	renders, err := w.s.PreviewPost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: long}})
	if err != nil || len(renders) != 1 || renders[0].Violations[0].Code != "too_long" || renders[0].Limit != 300 {
		t.Fatalf("PreviewPost = %+v, %v", renders, err)
	}
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID,
		Content: &model.Content{Body: long, Fit: map[platform.Provider]platform.Fit{platform.Bluesky: platform.FitThread}}})
	if err != nil || len(p.Targets[0].Parts) != 2 {
		t.Fatalf("thread fit: %v, %+v", err, p)
	}
}

func TestOtherOrgsSeeNothing(t *testing.T) {
	t.Parallel()
	a, b := newWorld(t), newWorld(t)
	ctx := t.Context()
	p, err := a.s.CreatePost(ctx, a.owner, core.PostInput{BrandID: a.brand.ID, Content: &model.Content{Body: "hello"}, PublishAt: "next_slot"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.s.CreateMedia(ctx, a.owner, core.MediaInput{BrandID: a.brand.ID, Data: pngOf(t, 4, 3)})
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := a.s.CreateOperatorAPIKey(ctx, a.owner, core.APIKeyInput{Name: "theirs"})
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]error{}
	_, checks["api key"] = b.s.APIKey(ctx, b.owner, key.ID)
	_, checks["member"] = b.s.Member(ctx, b.owner, *a.owner.UserID)
	_, checks["media"] = b.s.Media(ctx, b.owner, m.ID)
	_, _, checks["media content"] = b.s.MediaContent(ctx, b.owner, m.ID)
	_, checks["media alt"] = b.s.UpdateMediaAlt(ctx, b.owner, m.ID, "mine now")
	checks["delete media"] = b.s.DeleteMedia(ctx, b.owner, m.ID)
	_, checks["media for their brand"] = b.s.CreateMedia(ctx, b.owner, core.MediaInput{BrandID: a.brand.ID, Data: pngOf(t, 4, 3)})
	_, checks["post"] = b.s.Post(ctx, b.owner, p.ID)
	_, checks["brand"] = b.s.Brand(ctx, b.owner, a.brand.ID)
	_, checks["channel"] = b.s.Channel(ctx, b.owner, a.channel.ID)
	_, checks["cancel"] = b.s.CancelPost(ctx, b.owner, p.ID)
	_, checks["reschedule"] = b.s.ReschedulePost(ctx, b.owner, p.ID, core.RescheduleInput{PublishAt: "now"})
	theirs, err := b.s.CreatePost(ctx, b.owner, core.PostInput{BrandID: b.brand.ID, Content: &model.Content{Body: "theirs"}, PublishAt: "next_slot"})
	if err != nil {
		t.Fatal(err)
	}
	_, checks["swap"] = b.s.ReschedulePost(ctx, b.owner, theirs.ID, core.RescheduleInput{SwapWith: &p.ID})
	adAcct, err := a.s.ConnectAdAccount(ctx, a.owner, core.AdAccountInput{BrandID: a.brand.ID, Network: ads.Sandbox})
	if err != nil {
		t.Fatal(err)
	}
	_, checks["ad account"] = b.s.AdAccount(ctx, b.owner, adAcct.ID)
	checks["delete ad account"] = b.s.DeleteAdAccount(ctx, b.owner, adAcct.ID)
	anl, err := a.s.ConnectAnalyticsSource(ctx, a.owner, core.AnalyticsSourceInput{BrandID: a.brand.ID, Provider: analytics.Sandbox})
	if err != nil {
		t.Fatal(err)
	}
	_, checks["analytics source"] = b.s.AnalyticsSource(ctx, b.owner, anl.ID)
	checks["delete analytics source"] = b.s.DeleteAnalyticsSource(ctx, b.owner, anl.ID)
	_, checks["analytics for their brand"] = b.s.ConnectAnalyticsSource(ctx, b.owner, core.AnalyticsSourceInput{BrandID: a.brand.ID, Provider: analytics.Sandbox})
	_, checks["ad account for their brand"] = b.s.ConnectAdAccount(ctx, b.owner, core.AdAccountInput{BrandID: a.brand.ID, Network: ads.Sandbox})
	mailAcct, err := a.s.ConnectMailAccount(ctx, a.owner, core.MailAccountInput{BrandID: a.brand.ID, Provider: email.Sandbox,
		FromName: "A", FromEmail: "news@a.example", DefaultAudiences: []string{"list-1"}})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := a.s.CreateIssue(ctx, a.owner, core.IssueInput{BrandID: a.brand.ID, Subject: "Hi", Body: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	_, checks["mail account"] = b.s.MailAccount(ctx, b.owner, mailAcct.ID)
	_, checks["mail audiences"] = b.s.MailAudiences(ctx, b.owner, mailAcct.ID)
	_, checks["update mail account"] = b.s.UpdateMailAccount(ctx, b.owner, mailAcct.ID, core.MailAccountUpdate{FromName: "B", FromEmail: "b@b.example"})
	checks["delete mail account"] = b.s.DeleteMailAccount(ctx, b.owner, mailAcct.ID)
	_, checks["mail account for their brand"] = b.s.ConnectMailAccount(ctx, b.owner, core.MailAccountInput{BrandID: a.brand.ID,
		Provider: email.Sandbox, FromName: "B", FromEmail: "b@b.example"})
	_, checks["email theme"] = b.s.SetEmailTheme(ctx, b.owner, a.brand.ID, core.EmailThemeInput{PostalAddress: "x"})
	_, checks["issue"] = b.s.Issue(ctx, b.owner, issue.ID)
	_, checks["issue preview"] = b.s.IssuePreview(ctx, b.owner, issue.ID)
	_, checks["update issue"] = b.s.UpdateIssue(ctx, b.owner, issue.ID, core.IssueInput{Subject: "x", Body: "x"})
	_, checks["schedule issue"] = b.s.ScheduleIssue(ctx, b.owner, issue.ID, time.Now().Add(time.Hour))
	_, checks["unschedule issue"] = b.s.UnscheduleIssue(ctx, b.owner, issue.ID)
	_, checks["cancel issue"] = b.s.CancelIssue(ctx, b.owner, issue.ID)
	_, checks["review issue"] = b.s.ReviewIssue(ctx, b.owner, issue.ID, true, "")
	checks["test issue"] = b.s.SendTestIssue(ctx, b.owner, issue.ID, core.TestIssueInput{MailAccountID: mailAcct.ID, To: []string{"b@b.example"}})
	_, checks["issue for their brand"] = b.s.CreateIssue(ctx, b.owner, core.IssueInput{BrandID: a.brand.ID, Subject: "x", Body: "x"})
	_, checks["preview for their brand"] = b.s.PreviewIssue(ctx, b.owner, core.IssueInput{BrandID: a.brand.ID, Subject: "x", Body: "x"})
	_, checks["retry"] = b.s.RetryTarget(ctx, b.owner, p.Targets[0].ID)
	_, checks["post to their brand"] = b.s.CreatePost(ctx, b.owner, core.PostInput{BrandID: a.brand.ID, Content: &model.Content{Body: "x"}})
	for what, err := range checks {
		if kind(err) != apperr.KindNotFound {
			t.Errorf("%s from another org: %v, want not found", what, err)
		}
	}
	// Queue counts are per org.
	if q, err := b.s.QueueStats(ctx, b.owner); err != nil || q.Due+q.Publishing+q.NeedsAttention != 0 {
		t.Errorf("another org's queue stats: %+v, %v; want empty", q, err)
	}
	// A channel from another org cannot be named as a target either.
	_, err = b.s.CreatePost(ctx, b.owner, core.PostInput{BrandID: b.brand.ID, Content: &model.Content{Body: "x"}, Channels: []uuid.UUID{a.channel.ID}})
	if kind(err) != apperr.KindInvalid {
		t.Errorf("targeting another org's channel: %v", err)
	}
	// Nor can another org's media be attached.
	_, err = b.s.CreatePost(ctx, b.owner, core.PostInput{BrandID: b.brand.ID, Content: &model.Content{Body: "x"}, Media: []uuid.UUID{m.ID}})
	if !hasProblem(err, "media_missing") {
		t.Errorf("attaching another org's media: %v", err)
	}
	if list, _, err := b.s.MediaList(ctx, b.owner, core.MediaFilter{}, store.Page{}); err != nil || slices.ContainsFunc(list, func(x *model.Media) bool { return x.ID == m.ID }) {
		t.Errorf("another org's media listing shows ours (%v)", err)
	}
}

func TestTestModeCannotReachRealPlatforms(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	_, err := w.s.ConnectChannel(t.Context(), w.owner, core.ConnectInput{BrandID: w.brand.ID, Provider: platform.Bluesky,
		Fields: map[string]string{"identifier": "x", "app_password": "y"}})
	if kind(err) != apperr.KindInvalid || !strings.Contains(err.Error(), "Test mode") {
		t.Fatalf("connecting Bluesky in test mode: %v", err)
	}
	live := w.owner
	live.Livemode = true
	if _, err := w.s.ConnectChannel(t.Context(), live, core.ConnectInput{BrandID: w.brand.ID, Provider: platform.Sandbox,
		Fields: map[string]string{"emulates": "x"}}); kind(err) != apperr.KindInvalid {
		t.Fatalf("sandbox in live mode: %v", err)
	}
	// Live mode cannot see test-mode channels.
	if _, err := w.s.Channel(t.Context(), live, w.channel.ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("live actor reading a test channel: %v", err)
	}
}

func TestNextSlotsAreUnique(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	seen := map[time.Time]bool{}
	for range 4 {
		p, err := w.s.CreatePost(t.Context(), w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "slot"}, PublishAt: "next_slot"})
		if err != nil {
			t.Fatal(err)
		}
		if seen[*p.PublishAt] {
			t.Fatalf("slot %s given twice", p.PublishAt)
		}
		seen[*p.PublishAt] = true
		local := p.PublishAt.In(mustLoc(t, "America/Denver"))
		if h := local.Hour(); (h != 9 && h != 13) || local.Minute() != 0 || local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
			t.Fatalf("slot %s is not a weekday 9:00 or 13:00 in Denver", local)
		}
	}
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skip("no tzdata")
	}
	return loc
}

func TestApprovalFlow(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	if _, err := w.s.UpdateBrand(ctx, w.owner, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		ApprovalPolicy: model.ApprovalEditorsAndKey}); err != nil {
		t.Fatal(err)
	}
	editorUser, err := w.s.AddMember(ctx, w.owner, w.session, fmt.Sprintf("editor-%s@example.com", uuid.NewString()[:8]), model.RoleEditor, "temporary password 1")
	if err != nil {
		t.Fatal(err)
	}
	editor, _, err := w.s.MemberActor(ctx, editorUser.ID, w.org.ID, false, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.s.CreatePost(ctx, editor, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "needs review"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != model.PostPendingApproval || p.Targets[0].Status != model.TargetHeld {
		t.Fatalf("editor's post: %s / %s", p.Status, p.Targets[0].Status)
	}
	settle(t, w, p.ID)
	if got, _ := w.s.Post(ctx, w.owner, p.ID); got.Status != model.PostPendingApproval {
		t.Fatalf("held post published before approval: %s", got.Status)
	}
	if _, err := w.s.ReviewPost(ctx, editor, p.ID, true, ""); kind(err) != apperr.KindForbidden {
		t.Fatalf("editor approving: %v", err)
	}
	// Nor can a full-access key (which holds brands:write) lift the policy
	// to post unreviewed; tightening it, or changing the rest of the
	// brand, is still its.
	plain, _, err := w.s.CreateAPIKey(ctx, w.owner, w.session, core.APIKeyInput{Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := w.s.AuthenticateKey(ctx, plain, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.UpdateBrand(ctx, key, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		ApprovalPolicy: model.ApprovalNone}); kind(err) != apperr.KindForbidden {
		t.Fatalf("a key lifting the approval policy: %v", err)
	}
	if _, err := w.s.UpdateBrand(ctx, key, w.brand.ID, core.BrandInput{Name: w.brand.Name + " renamed", Timezone: w.brand.Timezone,
		ApprovalPolicy: model.ApprovalEditorsAndKey}); err != nil {
		t.Fatalf("a key renaming the brand: %v", err)
	}
	if _, err := w.s.UpdateBrand(ctx, key, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		ApprovalPolicy: model.ApprovalAll}); err != nil {
		t.Fatalf("a key asking for more review: %v", err)
	}
	if _, err := w.s.UpdateBrand(ctx, key, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		ApprovalPolicy: model.ApprovalEditorsAndKey}); kind(err) != apperr.KindForbidden {
		t.Fatalf("a key easing it again: %v", err)
	}
	if _, err := w.s.ReviewPost(ctx, w.owner, p.ID, true, "ship it"); err != nil {
		t.Fatal(err)
	}
	settle(t, w, p.ID)
	if got, _ := w.s.Post(ctx, w.owner, p.ID); got.Status != model.PostPublished || got.ReviewNote != "ship it" {
		t.Fatalf("after approval: %s", got.Status)
	}
}

func TestSimulatedFailures(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	now := time.Now()
	w.s.Now = func() time.Time { return now }
	mk := func(sim string) *model.Post {
		p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: sim},
			Metadata: map[string]string{"araldo_simulate": sim}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	uncertain := mk(sandbox.SimTimeoutAfterSend)
	settle(t, w, uncertain.ID)
	got, _ := w.s.Post(ctx, w.owner, uncertain.ID)
	if got.Targets[0].Status != model.TargetNeedsAttention {
		t.Fatalf("timeout after send: target %s, want needs_attention", got.Targets[0].Status)
	}
	if _, err := w.s.RetryTarget(ctx, w.owner, got.Targets[0].ID); err != nil {
		t.Fatal(err)
	}
	settle(t, w, uncertain.ID)
	if got, _ := w.s.Post(ctx, w.owner, uncertain.ID); got.Status != model.PostPublished {
		t.Fatalf("after retry: %s", got.Status)
	}

	rejected := mk(sandbox.SimRejected)
	settle(t, w, rejected.ID)
	if got, _ := w.s.Post(ctx, w.owner, rejected.ID); got.Status != model.PostFailed || got.Targets[0].ErrorCode != "rejected" {
		t.Fatalf("rejected: %s %q", got.Status, got.Targets[0].ErrorCode)
	}

	limited := mk(sandbox.SimRateLimited)
	settle(t, w, limited.ID)
	got, _ = w.s.Post(ctx, w.owner, limited.ID)
	if got.Targets[0].Status != model.TargetQueued || got.Targets[0].Attempts != 1 {
		t.Fatalf("rate limited: %+v", got.Targets[0])
	}
	now = now.Add(time.Minute) // past the hold
	settle(t, w, limited.ID)
	if got, _ := w.s.Post(ctx, w.owner, limited.ID); got.Status != model.PostPublished {
		t.Fatalf("after the rate limit passed: %s", got.Status)
	}

	revoked := mk(sandbox.SimAuthRevoked)
	settle(t, w, revoked.ID)
	ch, _ := w.s.Channel(ctx, w.owner, w.channel.ID)
	if ch.Status != model.ChannelNeedsReauth {
		t.Fatalf("channel after auth revoked: %s", ch.Status)
	}
	_ = revoked

	if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "x"},
		Metadata: map[string]string{"araldo_simulate": "meteor"}}); kind(err) != apperr.KindInvalid {
		t.Fatalf("unknown simulation: %v", err)
	}
}

func TestAPIKeys(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	plain, k, err := w.s.CreateAPIKey(ctx, w.owner, w.session, core.APIKeyInput{Name: "araldo.dev", Scopes: []string{"posts:read"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "ald_test_") || k.Hint == plain {
		t.Fatalf("key %q hint %q", plain, k.Hint)
	}
	a, err := w.s.AuthenticateKey(ctx, plain, "req_1")
	if err != nil || a.OrgID != w.org.ID || a.Livemode {
		t.Fatalf("AuthenticateKey = %+v, %v", a, err)
	}
	if _, err := w.s.CreatePost(ctx, a, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "x"}}); kind(err) != apperr.KindForbidden {
		t.Fatalf("read-only key posting: %v", err)
	}
	if _, _, err := w.s.Posts(ctx, a, core.PostFilter{}, store.Page{}); err != nil {
		t.Fatalf("read-only key listing: %v", err)
	}
	if err := w.s.RevokeAPIKey(ctx, w.owner, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.AuthenticateKey(ctx, plain, ""); kind(err) != apperr.KindUnauthorized {
		t.Fatalf("revoked key: %v", err)
	}
	if _, err := w.s.AuthenticateKey(ctx, authn.NewAPIKey(false), ""); kind(err) != apperr.KindUnauthorized {
		t.Fatalf("unknown key: %v", err)
	}
	// Keys need recent re-authentication.
	stale := *w.session
	stale.SudoUntil = nil
	if _, _, err := w.s.CreateAPIKey(ctx, w.owner, &stale, core.APIKeyInput{Name: "x"}); kind(err) != apperr.KindForbidden {
		t.Fatalf("creating a key without sudo: %v", err)
	}
}

// TestOwnerChangesNeedSudo checks that changing or removing an owner, and
// no longer requiring two-factor authentication, need a recent
// re-authentication, as ADR 0007 says; other member changes do not, and
// the operator acting through the CLI needs none.
func TestOwnerChangesNeedSudo(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	needsSudo := func(err error) bool { return apperr.As(err).Code == "reauthentication_required" }
	stale := *w.session
	stale.SudoUntil = nil

	other, err := w.s.AddMember(ctx, w.owner, w.session, fmt.Sprintf("owner2-%s@example.com", uuid.NewString()[:8]), model.RoleOwner, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	editor, err := w.s.AddMember(ctx, w.owner, &stale, fmt.Sprintf("editor-%s@example.com", uuid.NewString()[:8]), model.RoleEditor, "correct horse battery")
	if err != nil {
		t.Fatalf("adding an editor needs no sudo: %v", err)
	}
	if _, err := w.s.AddMember(ctx, w.owner, &stale, fmt.Sprintf("owner3-%s@example.com", uuid.NewString()[:8]), model.RoleOwner, "correct horse battery"); !needsSudo(err) {
		t.Fatalf("adding an owner without sudo: %v", err)
	}
	if err := w.s.SetMemberRole(ctx, w.owner, &stale, other.ID, model.RoleAdmin); !needsSudo(err) {
		t.Fatalf("demoting an owner without sudo: %v", err)
	}
	if err := w.s.SetMemberRole(ctx, w.owner, &stale, editor.ID, model.RoleOwner); !needsSudo(err) {
		t.Fatalf("making an owner without sudo: %v", err)
	}
	if err := w.s.RemoveMember(ctx, w.owner, &stale, other.ID); !needsSudo(err) {
		t.Fatalf("removing an owner without sudo: %v", err)
	}
	if err := w.s.SetMemberRole(ctx, w.owner, &stale, editor.ID, model.RoleViewer); err != nil {
		t.Fatalf("changing a viewer's role needs no sudo: %v", err)
	}
	operator := w.owner
	operator.Operator = true
	if err := w.s.SetMemberRole(ctx, operator, nil, other.ID, model.RoleAdmin); err != nil {
		t.Fatalf("the operator demoting an owner: %v", err)
	}
	if err := w.s.SetMemberRole(ctx, w.owner, w.session, other.ID, model.RoleOwner); err != nil {
		t.Fatalf("making an owner in sudo mode: %v", err)
	}
	if err := w.s.RemoveMember(ctx, w.owner, w.session, other.ID); err != nil {
		t.Fatalf("removing an owner in sudo mode: %v", err)
	}

	secretText, _, err := w.s.BeginTOTP(ctx, w.session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.ConfirmTOTP(ctx, w.session, authn.TOTPCode(decodeB32(t, secretText), authn.TOTPStep(time.Now()))); err != nil {
		t.Fatal(err)
	}
	if err := w.s.UpdateOrg(ctx, w.owner, &stale, w.org.Name, true); err != nil {
		t.Fatalf("requiring 2FA needs no sudo: %v", err)
	}
	if err := w.s.UpdateOrg(ctx, w.owner, &stale, w.org.Name+" renamed", true); err != nil {
		t.Fatalf("renaming needs no sudo: %v", err)
	}
	if err := w.s.UpdateOrg(ctx, w.owner, &stale, w.org.Name, false); !needsSudo(err) {
		t.Fatalf("no longer requiring 2FA without sudo: %v", err)
	}
	if err := w.s.UpdateOrg(ctx, w.owner, w.session, w.org.Name, false); err != nil {
		t.Fatalf("no longer requiring 2FA in sudo mode: %v", err)
	}
}

// TestKeysNeverOutliveTheirMaker checks that a key cannot keep itself, or
// a key it makes, alive past its own expiry (ADR 0019).
func TestKeysNeverOutliveTheirMaker(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	expires := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	plain, k, err := w.s.CreateAPIKey(ctx, w.owner, w.session, core.APIKeyInput{Name: "short-lived", Scopes: []string{"keys:write", "posts:read"},
		Expires: &expires})
	if err != nil {
		t.Fatal(err)
	}
	a, err := w.s.AuthenticateKey(ctx, plain, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		expires *time.Time
		ok      bool
	}{
		{"never expiring", nil, false},
		{"expiring later", ptr(expires.Add(time.Hour)), false},
		{"expiring first", ptr(expires.Add(-time.Hour)), true},
	} {
		_, _, err := w.s.CreateKeyWithKey(ctx, a, core.APIKeyInput{Name: tt.name, Scopes: []string{"posts:read"}, Expires: tt.expires})
		if got := err == nil; got != tt.ok {
			t.Errorf("a key creating a key %s: %v", tt.name, err)
		}
	}
	// A day later, rolling itself does not restart its 48 hours.
	w.s.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	_, rolled, err := w.s.RollOwnKey(ctx, a, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rolled.ExpiresAt == nil || !rolled.ExpiresAt.Equal(*k.ExpiresAt) {
		t.Fatalf("the rolled key expires at %v, want %v", rolled.ExpiresAt, k.ExpiresAt)
	}
	// A member rolling it in the dashboard gives it a fresh lifetime.
	_, byMember, err := w.s.RollAPIKey(ctx, w.owner, &model.Session{SudoUntil: ptr(time.Now().Add(48 * time.Hour))}, rolled.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if byMember.ExpiresAt == nil || !byMember.ExpiresAt.After(expires) {
		t.Fatalf("rolled by a member, it expires at %v (want after %v)", byMember.ExpiresAt, expires)
	}
}

// TestTheDatabaseKeepsModesApart checks that the schema itself refuses a
// target in another mode than its post and channel, so a test post can
// never reach a live channel even if code forgets to check (ADR 0006).
func TestTheDatabaseKeepsModesApart(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "test mode only"}})
	if err != nil || len(p.Targets) != 1 {
		t.Fatalf("post: %+v, %v", p, err)
	}
	_, err = open(t).Pool().Exec(ctx, `UPDATE post_targets SET livemode = true WHERE id = $1`, p.Targets[0].ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("making a test target live: %v (want a foreign key violation)", err)
	}
}

// TestAbandonedIdempotencyKey checks that a key left in progress by a
// request whose process died is taken over by a retry of the same request
// once it is surely abandoned, instead of refusing it for a day.
func TestAbandonedIdempotencyKey(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	plain, _, err := w.s.CreateAPIKey(ctx, w.owner, w.session, core.APIKeyInput{Name: "idempotent"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := w.s.AuthenticateKey(ctx, plain, "")
	if err != nil {
		t.Fatal(err)
	}
	key, req, other := uuid.NewString(), []byte("POST /v1/posts"), []byte("POST /v1/brands")
	if replay, err := w.s.BeginIdempotent(ctx, a, key, req); err != nil || replay != nil {
		t.Fatalf("first claim: %v, %v", replay, err)
	}
	// The request's process dies: the key is never finished.
	if _, err := w.s.BeginIdempotent(ctx, a, key, req); apperr.As(err).Code != "idempotency_key_in_use" {
		t.Fatalf("a retry while it may still run: %v", err)
	}
	w.s.Now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	if _, err := w.s.BeginIdempotent(ctx, a, key, other); apperr.As(err).Code != "idempotency_key_reused" {
		t.Fatalf("another request with the abandoned key: %v", err)
	}
	if replay, err := w.s.BeginIdempotent(ctx, a, key, req); err != nil || replay != nil {
		t.Fatalf("a retry once it is abandoned: %v, %v", replay, err)
	}
	if err := w.s.FinishIdempotent(ctx, a, key, http.StatusCreated, []byte(`{"id":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if replay, err := w.s.BeginIdempotent(ctx, a, key, req); err != nil || replay == nil || replay.Status != http.StatusCreated {
		t.Fatalf("after it finished: %+v, %v", replay, err)
	}
}

// TestReadyThroughADatabaseOutage checks that a server that found its
// schema current stays ready when the database goes away, so the load
// balancer keeps sending requests that get Araldo's own 503, and that one
// that never saw the database is not ready.
func TestReadyThroughADatabaseOutage(t *testing.T) {
	t.Parallel()
	open(t) // migrates the shared database
	ctx := t.Context()
	mk, err := keyring.ParseMasterKeys(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	newService := func() (*core.Service, *store.Store) {
		st, err := store.Open(ctx, os.Getenv("ARALDO_TEST_DSN"))
		if err != nil {
			t.Fatal(err)
		}
		return core.New(st, keyring.New(mk, st), platform.NewRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)), core.Config{}), st
	}
	seen, st := newService()
	if err := seen.Ready(ctx); err != nil {
		t.Fatalf("with the database: %v", err)
	}
	st.Close() // the database is gone
	if err := seen.Ready(ctx); err != nil {
		t.Fatalf("a ready server, without the database: %v", err)
	}
	fresh, st2 := newService()
	st2.Close()
	if err := fresh.Ready(ctx); err == nil {
		t.Fatal("a server that never reached the database is ready")
	}
}

// TestPruneEvents checks that events past retention go, in as many
// batches as it takes, and newer ones stay.
func TestPruneEvents(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	st := open(t)
	var old []uuid.UUID
	for i := range 3 {
		u := id.Before(time.Now().Add(-core.EventRetention - time.Duration(i+1)*time.Hour))
		copy(u[10:], uuid.New().NodeID()) // unique, and still that old
		old = append(old, u)
		if err := st.CreateEvent(ctx, &model.Event{ID: u, OrgID: w.org.ID, Type: "post.created", Data: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "recent"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.PruneEvents(ctx); err != nil {
		t.Fatal(err)
	}
	var gone, kept int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FILTER (WHERE id = ANY($2)), count(*) FILTER (WHERE NOT id = ANY($2))
		FROM events WHERE org_id = $1`, w.org.ID, old).Scan(&gone, &kept); err != nil {
		t.Fatal(err)
	}
	if gone != 0 || kept == 0 {
		t.Fatalf("after pruning: %d old events left, %d recent kept", gone, kept)
	}
}

func TestOperatorAPIKeys(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	plain, k, err := w.s.CreateOperatorAPIKey(ctx, w.owner, core.APIKeyInput{Name: "araldo.dev staging",
		Scopes: []string{"posts:write", "templates:write"}, BrandID: &w.brand.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "ald_test_") || k.BrandID == nil || *k.BrandID != w.brand.ID {
		t.Fatalf("key %q brand %v", plain, k.BrandID)
	}
	if _, err := w.s.AuthenticateKey(ctx, plain, ""); err != nil {
		t.Fatalf("AuthenticateKey: %v", err)
	}
	// Still bound by the member's role.
	viewer := w.owner
	viewer.Role = model.RoleViewer
	if _, _, err := w.s.CreateOperatorAPIKey(ctx, viewer, core.APIKeyInput{Name: "x"}); kind(err) != apperr.KindForbidden {
		t.Fatalf("viewer creating a key: %v", err)
	}
	if _, _, err := w.s.CreateOperatorAPIKey(ctx, w.owner, core.APIKeyInput{Name: "x", Scopes: []string{"org:write"}}); kind(err) != apperr.KindInvalid {
		t.Fatalf("key with a scope keys cannot have: %v", err)
	}
}

func TestLoginAndTOTP(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	if _, err := w.s.Login(ctx, w.user.Email, "wrong password!!", "", ""); !errors.Is(err, core.ErrBadCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := w.s.Login(ctx, "nobody-"+uuid.NewString()+"@example.com", "whatever password", "", ""); !errors.Is(err, core.ErrBadCredentials) {
		t.Fatalf("unknown email: %v", err)
	}
	secretText, uri, err := w.s.BeginTOTP(ctx, w.session)
	if err != nil || !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("BeginTOTP: %v", err)
	}
	secret := decodeB32(t, secretText)
	codes, err := w.s.ConfirmTOTP(ctx, w.session, authn.TOTPCode(secret, authn.TOTPStep(time.Now())))
	if err != nil || len(codes) != 10 {
		t.Fatalf("ConfirmTOTP: %d codes, %v", len(codes), err)
	}
	login, err := w.s.Login(ctx, w.user.Email, "correct horse battery", "", "")
	if err != nil || !login.NeedsMFA {
		t.Fatalf("login with MFA on: %+v %v", login, err)
	}
	// The same TOTP step cannot be used twice: use a recovery code instead.
	if err := w.s.VerifySecondFactor(ctx, login.Session, authn.TOTPCode(secret, authn.TOTPStep(time.Now()))); kind(err) != apperr.KindUnauthorized {
		t.Fatalf("replayed TOTP code: %v", err)
	}
	if err := w.s.VerifySecondFactor(ctx, login.Session, strings.ToUpper(codes[0])); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	ss, _, err := w.s.Session(ctx, login.Token)
	if err != nil || ss.State != model.SessionActive {
		t.Fatalf("session after MFA: %+v %v", ss, err)
	}
	login2, _ := w.s.Login(ctx, w.user.Email, "correct horse battery", "", "")
	if err := w.s.VerifySecondFactor(ctx, login2.Session, codes[0]); kind(err) != apperr.KindUnauthorized {
		t.Fatalf("reused recovery code: %v", err)
	}
}

// TestWrongCodesLockTheAccount checks that wrong second-factor codes and
// wrong passwords given to confirm sudo mode count toward the lockout, so
// neither can be guessed by signing in again and again (ADR 0007).
func TestWrongCodesLockTheAccount(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	isLocked := func(err error) bool {
		var ae *apperr.Error
		return errors.As(err, &ae) && ae.Code == "account_locked"
	}

	w := newWorld(t)
	secretText, _, err := w.s.BeginTOTP(ctx, w.session)
	if err != nil {
		t.Fatal(err)
	}
	secret := decodeB32(t, secretText)
	if _, err := w.s.ConfirmTOTP(ctx, w.session, authn.TOTPCode(secret, authn.TOTPStep(time.Now()))); err != nil {
		t.Fatal(err)
	}
	// Each sign-in gives the right password; only the code is wrong.
	for i := range 10 {
		login, err := w.s.Login(ctx, w.user.Email, "correct horse battery", "", "")
		if err != nil {
			t.Fatalf("sign-in %d: %v", i+1, err)
		}
		if err := w.s.VerifySecondFactor(ctx, login.Session, "nope"); kind(err) != apperr.KindUnauthorized {
			t.Fatalf("wrong code %d: %v", i+1, err)
		}
	}
	if _, err := w.s.Login(ctx, w.user.Email, "correct horse battery", "", ""); !isLocked(err) {
		t.Fatalf("sign-in after 10 wrong codes: %v (want account_locked)", err)
	}

	w2 := newWorld(t)
	for i := range 10 {
		if err := w2.s.Reauthenticate(ctx, w2.session, "wrong password!!", ""); kind(err) != apperr.KindUnauthorized {
			t.Fatalf("wrong password %d: %v", i+1, err)
		}
	}
	if err := w2.s.Reauthenticate(ctx, w2.session, "correct horse battery", ""); !isLocked(err) {
		t.Fatalf("confirming after 10 wrong passwords: %v (want account_locked)", err)
	}
	if _, err := w2.s.Login(ctx, w2.user.Email, "correct horse battery", "", ""); !isLocked(err) {
		t.Fatalf("sign-in after 10 wrong passwords to confirm: %v (want account_locked)", err)
	}
}

func decodeB32(t *testing.T, s string) []byte {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	var out []byte
	var acc, bits uint
	for _, c := range s {
		acc = acc<<5 | uint(strings.IndexRune(alphabet, c))
		bits += 5
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>bits))
		}
	}
	return out
}

func TestWebhookDelivery(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	var mu sync.Mutex
	var got []string
	var secret string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if err := core.VerifySignature(secret, r.Header.Get("Araldo-Signature"), body, time.Now(), 5*time.Minute); err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		var e struct{ Type string }
		_ = json.Unmarshal(body, &e)
		got = append(got, e.Type)
	}))
	defer srv.Close()
	ep, sec, err := w.s.CreateEndpoint(ctx, w.owner, core.EndpointInput{URL: srv.URL, EventTypes: []string{"post_target.published"}})
	if err != nil {
		t.Fatal(err)
	}
	secret = sec
	hooked, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "hook me"}})
	if err != nil {
		t.Fatal(err)
	}
	settle(t, w, hooked.ID)
	// Delivery claims due webhooks in every org, so another worker may send
	// this one; wait for this endpoint's delivery to finish.
	var ds []model.Delivery
	for deadline := time.Now().Add(15 * time.Second); ; {
		if _, err := w.s.DeliverDue(ctx, "test-worker"); err != nil {
			t.Fatal(err)
		}
		if ds, _, err = w.s.Deliveries(ctx, w.owner, ep.ID, store.Page{}); err != nil {
			t.Fatal(err)
		}
		if len(ds) == 1 && (ds[0].Status == "succeeded" || ds[0].Status == "failed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery did not finish: %+v", ds)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ds[0].Status != "succeeded" {
		t.Fatalf("delivery %+v", ds[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "post_target.published" {
		t.Fatalf("endpoint received %v", got)
	}
}

// TestRollingAWebhookSecretOverlaps checks that after a roll, deliveries
// are signed with the old secret as well as the new one until the overlap
// ends, so a receiver can switch without rejecting any (ADR 0012).
func TestRollingAWebhookSecretOverlaps(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	var mu sync.Mutex
	var headers []string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		headers = append(headers, r.Header.Get("Araldo-Signature")+"\n"+string(body))
		mu.Unlock()
	}))
	defer srv.Close()
	ep, first, err := w.s.CreateEndpoint(ctx, w.owner, core.EndpointInput{URL: srv.URL, EventTypes: []string{"post.created"}})
	if err != nil {
		t.Fatal(err)
	}
	// deliver creates a post and returns the signature its event arrived with.
	deliver := func() (header string, body []byte) {
		t.Helper()
		mu.Lock()
		before := len(headers)
		mu.Unlock()
		if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "signed"}}); err != nil {
			t.Fatal(err)
		}
		// Other tests' workers may deliver it; wait for it to arrive.
		for deadline := time.Now().Add(15 * time.Second); ; {
			if _, err := w.s.DeliverDue(ctx, "test-worker"); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			if len(headers) > before {
				h, b, _ := strings.Cut(headers[before], "\n")
				mu.Unlock()
				return h, []byte(b)
			}
			mu.Unlock()
			if time.Now().After(deadline) {
				t.Fatal("the delivery did not arrive")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	verifies := func(secret, header string, body []byte) bool {
		return core.VerifySignature(secret, header, body, time.Now(), 5*time.Minute) == nil
	}

	second, err := w.s.RollEndpointSecret(ctx, w.owner, ep.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h, body := deliver()
	if !verifies(second, h, body) || !verifies(first, h, body) {
		t.Fatalf("during the overlap: new %t, old %t (want both)", verifies(second, h, body), verifies(first, h, body))
	}
	third, err := w.s.RollEndpointSecret(ctx, w.owner, ep.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, body = deliver()
	if !verifies(third, h, body) || verifies(second, h, body) || verifies(first, h, body) {
		t.Fatalf("rolled with no overlap: header %q", h)
	}
}

// TestFailingEndpointIsDisabled checks that an endpoint every delivery to
// which failed for 3 days is disabled, with a webhook_endpoint.disabled
// event in the same change.
func TestFailingEndpointIsDisabled(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	ep, _, err := w.s.CreateEndpoint(ctx, w.owner, core.EndpointInput{URL: "https://receiver.example/hooks", EventTypes: []string{"post.created"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := open(t).Pool().Exec(ctx, `UPDATE webhook_endpoints SET failing_since = now() - interval '4 days' WHERE id = $1`, ep.ID); err != nil {
		t.Fatal(err)
	}
	// Other tests' workers may run the task too; either way it is done.
	if _, err := w.s.DisableFailingEndpoints(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := w.s.Endpoint(ctx, w.owner, ep.ID)
	if err != nil || got.Status != "disabled" || got.DisabledReason == "" {
		t.Fatalf("the endpoint: %+v, %v", got, err)
	}
	evs, _, err := w.s.Events(ctx, w.owner, "webhook_endpoint.disabled", store.Page{})
	if err != nil || len(evs) != 1 || !strings.Contains(string(evs[0].Data), id.Format(id.WebhookEndpoint, ep.ID)) {
		t.Fatalf("the event: %v, %v", evs, err)
	}
}

func TestTemplateApprovalOverride(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	if _, err := w.s.UpdateBrand(ctx, w.owner, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		ApprovalPolicy: model.ApprovalAll}); err != nil {
		t.Fatal(err)
	}
	src := tmpl.Source{Body: "Featured brand: {{.name}}"}
	fast, _, err := w.s.CreateTemplate(ctx, w.owner, core.TemplateInput{BrandID: w.brand.ID, Key: "featured-brand",
		Approval: model.TemplateApprovalNotRequired, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	post := func(a core.Actor, template string) *model.Post {
		t.Helper()
		in := core.PostInput{BrandID: w.brand.ID, Data: json.RawMessage(`{"name":"Aero"}`)}
		if template != "" {
			in.Template = template
		} else {
			in.Content = &model.Content{Body: "freeform"}
		}
		p, err := w.s.CreatePost(ctx, a, in)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := post(w.owner, "featured-brand"); p.Status != model.PostScheduled {
		t.Errorf("not_required template under a required_for_all brand: %s, want scheduled", p.Status)
	}
	if p := post(w.owner, ""); p.Status != model.PostPendingApproval {
		t.Errorf("freeform post under required_for_all: %s, want pending_approval", p.Status)
	}
	// And the other way: a brand without approvals, a template that requires them.
	if _, err := w.s.UpdateBrand(ctx, w.owner, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		ApprovalPolicy: model.ApprovalNone}); err != nil {
		t.Fatal(err)
	}
	required := model.TemplateApprovalRequired
	if _, _, err := w.s.UpdateTemplate(ctx, w.owner, fast.ID, core.TemplateSettings{Approval: &required}); err != nil {
		t.Fatal(err)
	}
	if p := post(w.owner, "featured-brand"); p.Status != model.PostPendingApproval {
		t.Errorf("required template under an approval-free brand: %s, want pending_approval", p.Status)
	}
	// Editors and API keys cannot exempt posts from review.
	editorUser, err := w.s.AddMember(ctx, w.owner, w.session, fmt.Sprintf("ed-%s@example.com", uuid.NewString()[:8]), model.RoleEditor, "temporary password 1")
	if err != nil {
		t.Fatal(err)
	}
	editor, _, _ := w.s.MemberActor(ctx, editorUser.ID, w.org.ID, false, "")
	notRequired := model.TemplateApprovalNotRequired
	if _, _, err := w.s.UpdateTemplate(ctx, editor, fast.ID, core.TemplateSettings{Approval: &notRequired}); kind(err) != apperr.KindForbidden {
		t.Errorf("editor exempting a template: %v, want forbidden", err)
	}
	if _, _, err := w.s.CreateTemplate(ctx, editor, core.TemplateInput{BrandID: w.brand.ID, Key: "sneaky",
		Approval: model.TemplateApprovalNotRequired, Source: src}); kind(err) != apperr.KindForbidden {
		t.Errorf("editor creating an exempt template: %v, want forbidden", err)
	}
	// Renaming needs no special role.
	name := "Featured brand"
	if tpl, _, err := w.s.UpdateTemplate(ctx, editor, fast.ID, core.TemplateSettings{Name: &name}); err != nil || tpl.Name != name {
		t.Errorf("editor renaming: %v", err)
	}
}

func TestUTMTagging(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	b, err := w.s.UpdateBrand(ctx, w.owner, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		UTMDomains: []string{"https://www.ARALDO.dev/", "araldo.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.UTMDomains, []string{"araldo.dev"}) {
		t.Fatalf("stored domains %q", b.UTMDomains)
	}
	if _, _, err := w.s.CreateTemplate(ctx, w.owner, core.TemplateInput{BrandID: w.brand.ID, Key: "brand-spotlight", Name: "Spotlight",
		Source: tmpl.Source{Body: "{{.name}} {{.url}} via https://example.com/x"}}); err != nil {
		t.Fatal(err)
	}
	in := core.PostInput{BrandID: w.brand.ID, Template: "brand-spotlight", Data: json.RawMessage(`{"name":"Aero","url":"https://araldo.dev/brands/7?ref=a"}`)}
	tagged := func(postID string) string {
		return "Aero https://araldo.dev/brands/7?ref=a&utm_campaign=brand-spotlight&utm_content=" + postID +
			"&utm_medium=social&utm_source=sandbox via https://example.com/x"
	}

	// A preview uses a placeholder ID of the same length.
	rs, err := w.s.PreviewPost(ctx, w.owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if want := tagged(id.Format(id.Post, uuid.Nil)); len(rs) != 1 || rs[0].Parts[0] != want {
		t.Fatalf("preview parts %q, want %q", rs[0].Parts, want)
	}

	p, err := w.s.CreatePost(ctx, w.owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if want := tagged(id.Format(id.Post, p.ID)); p.Targets[0].Parts[0] != want {
		t.Fatalf("post parts %q, want %q", p.Targets[0].Parts, want)
	}

	// Clearing the list stops tagging; a bad entry is refused.
	if _, err := w.s.UpdateBrand(ctx, w.owner, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		UTMDomains: []string{"not a domain"}}); kind(err) != apperr.KindInvalid {
		t.Fatalf("bad domain: %v", err)
	}
	if _, err := w.s.UpdateBrand(ctx, w.owner, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone}); err != nil {
		t.Fatal(err)
	}
	p2, err := w.s.CreatePost(ctx, w.owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Aero https://araldo.dev/brands/7?ref=a via https://example.com/x"; p2.Targets[0].Parts[0] != want {
		t.Fatalf("untagged parts %q", p2.Targets[0].Parts)
	}
}

func TestPreviewTemplateNeedsData(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	src := tmpl.Source{Body: "{{.name}}", Variables: json.RawMessage(`{"type":"object","required":["name"]}`)}

	// No data and no example: one clear problem, not a list of missing fields.
	_, err := w.s.PreviewTemplate(ctx, w.owner, src, nil, []platform.Provider{platform.Bluesky}, "UTC")
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != "example_required" {
		t.Fatalf("no data: %v", err)
	}
	// Explicit data that is wrong still names the fields.
	_, err = w.s.PreviewTemplate(ctx, w.owner, src, json.RawMessage(`{"other":1}`), []platform.Provider{platform.Bluesky}, "UTC")
	if !errors.As(err, &ae) || ae.Code != "data_invalid" {
		t.Fatalf("bad data: %v", err)
	}
	// An example is used when no data is given.
	src.Examples = []json.RawMessage{json.RawMessage(`{"name":"Aero"}`)}
	rs, err := w.s.PreviewTemplate(ctx, w.owner, src, nil, []platform.Provider{platform.Bluesky}, "UTC")
	if err != nil || len(rs) != 1 || rs[0].Parts[0] != "Aero" {
		t.Fatalf("with example: %+v, %v", rs, err)
	}
}

// TestTemplatesCannotLiftApproval checks a template exempt from approval
// exempts only its own brand's posts, and only with text an approver saw
// (ADR 0004).
func TestTemplatesCannotLiftApproval(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	strict, err := w.s.CreateBrand(ctx, w.owner, core.BrandInput{Name: "Strict", Timezone: "UTC", ApprovalPolicy: model.ApprovalAll})
	if err != nil {
		t.Fatal(err)
	}
	exempt, _, err := w.s.CreateTemplate(ctx, w.owner, core.TemplateInput{BrandID: w.brand.ID, Key: "release", Name: "Release",
		Approval: model.TemplateApprovalNotRequired, Source: tmpl.Source{Body: "Shipped"}})
	if err != nil {
		t.Fatal(err)
	}
	editorUser, err := w.s.AddMember(ctx, w.owner, w.session, fmt.Sprintf("editor-%s@example.com", uuid.NewString()[:8]), model.RoleEditor, "temporary password 1")
	if err != nil {
		t.Fatal(err)
	}
	editor, _, err := w.s.MemberActor(ctx, editorUser.ID, w.org.ID, false, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.s.CreatePost(ctx, editor, core.PostInput{BrandID: strict.ID, Template: id.Format(id.Template, exempt.ID)}); kind(err) != apperr.KindInvalid {
		t.Fatalf("another brand's exempt template, by ID: %v", err)
	}
	if p, err := w.s.CreatePost(ctx, editor, core.PostInput{BrandID: w.brand.ID, Template: id.Format(id.Template, exempt.ID)}); err != nil || p.ApprovalNeeded {
		t.Fatalf("its own brand's template, by ID: %+v, %v", p, err)
	}
	if _, _, err := w.s.AddTemplateVersion(ctx, editor, exempt.ID, "", tmpl.Source{Body: "Anything at all"}); kind(err) != apperr.KindForbidden {
		t.Fatalf("an editor rewriting an exempt template: %v", err)
	}
	if _, _, err := w.s.AddTemplateVersion(ctx, w.owner, exempt.ID, "", tmpl.Source{Body: "Shipped, reviewed"}); err != nil {
		t.Fatalf("an owner rewriting it: %v", err)
	}
}

// TestPublisherSlotsAreIndependent checks a publish that takes long holds
// one of the publisher's slots, not the others: a post due later, on
// another channel, goes out while it is still running.
func TestPublisherSlotsAreIndependent(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	defer close(release)
	w := newWorld(t, func(_ *core.Config, adapters *[]platform.Adapter) {
		sb := sandbox.New("https://araldo.test")
		sb.Sleep = func(ctx context.Context, _ time.Duration) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		(*adapters)[0] = sb
	})
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	other, err := w.s.ConnectChannel(ctx, w.owner, core.ConnectInput{BrandID: w.brand.ID, Provider: platform.Sandbox,
		Fields: map[string]string{"emulates": "mastodon"}})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = w.s.RunPublisher(ctx, "slots-"+uuid.NewString()[:8]) }()

	status := func(postID uuid.UUID) model.TargetStatus {
		t.Helper()
		p, err := w.s.Post(ctx, w.owner, postID)
		if err != nil {
			t.Fatal(err)
		}
		return p.Targets[0].Status
	}
	wait := func(postID uuid.UUID, want model.TargetStatus) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); status(postID) != want; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("post %s stayed %s, want %s", postID, status(postID), want)
			}
		}
	}
	slow, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Channels: []uuid.UUID{w.channel.ID},
		Content: &model.Content{Body: "slow"}, PublishAt: "now", Metadata: map[string]string{"araldo_simulate": sandbox.SimSlow}})
	if err != nil {
		t.Fatal(err)
	}
	wait(slow.ID, model.TargetPublishing)
	quick, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Channels: []uuid.UUID{other.ID},
		Content: &model.Content{Body: "quick"}, PublishAt: "now"})
	if err != nil {
		t.Fatal(err)
	}
	wait(quick.ID, model.TargetPublished)
}

// TestLostLeasesBeforeTheCall checks a target whose worker vanished before
// putting its attempt on record goes back in the queue (the platform was
// never called), while one that vanished after needs a person (ADR 0011).
func TestLostLeasesBeforeTheCall(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	pool := open(t).Pool()
	lose := func(started bool) uuid.UUID {
		t.Helper()
		p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "lost"},
			PublishAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		if err != nil {
			t.Fatal(err)
		}
		tg := p.Targets[0]
		// As a claim, then a worker gone with its lease run out.
		if _, err := pool.Exec(ctx, `UPDATE post_targets SET status = 'publishing', lease_owner = 'gone', lease_until = now() - interval '1 minute',
			attempts = attempts + 1 WHERE id = $1`, tg.ID); err != nil {
			t.Fatal(err)
		}
		if started {
			if _, err := pool.Exec(ctx, `INSERT INTO publish_attempts (id, org_id, target_id, attempt, started_at) VALUES ($1, $2, $3, $4, now())`,
				uuid.New(), w.org.ID, tg.ID, tg.Attempts+1); err != nil {
				t.Fatal(err)
			}
		}
		return p.ID
	}
	before, after := lose(false), lose(true)
	if _, err := w.s.ReclaimLostTargets(ctx); err != nil {
		t.Fatal(err)
	}
	for postID, want := range map[uuid.UUID]model.TargetStatus{before: model.TargetQueued, after: model.TargetNeedsAttention} {
		p, err := w.s.Post(ctx, w.owner, postID)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Targets[0].Status; got != want {
			t.Errorf("post %s: %s, want %s (%s)", postID, got, want, p.Targets[0].ErrorMessage)
		}
	}
}

// TestPastDeadlineIsNotPublished checks a target past its publish_by is
// not claimed, even before the expire task fails it (ADR 0011).
func TestPastDeadlineIsNotPublished(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "too late"},
		PublishAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	// Due now, but its deadline has passed (the channel was held, say).
	if _, err := open(t).Pool().Exec(ctx, `UPDATE post_targets SET next_attempt_at = now() - interval '1 minute',
		publish_by = now() - interval '1 second' WHERE id = $1`, p.Targets[0].ID); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := w.s.PublishDue(ctx, "test-worker"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := w.s.Post(ctx, w.owner, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st := got.Targets[0].Status; st == model.TargetPublished || st == model.TargetPublishing {
		t.Fatalf("a target past its deadline was published: %s", st)
	}
}

// TestSlowEndpointHoldsOnlyItsOwnDeliveries checks an endpoint that
// answers slowly gets at most a couple of deliveries at once, and another
// org's webhooks go out meanwhile.
func TestSlowEndpointHoldsOnlyItsOwnDeliveries(t *testing.T) {
	t.Parallel()
	slowOrg, quickOrg := newWorld(t), newWorld(t)
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	var now, most atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		n := now.Add(1)
		defer now.Add(-1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(700 * time.Millisecond)
	}))
	defer slow.Close()
	quick := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer quick.Close()
	if _, _, err := slowOrg.s.CreateEndpoint(ctx, slowOrg.owner, core.EndpointInput{URL: slow.URL, EventTypes: []string{"post.created"}}); err != nil {
		t.Fatal(err)
	}
	quickEP, _, err := quickOrg.s.CreateEndpoint(ctx, quickOrg.owner, core.EndpointInput{URL: quick.URL, EventTypes: []string{"post.created"}})
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for range 6 {
		if _, err := slowOrg.s.CreatePost(ctx, slowOrg.owner, core.PostInput{BrandID: slowOrg.brand.ID, Content: &model.Content{Body: "slow"}, PublishAt: later}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := quickOrg.s.CreatePost(ctx, quickOrg.owner, core.PostInput{BrandID: quickOrg.brand.ID, Content: &model.Content{Body: "quick"}, PublishAt: later}); err != nil {
		t.Fatal(err)
	}
	go func() { _ = slowOrg.s.RunDeliverer(ctx, "fair-"+uuid.NewString()[:8]) }()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		ds, _, err := quickOrg.s.Deliveries(ctx, quickOrg.owner, quickEP.ID, store.Page{})
		if err != nil {
			t.Fatal(err)
		}
		if len(ds) == 1 && ds[0].Status == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the other org's delivery: %+v", ds)
		}
	}
	if m := most.Load(); m > store.PerEndpoint {
		t.Fatalf("%d deliveries at once to one endpoint, want at most %d", m, store.PerEndpoint)
	}
}

// userToken signs the CLI in for w's owner, as the device flow does.
func userToken(t *testing.T, w *world, ss *model.Session) string {
	t.Helper()
	ctx := t.Context()
	start, err := w.s.StartDevice(ctx, "araldo CLI", false, "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.DecideDevice(ctx, ss, start.UserCode, true); err != nil {
		t.Fatal(err)
	}
	plain, _, err := w.s.PollDevice(ctx, start.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

// TestAccountSecurity checks changing a password signs out everywhere
// else, recovery codes are replaced only with a recent password, and
// two-factor authentication stays on where an org requires it (ADR 0007).
func TestAccountSecurity(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	const pw = "correct horse battery"
	login := func(password string) *core.LoginResult {
		t.Helper()
		l, err := w.s.Login(ctx, w.user.Email, password, "test", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	here, elsewhere := login(pw), login(pw)
	cli := userToken(t, w, here.Session)

	if err := w.s.ChangePassword(ctx, here.Session, "not my password", "a brand new password"); code(err) != "bad_password" {
		t.Fatalf("the wrong current password: %v", err)
	}
	if err := w.s.ChangePassword(ctx, here.Session, pw, "short"); code(err) != "password_invalid" {
		t.Fatalf("a short new password: %v", err)
	}
	const next = "a brand new password"
	if err := w.s.ChangePassword(ctx, here.Session, pw, next); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.Session(ctx, here.Token); err != nil {
		t.Fatalf("the session that changed it: %v", err)
	}
	if _, _, err := w.s.Session(ctx, elsewhere.Token); err == nil {
		t.Fatal("another session survived the change")
	}
	if _, err := w.s.AuthenticateUserToken(ctx, cli, "", ""); err == nil {
		t.Fatal("the CLI's token survived the change")
	}
	if _, err := w.s.Login(ctx, w.user.Email, pw, "", ""); !errors.Is(err, core.ErrBadCredentials) {
		t.Fatalf("the old password: %v", err)
	}
	fresh := login(next)

	// Recovery codes: replaced only after confirming the password; the old
	// ones stop working.
	secretText, _, err := w.s.BeginTOTP(ctx, fresh.Session)
	if err != nil {
		t.Fatal(err)
	}
	secret := decodeB32(t, secretText)
	old, err := w.s.ConfirmTOTP(ctx, fresh.Session, authn.TOTPCode(secret, authn.TOTPStep(time.Now())))
	if err != nil {
		t.Fatal(err)
	}
	stale := *fresh.Session
	stale.SudoUntil = nil
	if _, err := w.s.RegenerateRecoveryCodes(ctx, &stale); code(err) != "reauthentication_required" {
		t.Fatalf("new codes without a recent password: %v", err)
	}
	codes, err := w.s.RegenerateRecoveryCodes(ctx, fresh.Session)
	if err != nil || len(codes) != 10 {
		t.Fatalf("new codes: %d, %v", len(codes), err)
	}
	signIn := login(next)
	if err := w.s.VerifySecondFactor(ctx, signIn.Session, old[0]); kind(err) != apperr.KindUnauthorized {
		t.Fatalf("an old recovery code: %v", err)
	}
	if err := w.s.VerifySecondFactor(ctx, signIn.Session, codes[0]); err != nil {
		t.Fatalf("a new recovery code: %v", err)
	}

	// Two-factor authentication stays on while an org requires it.
	owner := memberActor(t, w, false)
	if err := w.s.UpdateOrg(ctx, owner, fresh.Session, w.org.Name, true); err != nil {
		t.Fatal(err)
	}
	if err := w.s.DisableTOTP(ctx, fresh.Session); kind(err) != apperr.KindForbidden {
		t.Fatalf("turning it off where it is required: %v", err)
	}
	if err := w.s.UpdateOrg(ctx, owner, fresh.Session, w.org.Name, false); err != nil {
		t.Fatal(err)
	}
	if err := w.s.DisableTOTP(ctx, &stale); code(err) != "reauthentication_required" {
		t.Fatalf("turning it off without a recent password: %v", err)
	}
	if err := w.s.DisableTOTP(ctx, fresh.Session); err != nil {
		t.Fatal(err)
	}
	if l := login(next); l.NeedsMFA {
		t.Fatal("still asked for a code with two-factor authentication off")
	}
}
