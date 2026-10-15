// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"testing"

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
