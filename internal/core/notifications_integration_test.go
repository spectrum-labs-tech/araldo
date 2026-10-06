// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// member adds someone to w's org with role and returns them.
func member(t *testing.T, w *world, role model.Role) (*model.User, core.Actor) {
	t.Helper()
	ctx := t.Context()
	u, err := w.s.AddMember(ctx, w.owner, w.session, fmt.Sprintf("%s-%s@example.com", role, uuid.NewString()[:8]), role, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := w.s.MemberActor(ctx, u.ID, w.org.ID, false, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	return u, a
}

// inbox lists a person's notifications of a type.
func inbox(t *testing.T, w *world, userID uuid.UUID, typ string) []*model.Notification {
	t.Helper()
	all, err := w.s.Notifications(t.Context(), userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []*model.Notification
	for _, n := range all {
		if n.Type == typ {
			out = append(out, n)
		}
	}
	return out
}

// emailOf is a notification's email status and why.
func emailOf(t *testing.T, n *model.Notification) string {
	t.Helper()
	status, reason, err := open(t).NotificationEmail(t.Context(), n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		return status + ": " + reason
	}
	return status
}

// TestNotifications checks who is told what: recipients with their
// choices, never who caused it, never in test mode, once per dedupe key,
// and an inbox each person reads alone (ADR 0034).
func TestNotifications(t *testing.T) {
	t.Parallel()
	w := newWorld(t, withMail(&outbox{}))
	ctx := t.Context()
	owner2, _ := member(t, w, model.RoleOwner)
	admin, adminActor := member(t, w, model.RoleAdmin)
	editor, _ := member(t, w, model.RoleEditor)

	// Joining: the other owner is told, not the newcomer nor who added them.
	if got := inbox(t, w, owner2.ID, core.NotifyMemberJoined); len(got) != 2 || got[0].Link == "" || emailOf(t, got[0]) != "suppressed: turned off" {
		t.Fatalf("owner2 was told of %d joins", len(got))
	}
	if got := inbox(t, w, w.user.ID, core.NotifyMemberJoined); len(got) != 0 {
		t.Fatalf("the owner who added them was told: %d", len(got))
	}

	// An approval request: approvers (admins and owners), not who asked.
	everyone := []uuid.UUID{w.user.ID, owner2.ID, admin.ID, editor.ID}
	if err := core.NotifyOrg(w.s, w.org.ID, true, core.NotifyPostApproval, "A post waits", "", everyone, &admin.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		who  uuid.UUID
		want int
	}{{w.user.ID, 1}, {owner2.ID, 1}, {admin.ID, 0}, {editor.ID, 1}} {
		got := inbox(t, w, tc.who, core.NotifyPostApproval)
		if len(got) != tc.want {
			t.Errorf("%s was told %d times, want %d", tc.who, len(got), tc.want)
		}
		if len(got) == 1 && emailOf(t, got[0]) != "queued" {
			t.Errorf("the email: %s", emailOf(t, got[0]))
		}
	}

	// Test mode tells no one; a dedupe key tells once.
	if err := core.NotifyOrg(w.s, w.org.ID, false, core.NotifyPostApproval, "In test mode", "", everyone, nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := core.NotifyOrg(w.s, w.org.ID, true, core.NotifyChannelReauth, "Reconnect", "channel:x", []uuid.UUID{w.user.ID}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(inbox(t, w, w.user.ID, core.NotifyPostApproval)); n != 1 {
		t.Fatalf("approvals after one in test mode: %d", n)
	}
	if n := len(inbox(t, w, w.user.ID, core.NotifyChannelReauth)); n != 1 {
		t.Fatalf("a deduplicated notification told %d times", n)
	}

	// Choices: email only hides it from the inbox; both off tells nothing.
	if err := w.s.SetNotificationChoice(ctx, adminActor, core.NotifyChannelReauth, false, true); err != nil {
		t.Fatal(err)
	}
	if err := w.s.SetNotificationChoice(ctx, adminActor, core.NotifyTargetFailed, false, false); err != nil {
		t.Fatal(err)
	}
	if err := w.s.SetNotificationChoice(ctx, adminActor, core.NotifyPasswordChanged, false, false); code(err) != "notification_type_invalid" {
		t.Fatalf("turning off an account notice: %v", err)
	}
	choices, err := w.s.NotificationChoices(ctx, adminActor)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range choices {
		if c.Account {
			t.Fatalf("an account notice among the org's choices: %s", c.Key)
		}
		if c.Key == core.NotifyChannelReauth && (c.InApp || !c.Email) || c.Key == core.NotifyPostApproval && (!c.InApp || !c.Email) {
			t.Fatalf("choice %s: %+v", c.Key, c)
		}
	}
	if err := core.NotifyOrg(w.s, w.org.ID, true, core.NotifyChannelReauth, "Reconnect", "channel:y", []uuid.UUID{admin.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := core.NotifyOrg(w.s, w.org.ID, true, core.NotifyTargetFailed, "Failed", "", []uuid.UUID{admin.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(inbox(t, w, admin.ID, core.NotifyChannelReauth)) + len(inbox(t, w, admin.ID, core.NotifyTargetFailed)); n != 0 {
		t.Fatalf("the admin's inbox shows %d they turned off", n)
	}

	// Reading: one, then all; nobody else's.
	unread, err := w.s.UnreadNotifications(ctx, w.user.ID)
	if err != nil || unread != 2 {
		t.Fatalf("unread: %d, %v", unread, err)
	}
	first := inbox(t, w, w.user.ID, core.NotifyPostApproval)[0]
	if _, err := w.s.OpenNotification(ctx, editor.ID, first.ID); err == nil {
		t.Fatal("someone else opened the owner's notification")
	}
	if n, err := w.s.OpenNotification(ctx, w.user.ID, first.ID); err != nil || n.ReadAt == nil || n.Link != "/posts" {
		t.Fatalf("opening: %+v, %v", n, err)
	}
	if unread, _ := w.s.UnreadNotifications(ctx, w.user.ID); unread != 1 {
		t.Fatalf("unread after opening one: %d", unread)
	}
	if err := w.s.ReadAllNotifications(ctx, w.user.ID); err != nil {
		t.Fatal(err)
	}
	if unread, _ := w.s.UnreadNotifications(ctx, w.user.ID); unread != 0 {
		t.Fatalf("unread after reading all: %d", unread)
	}
	if unread, _ := w.s.UnreadNotifications(ctx, owner2.ID); unread == 0 {
		t.Fatal("reading all read someone else's")
	}
}

// TestAccountNotices checks a person is told of changes to their account,
// by email too, or with why not when the server sends none.
func TestAccountNotices(t *testing.T) {
	t.Parallel()
	for _, mail := range []bool{true, false} {
		var opts []option
		want := "suppressed: the server sends no email"
		if mail {
			opts, want = append(opts, withMail(&outbox{})), "queued"
		}
		w := newWorld(t, opts...)
		if err := w.s.ChangePassword(t.Context(), w.session, "correct horse battery", "a whole new password"); err != nil {
			t.Fatal(err)
		}
		got := inbox(t, w, w.user.ID, core.NotifyPasswordChanged)
		if len(got) != 1 || got[0].OrgID != nil || emailOf(t, got[0]) != want {
			t.Fatalf("mail %v: %d notices", mail, len(got))
		}
	}
}
