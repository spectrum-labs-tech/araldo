// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform/sandbox"
)

// TestOverview checks the overview's lists: what is coming up in the next
// week, soonest first and nothing later; posts and issues needing someone;
// and what the last week published, each in the actor's org and mode only.
func TestOverview(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	setTheme(t, w)
	connectSandboxMail(t, w, "news@araldo.dev", nil)

	post := func(body string, at time.Time, meta map[string]string) *model.Post {
		t.Helper()
		in := core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: body}, Metadata: meta}
		if !at.IsZero() {
			in.PublishAt = at.UTC().Format(time.RFC3339)
		}
		p, err := w.s.CreatePost(ctx, w.owner, in)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	later := post("In three days", time.Now().Add(72*time.Hour), nil)
	sooner := post("Tomorrow", time.Now().Add(24*time.Hour), nil)
	post("Next month", time.Now().Add(30*24*time.Hour), nil)
	is, err := w.s.CreateIssue(ctx, w.owner, core.IssueInput{BrandID: w.brand.ID, Subject: "This week", Body: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.ScheduleIssue(ctx, w.owner, is.ID, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	up, err := w.s.UpcomingPosts(ctx, w.owner)
	if err != nil || len(up) != 2 || up[0].ID != sooner.ID || up[1].ID != later.ID {
		t.Fatalf("upcoming posts: %v, %v", ids(up), err)
	}
	issues, err := w.s.UpcomingIssues(ctx, w.owner)
	if err != nil || len(issues) != 1 || issues[0].ID != is.ID || len(issues[0].Deliveries) != 1 {
		t.Fatalf("upcoming issues: %+v, %v", issues, err)
	}

	// A post published now counts; one whose copy is uncertain needs someone.
	settle(t, w, post("Now", time.Time{}, nil).ID)
	stuck := post(sandbox.SimTimeoutAfterSend, time.Time{}, map[string]string{"araldo_simulate": sandbox.SimTimeoutAfterSend})
	settle(t, w, stuck.ID)
	att, err := w.s.PostsNeedingAttention(ctx, w.owner)
	if err != nil || len(att) != 1 || att[0].ID != stuck.ID {
		t.Fatalf("posts needing attention: %v, %v", ids(att), err)
	}
	if got, err := w.s.IssuesNeedingAttention(ctx, w.owner); err != nil || len(got) != 0 {
		t.Fatalf("issues needing attention: %d, %v", len(got), err)
	}
	act, err := w.s.RecentActivity(ctx, w.owner)
	if err != nil || act.PostsPublished != 1 || act.IssuesSent != 0 {
		t.Fatalf("activity: %+v, %v", act, err)
	}

	// Another org, and the same org in live mode, see none of it.
	other := newWorld(t)
	live := w.owner
	live.Livemode = true
	for name, a := range map[string]core.Actor{"another org": other.owner, "live mode": live} {
		if got, err := w.s.UpcomingPosts(ctx, a); err != nil || len(got) != 0 {
			t.Errorf("%s: upcoming posts %v, %v", name, ids(got), err)
		}
		if got, err := w.s.UpcomingIssues(ctx, a); err != nil || len(got) != 0 {
			t.Errorf("%s: upcoming issues %d, %v", name, len(got), err)
		}
		if got, err := w.s.PostsNeedingAttention(ctx, a); err != nil || len(got) != 0 {
			t.Errorf("%s: posts needing attention %v, %v", name, ids(got), err)
		}
		if got, err := w.s.RecentActivity(ctx, a); err != nil || got.PostsPublished != 0 {
			t.Errorf("%s: activity %+v, %v", name, got, err)
		}
	}
}

func ids(ps []*model.Post) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID.String()
	}
	return out
}
