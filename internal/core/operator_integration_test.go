// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// code is an error's problem code, or "" for none.
func code(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// operatorKey makes an operator key and returns the operator it
// authenticates as.
func operatorKey(t *testing.T, s *core.Service) (string, core.Actor) {
	t.Helper()
	plain, _, err := s.CreateOperatorKey(t.Context(), core.InstallOperator("araldo admin operator-keys create"), "billing "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.AuthenticateOperatorKey(t.Context(), plain, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	return plain, op
}

// memberActor is the world's owner as a new request would see them, with
// the org's status now.
func memberActor(t *testing.T, w *world, live bool) core.Actor {
	t.Helper()
	a, _, err := w.s.MemberActor(t.Context(), w.user.ID, w.org.ID, live, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestOperatorKeys checks only the operator makes keys, which authenticate
// until revoked (ADR 0031).
func TestOperatorKeys(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	if _, _, err := w.s.CreateOperatorKey(ctx, w.owner, "mine"); kind(err) != apperr.KindForbidden {
		t.Fatalf("an org owner making an operator key: %v", err)
	}
	if _, _, err := w.s.CreateOperatorKey(ctx, core.InstallOperator("test"), " "); kind(err) != apperr.KindInvalid {
		t.Fatalf("a key with no name: %v", err)
	}
	plain, op := operatorKey(t, w.s)
	if !op.Operator || op.OperatorKeyID == nil || op.OrgID != uuid.Nil {
		t.Fatalf("the operator: %+v", op)
	}
	keys, err := w.s.OperatorKeys(ctx, core.InstallOperator("test"))
	if err != nil || !strings.Contains(keyIDs(keys), id.Format(id.OperatorKey, *op.OperatorKeyID)) {
		t.Fatalf("listing keys: %v, %v", keys, err)
	}
	for _, bad := range []string{"", "ald_op_nope", strings.Replace(plain, "ald_op_", "ald_live_", 1)} {
		if _, err := w.s.AuthenticateOperatorKey(ctx, bad, ""); kind(err) != apperr.KindUnauthorized {
			t.Errorf("authenticating %q: %v", bad, err)
		}
	}
	if err := w.s.RevokeOperatorKey(ctx, core.InstallOperator("test"), *op.OperatorKeyID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.AuthenticateOperatorKey(ctx, plain, ""); code(err) != "operator_key_revoked" {
		t.Fatalf("a revoked key: %v", err)
	}
	if err := w.s.RevokeOperatorKey(ctx, core.InstallOperator("test"), *op.OperatorKeyID); kind(err) != apperr.KindNotFound {
		t.Fatalf("revoking twice: %v", err)
	}
}

func keyIDs(keys []*model.OperatorKey) string {
	var ids []string
	for _, k := range keys {
		ids = append(ids, id.Format(id.OperatorKey, k.ID))
	}
	return strings.Join(ids, ",")
}

// TestOperatedOrgs checks the operator creates an org whose first owner
// joins by invitation, finds it by its external reference, and changes it,
// all audited with the key (ADR 0031).
func TestOperatedOrgs(t *testing.T) {
	t.Parallel()
	s := service(t)
	ctx := t.Context()
	_, op := operatorKey(t, s)
	ref := "cus_" + uuid.NewString()
	email := "first-" + uuid.NewString()[:8] + "@example.com"
	two := 2

	if _, _, _, err := s.CreateOperatedOrg(ctx, core.Actor{}, core.OperatedOrgInput{Name: "x", OwnerEmail: email}); kind(err) != apperr.KindForbidden {
		t.Fatalf("not the operator: %v", err)
	}
	if _, _, _, err := s.CreateOperatedOrg(ctx, op, core.OperatedOrgInput{Name: "", OwnerEmail: "nope"}); kind(err) != apperr.KindInvalid {
		t.Fatalf("no name or email: %v", err)
	}
	o, link, inv, err := s.CreateOperatedOrg(ctx, op, core.OperatedOrgInput{Name: "Hosted " + ref, OwnerEmail: email, ExternalRef: ref,
		Limits: model.OrgLimits{Brands: &two}})
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != model.OrgActive || o.ExternalRef != ref || inv.Role != model.RoleOwner || !strings.HasPrefix(link, "https://araldo.test/invite/") {
		t.Fatalf("created %+v, %s, %+v", o, link, inv)
	}
	if _, _, _, err := s.CreateOperatedOrg(ctx, op, core.OperatedOrgInput{Name: "Again", OwnerEmail: email, ExternalRef: ref}); code(err) != "external_ref_taken" {
		t.Fatalf("a second org with the reference: %v", err)
	}

	// The first owner creates their account from the link, and owns it.
	u, _, err := s.AcceptInvitationNewAccount(ctx, strings.TrimPrefix(link, "https://araldo.test/invite/"), "First", "correct horse battery", "req_test")
	if err != nil {
		t.Fatal(err)
	}
	owner, m, err := s.MemberActor(ctx, u.ID, o.ID, false, "req_test")
	if err != nil || m.Role != model.RoleOwner {
		t.Fatalf("the first owner: %+v, %v", m, err)
	}
	if got, err := s.Org(ctx, owner); err != nil || got.Limits.Brands == nil || *got.Limits.Brands != 2 {
		t.Fatalf("the org as its owner sees it: %+v, %v", got, err)
	}

	found, _, err := s.OperatedOrgs(ctx, op, store.Page{}, ref)
	if err != nil || len(found) != 1 || found[0].ID != o.ID {
		t.Fatalf("by reference: %v, %v", found, err)
	}
	if none, _, err := s.OperatedOrgs(ctx, op, store.Page{}, "cus_none_"+uuid.NewString()); err != nil || len(none) != 0 {
		t.Fatalf("an unknown reference: %v, %v", none, err)
	}

	in, _, err := s.InOrg(ctx, op, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, note := model.OrgReadOnly, "Payment failed"
	changed, err := s.ChangeOrg(ctx, in, core.OrgChange{Status: &readOnly, StatusNote: &note, Limits: &model.OrgLimits{}})
	if err != nil || changed.Status != model.OrgReadOnly || changed.StatusNote != note || changed.Limits.Brands != nil {
		t.Fatalf("change: %+v, %v", changed, err)
	}
	bad := model.OrgStatus("gone")
	if _, err := s.ChangeOrg(ctx, in, core.OrgChange{Status: &bad}); kind(err) != apperr.KindInvalid {
		t.Fatalf("an unknown status: %v", err)
	}
	if _, err := s.ChangeOrg(ctx, owner, core.OrgChange{Status: &readOnly}); kind(err) != apperr.KindForbidden {
		t.Fatalf("an owner changing their own status: %v", err)
	}
	if _, _, err := s.InOrg(ctx, op, uuid.New()); kind(err) != apperr.KindNotFound {
		t.Fatalf("an org that does not exist: %v", err)
	}

	var key, status string
	if err := open(t).Pool().QueryRow(ctx, `SELECT detail->>'operator_key', detail->>'status' FROM audit_events
		WHERE org_id = $1 AND action = 'org.operate'`, o.ID).Scan(&key, &status); err != nil ||
		key != id.Format(id.OperatorKey, *op.OperatorKeyID) || status != "read_only" {
		t.Fatalf("the audit: %q %q, %v", key, status, err)
	}

	// Another owner, invited by the operator, joins too.
	link2, _, err := s.InviteAsOperator(ctx, in, "second-"+uuid.NewString()[:8]+"@example.com", model.RoleOwner)
	if err != nil || link2 == "" {
		t.Fatalf("an operator's invitation: %q, %v", link2, err)
	}
}

// TestOrgLimits checks each limit refuses what would pass it, and only
// that (ADR 0031).
func TestOrgLimits(t *testing.T) {
	t.Parallel()
	w := newWorld(t, withFakeOAuth)
	ctx := t.Context()
	_, op := operatorKey(t, w.s)
	in, _, err := w.s.InOrg(ctx, op, w.org.ID)
	if err != nil {
		t.Fatal(err)
	}
	one, two := 1, 2
	if _, err := w.s.ChangeOrg(ctx, in, core.OrgChange{Limits: &model.OrgLimits{Brands: &one, Members: &two, Channels: &one, PostsMonth: &one}}); err != nil {
		t.Fatal(err)
	}

	// Brands: the world has one.
	if _, err := w.s.CreateBrand(ctx, w.owner, core.BrandInput{Name: "Second", Timezone: "UTC"}); code(err) != "limit_reached" {
		t.Fatalf("a second brand: %v", err)
	}
	if _, err := w.s.CreateBrand(ctx, in, core.BrandInput{Name: "By the operator", Timezone: "UTC"}); err != nil {
		t.Fatalf("the operator is not held back: %v", err)
	}

	// Members: the owner and one invitation fill two; inviting them again
	// replaces it.
	invitee := "invitee-" + uuid.NewString()[:8] + "@example.com"
	if _, _, err := w.s.InviteMember(ctx, w.owner, w.session, invitee, model.RoleEditor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.InviteMember(ctx, w.owner, w.session, invitee, model.RoleAdmin); err != nil {
		t.Fatalf("inviting the same person again: %v", err)
	}
	if _, _, err := w.s.InviteMember(ctx, w.owner, w.session, "other-"+invitee, model.RoleEditor); code(err) != "limit_reached" {
		t.Fatalf("a third seat: %v", err)
	}

	// Channels and posts count in live mode only; the sandbox channel does
	// not count.
	live := memberActor(t, w, true)
	app, err := w.s.CreateProviderApp(ctx, live, core.ProviderAppInput{Provider: platform.Threads, ClientID: "client-1", ClientSecret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.s.FinishConnect(ctx, live, platform.Threads, signIn(t, w, live, app), "many")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.ChooseConnections(ctx, live, platform.Threads, res.State, []string{"acct-a", "acct-b"}); code(err) != "limit_reached" {
		t.Fatalf("two channels where one is left: %v", err)
	}
	if _, err := w.s.ChooseConnections(ctx, live, platform.Threads, res.State, []string{"acct-a"}); err != nil {
		t.Fatalf("one channel: %v", err)
	}
	later := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := w.s.CreatePost(ctx, live, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "One"}, PublishAt: later}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.CreatePost(ctx, live, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "Two"}, PublishAt: later}); code(err) != "limit_reached" {
		t.Fatalf("a second live post this month: %v", err)
	}
	if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "Test"}, PublishAt: later}); err != nil {
		t.Fatalf("a test-mode post: %v", err)
	}

	u, err := w.s.Usage(ctx, w.owner, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if u.Brands != 2 || u.Channels != 1 || u.Members != 2 || u.PostsCreated != 1 {
		t.Fatalf("usage %+v", u)
	}
	if _, err := w.s.Usage(ctx, core.Actor{OrgID: w.org.ID, Role: model.RoleEditor}, time.Now()); kind(err) != apperr.KindForbidden {
		t.Fatalf("an editor reading usage: %v", err)
	}
}

// TestOrgStatus checks a read-only org reads but changes nothing, and a
// suspended one is refused and publishes nothing, while the operator can
// always act (ADR 0031).
func TestOrgStatus(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	_, op := operatorKey(t, w.s)
	in, _, err := w.s.InOrg(ctx, op, w.org.ID)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := w.s.CreateAPIKey(ctx, w.owner, w.session, core.APIKeyInput{Name: "status"})
	if err != nil {
		t.Fatal(err)
	}
	set := func(st model.OrgStatus) {
		t.Helper()
		if _, err := w.s.ChangeOrg(ctx, in, core.OrgChange{Status: &st}); err != nil {
			t.Fatal(err)
		}
	}

	set(model.OrgReadOnly)
	owner := memberActor(t, w, false)
	if _, err := w.s.Brands(ctx, owner); err != nil {
		t.Fatalf("reading in a read-only org: %v", err)
	}
	if _, err := w.s.APIKeys(ctx, owner); err != nil {
		t.Fatalf("listing keys in a read-only org: %v", err)
	}
	if _, err := w.s.CreateBrand(ctx, owner, core.BrandInput{Name: "No", Timezone: "UTC"}); code(err) != "org_read_only" {
		t.Fatalf("changing a read-only org: %v", err)
	}
	byKey, err := w.s.AuthenticateKey(ctx, key, "")
	if err != nil {
		t.Fatalf("a key of a read-only org: %v", err)
	}
	if _, err := w.s.CreatePost(ctx, byKey, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "No"}}); code(err) != "org_read_only" {
		t.Fatalf("a key posting in a read-only org: %v", err)
	}
	if _, err := w.s.CreateBrand(ctx, in, core.BrandInput{Name: "Operator", Timezone: "UTC"}); err != nil {
		t.Fatalf("the operator in a read-only org: %v", err)
	}

	set(model.OrgSuspended)
	if _, err := w.s.AuthenticateKey(ctx, key, ""); code(err) != "org_suspended" {
		t.Fatalf("a key of a suspended org: %v", err)
	}
	// Members reach nothing but a notice (the dashboard's part); the core
	// refuses changes too.
	owner = memberActor(t, w, false)
	if _, err := w.s.CreateBrand(ctx, owner, core.BrandInput{Name: "No", Timezone: "UTC"}); code(err) != "org_suspended" {
		t.Fatalf("changing a suspended org: %v", err)
	}
	// A post due while suspended (made by the operator, whom nothing holds
	// back) waits; once active again, it goes out.
	inTest := in
	inTest.Livemode = false
	p, err := w.s.CreatePost(ctx, inTest, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "Held"}, PublishAt: "now"})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := w.s.PublishDue(ctx, "test-worker"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := w.s.Post(ctx, inTest, p.ID)
	if err != nil || got.Targets[0].Status != model.TargetQueued {
		t.Fatalf("a suspended org's due post: %+v, %v", got, err)
	}
	set(model.OrgActive)
	if got := settle(t, w, p.ID); got.Status != model.PostPublished {
		t.Fatalf("after reactivating: %s", got.Status)
	}
}

// TestBillingLink checks owners are sent to billing with a hand-off that
// names them, verifies, and expires; and nobody else is (ADR 0031).
func TestBillingLink(t *testing.T) {
	t.Parallel()
	key := []byte(strings.Repeat("k", 32))
	w := newWorld(t, func(cfg *core.Config, _ *[]platform.Adapter) {
		cfg.BillingURL, cfg.BillingLinkKey = "https://billing.araldo.example/start?from=araldo", key
	})
	ctx := t.Context()
	if !w.s.Billing() {
		t.Fatal("billing is configured")
	}
	link, err := w.s.BillingLink(ctx, w.owner)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "https://billing.araldo.example/start?") || !strings.Contains(link, "from=araldo") {
		t.Fatalf("link %s", link)
	}
	token := link[strings.Index(link, "token=")+len("token="):]
	c, err := core.VerifyBillingHandoff(key, token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if c.Org != id.Format(id.Org, w.org.ID) || c.User != id.Format(id.User, w.user.ID) || c.Email != w.user.Email || c.Role != model.RoleOwner {
		t.Fatalf("claims %+v", c)
	}
	if _, err := core.VerifyBillingHandoff(key, token, time.Now().Add(core.BillingHandoffTTL)); err == nil {
		t.Fatal("an expired hand-off verified")
	}
	if _, err := core.VerifyBillingHandoff([]byte(strings.Repeat("x", 32)), token, time.Now()); err == nil {
		t.Fatal("a hand-off verified with another key")
	}
	if _, err := core.VerifyBillingHandoff(key, token[:len(token)-2]+"AA", time.Now()); err == nil {
		t.Fatal("a tampered hand-off verified")
	}
	editor := w.owner
	editor.Role = model.RoleEditor
	if _, err := w.s.BillingLink(ctx, editor); kind(err) != apperr.KindForbidden {
		t.Fatalf("an editor's billing link: %v", err)
	}
	if _, err := newWorld(t).s.BillingLink(ctx, w.owner); kind(err) != apperr.KindNotFound {
		t.Fatalf("billing on a server without it: %v", err)
	}
}

// TestOrgsFromSignup checks members cannot create orgs where orgs come
// from sign-up elsewhere (ADR 0031).
func TestOrgsFromSignup(t *testing.T) {
	t.Parallel()
	s := service(t, func(cfg *core.Config, _ *[]platform.Adapter) { cfg.SignupURL = "https://araldo.example/signup" })
	ctx := t.Context()
	u, err := s.CreateUser(ctx, "signup-"+uuid.NewString()[:8]+"@example.com", "", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOwnOrg(ctx, u.ID, "Mine"); kind(err) != apperr.KindForbidden || !strings.Contains(err.Error(), "araldo.example/signup") {
		t.Fatalf("creating an org: %v", err)
	}
	if _, err := service(t).CreateOwnOrg(ctx, u.ID, "Mine "+uuid.NewString()[:8]); err != nil {
		t.Fatalf("creating an org where members may: %v", err)
	}
}

// TestSuspendedOrgsGetNoWebhooks checks a suspended org's webhook
// deliveries wait until it is active again (ADR 0031), and a delivery is
// resent only in its own mode.
func TestSuspendedOrgsGetNoWebhooks(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	ep, _, err := w.s.CreateEndpoint(ctx, w.owner, core.EndpointInput{URL: srv.URL, EventTypes: []string{"post.created"}})
	if err != nil {
		t.Fatal(err)
	}
	_, op := operatorKey(t, w.s)
	in, _, err := w.s.InOrg(ctx, op, w.org.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.Livemode = false
	suspended, active := model.OrgSuspended, model.OrgActive
	if _, err := w.s.ChangeOrg(ctx, in, core.OrgChange{Status: &suspended}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.CreatePost(ctx, in, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "quiet"},
		PublishAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	delivery := func() model.Delivery {
		t.Helper()
		ds, _, err := w.s.Deliveries(ctx, in, ep.ID, store.Page{})
		if err != nil || len(ds) != 1 {
			t.Fatalf("deliveries: %v, %v", ds, err)
		}
		return ds[0]
	}
	for range 3 {
		if _, err := w.s.DeliverDue(ctx, "test-worker"); err != nil {
			t.Fatal(err)
		}
	}
	if d := delivery(); d.Status != "pending" || hits.Load() != 0 {
		t.Fatalf("a suspended org's delivery: %+v, %d requests", d, hits.Load())
	}
	if _, err := w.s.ChangeOrg(ctx, in, core.OrgChange{Status: &active}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); delivery().Status != "succeeded"; time.Sleep(20 * time.Millisecond) {
		if _, err := w.s.DeliverDue(ctx, "test-worker"); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the delivery once active: %+v", delivery())
		}
	}
	live := w.owner
	live.Livemode = true
	if err := w.s.ResendDelivery(ctx, live, delivery().ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("resending a test-mode delivery in live mode: %v", err)
	}
	if err := w.s.ResendDelivery(ctx, w.owner, delivery().ID); err != nil {
		t.Fatalf("resending it in its own mode: %v", err)
	}
}
