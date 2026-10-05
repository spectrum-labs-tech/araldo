// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api_test

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// tokenFor signs a device in as c's owner and returns a request header
// carrying the user token.
func tokenFor(t *testing.T, c *client) map[string]string {
	t.Helper()
	ctx := t.Context()
	start, err := c.s.StartDevice(ctx, "test", false, "")
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.s.User(ctx, *c.owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	login, err := c.s.Login(ctx, u.Email, "correct horse battery", "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.s.DecideDevice(ctx, login.Session, start.UserCode, true); err != nil {
		t.Fatal(err)
	}
	plain, _, err := c.s.PollDevice(ctx, start.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Authorization": "Bearer " + plain}
}

// TestMembersOverTheAPI manages members and the org with a person's token,
// and checks API keys are refused and sudo-mode changes stay in the
// dashboard.
func TestMembersOverTheAPI(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	ctx := t.Context()
	as := tokenFor(t, c)
	send := func(method, p string, body string, h map[string]string) (int, map[string]any) {
		t.Helper()
		ct := ""
		if body != "" {
			ct = "application/json"
		}
		return c.do(method, p, ct, []byte(body), h)
	}

	// API keys are refused outright.
	for _, p := range []string{"/v1/org", "/v1/members", "/v1/invitations"} {
		if status, got := send(http.MethodGet, p, "", nil); status != http.StatusForbidden {
			t.Errorf("GET %s with an API key: %d %v", p, status, got)
		}
	}

	email := fmt.Sprintf("teammate-%s@example.com", uuid.NewString()[:8])
	status, inv := send(http.MethodPost, "/v1/invitations", `{"email":"`+email+`","role":"editor"}`, as)
	link, _ := inv["url"].(string)
	if status != http.StatusCreated || inv["object"] != "invitation" || link == "" {
		t.Fatalf("inviting: %d %v", status, inv)
	}
	if status, got := send(http.MethodGet, "/v1/invitations", "", as); status != http.StatusOK || len(got["data"].([]any)) != 1 {
		t.Fatalf("open invitations: %d %v", status, got)
	}
	if status, got := send(http.MethodGet, "/v1/invitations", "", as); status != http.StatusOK || got["data"].([]any)[0].(map[string]any)["url"] != nil {
		t.Fatalf("a listed invitation shows its link: %v", got)
	}
	u, _ := url.Parse(link)
	newcomer, _, err := c.s.AcceptInvitationNewAccount(ctx, path.Base(u.Path), "Teammate", "a long enough password", "req_test")
	if err != nil {
		t.Fatal(err)
	}
	mid := id.Format(id.User, newcomer.ID)

	if status, got := send(http.MethodGet, "/v1/members", "", as); status != http.StatusOK || len(got["data"].([]any)) != 2 {
		t.Fatalf("members: %d %v", status, got)
	}
	if status, got := send(http.MethodPost, "/v1/members/"+mid, `{"role":"admin"}`, as); status != http.StatusOK || got["role"] != "admin" {
		t.Fatalf("changing a role: %d %v", status, got)
	}
	// Making an owner needs sudo mode: the dashboard's.
	if status, got := send(http.MethodPost, "/v1/members/"+mid, `{"role":"owner"}`, as); status != http.StatusForbidden || got["code"] != "reauthentication_required" {
		t.Fatalf("making an owner with a token: %d %v", status, got)
	}
	if status, got := send(http.MethodDelete, "/v1/members/"+mid, "", as); status != http.StatusOK || got["deleted"] != true {
		t.Fatalf("removing: %d %v", status, got)
	}
	if status, _ := send(http.MethodGet, "/v1/members/"+mid, "", as); status != http.StatusNotFound {
		t.Fatalf("a removed member: %d", status)
	}

	renamed := "Renamed " + uuid.NewString()[:8]
	if status, got := send(http.MethodPost, "/v1/org", `{"name":"`+renamed+`"}`, as); status != http.StatusOK || got["name"] != renamed {
		t.Fatalf("renaming the org: %d %v", status, got)
	}
	if status, got := send(http.MethodGet, "/v1/org", "", as); status != http.StatusOK || got["name"] != renamed || got["require_mfa"] != false {
		t.Fatalf("the org: %d %v", status, got)
	}
}
