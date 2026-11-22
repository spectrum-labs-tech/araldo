// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// The newsletter editor offers scheduling, moving and canceling only
// while an issue can still change.
func TestIssueDataOpen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status model.IssueStatus
		want   bool
	}{
		{model.IssueDraft, true}, {model.IssuePendingApproval, true}, {model.IssueScheduled, true}, {model.IssueSending, true},
		{model.IssueSent, false}, {model.IssuePartiallySent, false}, {model.IssueCanceled, false}, {model.IssueFailed, false},
	}
	for _, tt := range tests {
		if got := (issueData{Issue: &model.Issue{Status: tt.status}}).Open(); got != tt.want {
			t.Errorf("%s: Open() = %v, want %v", tt.status, got, tt.want)
		}
	}
	if (issueData{}).Open() {
		t.Error("a new issue is open")
	}
}

// An issue waiting for approval is overdue once its send time is closer
// than core lets a send be scheduled: approving then sends it at once.
func TestIssueDataOverdue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	tests := []struct {
		name   string
		status model.IssueStatus
		sendAt *time.Time
		want   bool
	}{
		{"an hour past", model.IssuePendingApproval, at(-time.Hour), true},
		{"within the minimum delay", model.IssuePendingApproval, at(time.Minute), true},
		{"ahead", model.IssuePendingApproval, at(time.Hour), false},
		{"no send time", model.IssuePendingApproval, nil, false},
		{"scheduled, not waiting", model.IssueScheduled, at(-time.Hour), false},
		{"a draft", model.IssueDraft, at(-time.Hour), false},
	}
	for _, tt := range tests {
		d := issueData{Issue: &model.Issue{Status: tt.status, SendAt: tt.sendAt}}
		if got := d.overdueAt(now); got != tt.want {
			t.Errorf("%s: overdueAt = %v, want %v", tt.name, got, tt.want)
		}
	}
	if (issueData{}).overdueAt(now) {
		t.Error("a new issue is overdue")
	}
}

func TestInviteArticle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		role model.Role
		want string
	}{
		{model.RoleAdmin, "an"}, {model.RoleOwner, "an"}, {model.RoleEditor, "an"}, {model.RoleViewer, "a"}, {"", "a"},
	}
	for _, tt := range tests {
		if got := (inviteData{Invitation: &model.Invitation{Role: tt.role}}).Article(); got != tt.want {
			t.Errorf("%q: %q, want %q", tt.role, got, tt.want)
		}
	}
	if got := (inviteData{}).Article(); got != "a" {
		t.Errorf("no invitation: %q", got)
	}
}
