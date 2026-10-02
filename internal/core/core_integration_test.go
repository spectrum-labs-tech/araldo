// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
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
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
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

func service(t *testing.T, opts ...func(*core.Config)) *core.Service {
	t.Helper()
	st := open(t)
	mk, err := keyring.ParseMasterKeys(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	reg := platform.NewRegistry(sandbox.New("https://araldo.test"))
	cfg := core.Config{BaseURL: "https://araldo.test", AllowPrivateWebhooks: true}
	for _, o := range opts {
		o(&cfg)
	}
	return core.New(st, keyring.New(mk, st), reg, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
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

func newWorld(t *testing.T, opts ...func(*core.Config)) *world {
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
	b, err := s.CreateBrand(ctx, owner, core.BrandInput{Name: "AR15.build", Timezone: "America/Denver"})
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
		Examples:  []json.RawMessage{json.RawMessage(`{"name":"Recce","url":"https://ar15.build/b/1"}`)},
		Body:      "Featured build: {{.name}} {{.url}}",
	}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Template: "featured-build",
		Data: json.RawMessage(`{"name":"Recce","url":"https://ar15.build/b/1"}`), PublishAt: "now", Metadata: map[string]string{"build_id": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != model.PostScheduled || len(p.Targets) != 1 || p.Targets[0].Parts[0] != "Featured build: Recce https://ar15.build/b/1" {
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
	checks := map[string]error{}
	_, checks["media"] = b.s.Media(ctx, b.owner, m.ID)
	_, _, checks["media content"] = b.s.MediaContent(ctx, b.owner, m.ID)
	_, checks["media alt"] = b.s.UpdateMediaAlt(ctx, b.owner, m.ID, "mine now")
	checks["delete media"] = b.s.DeleteMedia(ctx, b.owner, m.ID)
	_, checks["media for their brand"] = b.s.CreateMedia(ctx, b.owner, core.MediaInput{BrandID: a.brand.ID, Data: pngOf(t, 4, 3)})
	_, checks["post"] = b.s.Post(ctx, b.owner, p.ID)
	_, checks["brand"] = b.s.Brand(ctx, b.owner, a.brand.ID)
	_, checks["channel"] = b.s.Channel(ctx, b.owner, a.channel.ID)
	_, checks["cancel"] = b.s.CancelPost(ctx, b.owner, p.ID)
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
		if seen[p.PublishAt] {
			t.Fatalf("slot %s given twice", p.PublishAt)
		}
		seen[p.PublishAt] = true
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
	editorUser, err := w.s.AddMember(ctx, w.owner, fmt.Sprintf("editor-%s@example.com", uuid.NewString()[:8]), model.RoleEditor, "temporary password 1")
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
	plain, k, err := w.s.CreateAPIKey(ctx, w.owner, w.session, core.APIKeyInput{Name: "ar15.build", Scopes: []string{"posts:read"}})
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

func TestOperatorAPIKeys(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	plain, k, err := w.s.CreateOperatorAPIKey(ctx, w.owner, core.APIKeyInput{Name: "ar15.build staging",
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
	editorUser, err := w.s.AddMember(ctx, w.owner, fmt.Sprintf("ed-%s@example.com", uuid.NewString()[:8]), model.RoleEditor, "temporary password 1")
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
		UTMDomains: []string{"https://www.AR15.build/", "ar15.build"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.UTMDomains, []string{"ar15.build"}) {
		t.Fatalf("stored domains %q", b.UTMDomains)
	}
	if _, _, err := w.s.CreateTemplate(ctx, w.owner, core.TemplateInput{BrandID: w.brand.ID, Key: "brand-spotlight", Name: "Spotlight",
		Source: tmpl.Source{Body: "{{.name}} {{.url}} via https://example.com/x"}}); err != nil {
		t.Fatal(err)
	}
	in := core.PostInput{BrandID: w.brand.ID, Template: "brand-spotlight", Data: json.RawMessage(`{"name":"Aero","url":"https://ar15.build/brands/7?ref=a"}`)}
	tagged := func(postID string) string {
		return "Aero https://ar15.build/brands/7?ref=a&utm_campaign=brand-spotlight&utm_content=" + postID +
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
	if want := "Aero https://ar15.build/brands/7?ref=a via https://example.com/x"; p2.Targets[0].Parts[0] != want {
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
