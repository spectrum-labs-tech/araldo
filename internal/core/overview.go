// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// The dashboard's overview reads bounded lists in the actor's mode: what
// needs someone, what is coming up, and what the last days brought.

const (
	// OverviewAhead is how far ahead the overview lists what is coming up.
	OverviewAhead = 7 * 24 * time.Hour
	// OverviewRecent is how far back failures still need someone, and the
	// window the overview's activity covers.
	OverviewRecent = 7 * 24 * time.Hour
	// overviewLimit caps each of the overview's lists.
	overviewLimit = 20
)

// Activity is what a window brought (store.Activity).
type Activity = store.Activity

// UpcomingPosts lists posts scheduled or waiting for approval to publish
// within OverviewAhead, soonest first.
func (s *Service) UpcomingPosts(ctx context.Context, a Actor) ([]*model.Post, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, err
	}
	now := s.Now()
	return s.store.UpcomingPosts(ctx, a.OrgID, a.Livemode, a.BrandID, now, now.Add(OverviewAhead), overviewLimit)
}

// UpcomingIssues lists newsletter issues scheduled, sending or waiting for
// approval to send within OverviewAhead, soonest first.
func (s *Service) UpcomingIssues(ctx context.Context, a Actor) ([]*model.Issue, error) {
	if err := a.require(PermNewslettersRead); err != nil {
		return nil, err
	}
	now := s.Now()
	return s.store.UpcomingIssues(ctx, a.OrgID, a.Livemode, a.BrandID, now, now.Add(OverviewAhead), overviewLimit)
}

// PostsNeedingAttention lists posts with a copy needing attention, or
// that failed or partly published within OverviewRecent, newest first.
func (s *Service) PostsNeedingAttention(ctx context.Context, a Actor) ([]*model.Post, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, err
	}
	return s.store.PostsNeedingAttention(ctx, a.OrgID, a.Livemode, a.BrandID, s.Now().Add(-OverviewRecent), overviewLimit)
}

// IssuesNeedingAttention lists newsletter issues with a delivery needing
// attention, or that failed or partly sent within OverviewRecent, newest
// first.
func (s *Service) IssuesNeedingAttention(ctx context.Context, a Actor) ([]*model.Issue, error) {
	if err := a.require(PermNewslettersRead); err != nil {
		return nil, err
	}
	return s.store.IssuesNeedingAttention(ctx, a.OrgID, a.Livemode, a.BrandID, s.Now().Add(-OverviewRecent), overviewLimit)
}

// RecentActivity counts the posts published and newsletter issues sent
// within OverviewRecent, with the issues' delivered emails and clicks.
// Without newsletter access, only posts count.
func (s *Service) RecentActivity(ctx context.Context, a Actor) (Activity, error) {
	if err := a.require(PermPostsRead); err != nil {
		return Activity{}, err
	}
	act, err := s.store.Activity(ctx, a.OrgID, a.Livemode, a.BrandID, s.Now().Add(-OverviewRecent))
	if err != nil {
		return Activity{}, err
	}
	if !a.Can(PermNewslettersRead) {
		act.IssuesSent, act.Delivered, act.Clicks = 0, 0, 0
	}
	return act, nil
}
