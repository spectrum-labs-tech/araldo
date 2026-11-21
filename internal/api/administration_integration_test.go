// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// operatorClient is c with a new operator key, and the key's ID.
func operatorClient(t *testing.T, c *client) (*client, string) {
	t.Helper()
	plain, k, err := c.s.CreateOperatorKey(t.Context(), core.InstallOperator("test"), "admin "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	op := *c
	op.key = plain
	return &op, id.Format(id.OperatorKey, k.ID)
}

// TestAdministrationCredentials checks each credential reaches only its
// own API: org keys and members' tokens never the operator API's
// administration, operator keys never the org API (ADR 0031, ADR 0035).
func TestAdministrationCredentials(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	op, _ := operatorClient(t, c)
	token := tokenFor(t, c)
	org := id.Format(id.Org, c.owner.OrgID)
	user := id.Format(id.User, *c.owner.UserID)
	operatorRoutes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/operator/users?email=a%40example.com", ""},
		{http.MethodPost, "/v1/operator/users", `{"email":"x@example.com","password":"correct horse battery"}`},
		{http.MethodPost, "/v1/operator/users/" + user + "/password", `{"password":"correct horse battery"}`},
		{http.MethodGet, "/v1/operator/orgs/" + org + "/members", ""},
		{http.MethodPost, "/v1/operator/orgs/" + org + "/members", `{"email":"x@example.com","role":"viewer"}`},
		{http.MethodPost, "/v1/operator/orgs/" + org + "/members/" + user, `{"role":"viewer"}`},
		{http.MethodDelete, "/v1/operator/orgs/" + org + "/members/" + user, ""},
		{http.MethodPost, "/v1/operator/orgs/" + org + "/api_keys", `{"name":"x"}`},
		{http.MethodGet, "/v1/operator/operator_keys", ""},
		{http.MethodPost, "/v1/operator/operator_keys", `{"name":"x"}`},
		{http.MethodGet, "/v1/operator/apps", ""},
		{http.MethodGet, "/v1/operator/master_keys", ""},
		{http.MethodPost, "/v1/operator/master_keys/rewrap", `{}`},
	}
	for _, r := range operatorRoutes {
		for who, header := range map[string]map[string]string{"an org key": nil, "a member's token": token} {
			if status, got := c.do(r.method, r.path, "application/json", []byte(r.body), header); status != http.StatusUnauthorized {
				t.Errorf("%s on %s %s: %d %v", who, r.method, r.path, status, got)
			}
		}
	}
	for _, p := range []string{"/v1/members", "/v1/org", "/v1/api_keys"} {
		if status, got := op.do(http.MethodGet, p, "", nil, nil); status != http.StatusUnauthorized || !strings.Contains(fmt.Sprint(got["detail"]), "/v1/operator/") {
			t.Errorf("an operator key on %s: %d %v", p, status, got)
		}
	}
	// The owner is still the owner: none of the refused calls did anything.
	if status, got := c.do(http.MethodGet, "/v1/members", "", nil, token); status != http.StatusOK || !strings.Contains(fmt.Sprint(got["data"]), "owner") {
		t.Fatalf("members after: %d %v", status, got)
	}
}

// TestAdministrationOverTheOperatorAPI drives what `araldo admin` does,
// through the operator API: accounts, members and API keys in an org,
// operator keys, and master keys (ADR 0035). Another org is untouched.
func TestAdministrationOverTheOperatorAPI(t *testing.T) {
	t.Parallel()
	c, other := newClient(t), newClient(t)
	op, opKeyID := operatorClient(t, c)
	org := "/v1/operator/orgs/" + id.Format(id.Org, c.owner.OrgID)
	suffix := uuid.NewString()[:8]
	email := "made-" + suffix + "@example.com"

	// Accounts: created with a password that is never returned, found by
	// email, and reset.
	status, u := op.json(http.MethodPost, "/v1/operator/users", map[string]any{"email": email, "name": "Made", "password": "correct horse battery"})
	if status != http.StatusCreated || u["object"] != "user" || u["email"] != email || u["password"] != nil || u["mfa"] != false {
		t.Fatalf("create: %d %v", status, u)
	}
	userID := u["id"].(string)
	if status, got := op.json(http.MethodPost, "/v1/operator/users", map[string]any{"email": email, "password": "correct horse battery"}); status != http.StatusConflict ||
		got["code"] != "email_taken" {
		t.Fatalf("the same email: %d %v", status, got)
	}
	if status, got := op.json(http.MethodPost, "/v1/operator/users", map[string]any{"email": "x-" + email, "password": "short"}); status != http.StatusUnprocessableEntity {
		t.Fatalf("a short password: %d %v", status, got)
	}
	status, found := op.do(http.MethodGet, "/v1/operator/users?email="+url.QueryEscape(strings.ToUpper(email)), "", nil, nil)
	if data, _ := found["data"].([]any); status != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["id"] != userID {
		t.Fatalf("find: %d %v", status, found)
	}
	status, found = op.do(http.MethodGet, "/v1/operator/users?email=nobody-"+suffix+"%40example.com", "", nil, nil)
	if data, _ := found["data"].([]any); status != http.StatusOK || len(data) != 0 {
		t.Fatalf("find nobody: %d %v", status, found)
	}
	if status, got := op.do(http.MethodGet, "/v1/operator/users", "", nil, nil); status != http.StatusBadRequest || got["code"] != "parameter_missing" {
		t.Fatalf("find without an email: %d %v", status, got)
	}
	if status, got := op.json(http.MethodPost, "/v1/operator/users/"+userID+"/password", map[string]any{"password": "a brand new password"}); status != http.StatusOK ||
		got["id"] != userID {
		t.Fatalf("reset: %d %v", status, got)
	}
	if _, err := c.s.Login(t.Context(), email, "a brand new password", "test", "127.0.0.1"); err != nil {
		t.Fatalf("signing in after the reset: %v", err)
	}

	// Members, as the operator in the org.
	if status, got := op.json(http.MethodPost, org+"/members", map[string]any{"email": email, "role": "editor"}); status != http.StatusCreated ||
		got["role"] != "editor" || got["id"] != userID {
		t.Fatalf("add: %d %v", status, got)
	}
	stranger := "stranger-" + suffix + "@example.com"
	if status, got := op.json(http.MethodPost, org+"/members", map[string]any{"email": stranger, "role": "viewer"}); status != http.StatusUnprocessableEntity ||
		got["code"] != "password_required" {
		t.Fatalf("add someone without an account or password: %d %v", status, got)
	}
	if status, got := op.json(http.MethodPost, org+"/members", map[string]any{"email": stranger, "role": "viewer", "password": "a temporary password"}); status != http.StatusCreated {
		t.Fatalf("add with a temporary password: %d %v", status, got)
	}
	if status, got := op.json(http.MethodPost, org+"/members/"+userID, map[string]any{"role": "admin"}); status != http.StatusOK || got["role"] != "admin" {
		t.Fatalf("role: %d %v", status, got)
	}
	status, list := op.do(http.MethodGet, org+"/members", "", nil, nil)
	if status != http.StatusOK || !strings.Contains(fmt.Sprint(list["data"]), email) || !strings.Contains(fmt.Sprint(list["data"]), stranger) {
		t.Fatalf("list: %d %v", status, list)
	}
	owner := id.Format(id.User, *c.owner.UserID)
	if status, got := op.json(http.MethodPost, org+"/members/"+owner, map[string]any{"role": "viewer"}); status != http.StatusConflict || got["code"] != "last_owner" {
		t.Fatalf("demoting the last owner: %d %v", status, got)
	}
	// Another org's member is not this org's.
	otherOwner := id.Format(id.User, *other.owner.UserID)
	if status, got := op.do(http.MethodDelete, org+"/members/"+otherOwner, "", nil, nil); status != http.StatusNotFound {
		t.Fatalf("removing another org's owner here: %d %v", status, got)
	}
	if status, got := op.do(http.MethodDelete, org+"/members/"+userID, "", nil, nil); status != http.StatusOK || got["deleted"] != true {
		t.Fatalf("remove: %d %v", status, got)
	}

	// API keys, shown once, in the mode asked for.
	status, key := op.json(http.MethodPost, org+"/api_keys", map[string]any{"name": "audit " + suffix, "scopes": []string{"audit:read"}})
	secret, _ := key["secret"].(string)
	if status != http.StatusCreated || !strings.HasPrefix(secret, "ald_test_") || key["livemode"] != false {
		t.Fatalf("an API key: %d %v", status, key)
	}
	if status, live := op.json(http.MethodPost, org+"/api_keys", map[string]any{"name": "live " + suffix, "livemode": true, "brand": c.brand}); status != http.StatusCreated ||
		!strings.HasPrefix(fmt.Sprint(live["secret"]), "ald_live_") || live["brand"] != c.brand {
		t.Fatalf("a live key for a brand: %d %v", status, live)
	}
	if status, got := op.json(http.MethodPost, org+"/api_keys", map[string]any{"name": "x", "brand": "no-such-brand"}); status == http.StatusCreated {
		t.Fatalf("a key for a missing brand: %d %v", status, got)
	}

	// The org's audit log, read with that key, names the operator key, not
	// a member.
	auditor := *c
	auditor.key = secret
	status, events := auditor.do(http.MethodGet, "/v1/audit_events?limit=100", "", nil, nil)
	var adds int
	for _, e := range events["data"].([]any) {
		e := e.(map[string]any)
		if e["action"] == "member.add" {
			adds++
			if d, _ := e["detail"].(map[string]any); e["user"] != nil || d["operator_key"] != opKeyID {
				t.Errorf("member.add audited as %v", e)
			}
		}
	}
	if status != http.StatusOK || adds != 2 {
		t.Fatalf("audit: %d, %d member.add entries: %v", status, adds, events)
	}

	// The org by name, and the owners' settings.
	o, err := c.s.Org(t.Context(), c.owner)
	if err != nil {
		t.Fatal(err)
	}
	status, byName := op.do(http.MethodGet, "/v1/operator/orgs?name="+url.QueryEscape(strings.ToUpper(o.Name)), "", nil, nil)
	if data, _ := byName["data"].([]any); status != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["id"] != id.Format(id.Org, c.owner.OrgID) {
		t.Fatalf("by name: %d %v", status, byName)
	}
	if status, got := op.json(http.MethodPost, org, map[string]any{"require_mfa": true}); status != http.StatusOK || got["require_mfa"] != true {
		t.Fatalf("require two-factor: %d %v", status, got)
	}
	if status, got := op.json(http.MethodPost, org, map[string]any{"require_sso": true}); status != http.StatusUnprocessableEntity {
		t.Fatalf("require single sign-on with no provider: %d %v", status, got)
	}
}

// TestOperatorKeysAndMasterKeysOverTheAPI makes and revokes operator keys,
// never letting one revoke itself, and reads and rotates master keys in
// batches (ADR 0035).
func TestOperatorKeysAndMasterKeysOverTheAPI(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	op, _ := operatorClient(t, c)
	name := "service " + uuid.NewString()[:8]
	status, made := op.json(http.MethodPost, "/v1/operator/operator_keys", map[string]any{"name": name})
	secret, _ := made["secret"].(string)
	if status != http.StatusCreated || !strings.HasPrefix(secret, "ald_op_") || made["object"] != "operator_key" {
		t.Fatalf("create: %d %v", status, made)
	}
	madeID := made["id"].(string)
	status, keys := op.do(http.MethodGet, "/v1/operator/operator_keys", "", nil, nil)
	if status != http.StatusOK || !strings.Contains(fmt.Sprint(keys["data"]), madeID) || strings.Contains(fmt.Sprint(keys), secret) {
		t.Fatalf("list: %d %v", status, keys)
	}
	newer := *op
	newer.key = secret
	if status, got := newer.do(http.MethodDelete, "/v1/operator/operator_keys/"+madeID, "", nil, nil); status != http.StatusConflict || got["code"] != "operator_key_self" {
		t.Fatalf("a key revoking itself: %d %v", status, got)
	}
	if status, got := op.do(http.MethodDelete, "/v1/operator/operator_keys/"+madeID, "", nil, nil); status != http.StatusOK || got["deleted"] != true {
		t.Fatalf("revoke: %d %v", status, got)
	}
	if status, got := newer.do(http.MethodGet, "/v1/operator/operator_keys", "", nil, nil); status != http.StatusUnauthorized || got["code"] != "operator_key_revoked" {
		t.Fatalf("the revoked key: %d %v", status, got)
	}

	status, mk := op.do(http.MethodGet, "/v1/operator/master_keys", "", nil, nil)
	data, _ := mk["data"].([]any)
	if status != http.StatusOK || len(data) == 0 {
		t.Fatalf("master keys: %d %v", status, mk)
	}
	if first := data[0].(map[string]any); first["id"] != "test" || first["primary"] != true || first["configured"] != true {
		t.Fatalf("the primary: %v", first)
	}
	// One master key in every test keyring: nothing to move, however often
	// it is asked.
	for range 2 {
		if status, got := op.json(http.MethodPost, "/v1/operator/master_keys/rewrap", map[string]any{"batch_size": 50}); status != http.StatusOK ||
			got["object"] != "master_key_rewrap" || got["rewrapped"] != float64(0) || got["remaining"] != float64(0) {
			t.Fatalf("rewrap: %d %v", status, got)
		}
	}
	if status, got := op.json(http.MethodPost, "/v1/operator/master_keys/rewrap", map[string]any{"batch_size": 100000}); status != http.StatusUnprocessableEntity {
		t.Fatalf("a batch too big: %d %v", status, got)
	}

	// Apps: a platform that does not connect with OAuth is refused, and an
	// app that does not exist is not found.
	if status, got := op.json(http.MethodPost, "/v1/operator/apps", map[string]any{"provider": "nowhere", "client_id": "c", "client_secret": "s"}); status != http.StatusUnprocessableEntity {
		t.Fatalf("an app for no platform: %d %v", status, got)
	}
	if status, got := op.do(http.MethodDelete, "/v1/operator/apps/"+id.Format(id.ProviderApp, uuid.New()), "", nil, nil); status != http.StatusNotFound {
		t.Fatalf("removing no app: %d %v", status, got)
	}
	if status, got := op.do(http.MethodGet, "/v1/operator/apps", "", nil, nil); status != http.StatusOK || got["object"] != "list" {
		t.Fatalf("apps: %d %v", status, got)
	}
}
