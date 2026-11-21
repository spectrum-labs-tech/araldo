// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/newsletter"
)

// Limits on a "top posts" block.
const (
	MaxTopPosts     = 10
	MaxTopPostsDays = 90
)

// TopPostsBlock writes a brand's most engaging posts published in the last
// days as issue Markdown under a heading, for the writer to insert and
// edit (ADR 0024 phase 2). Each post brings its first image with alt text,
// its text as published, and a link to its most engaging copy.
func (s *Service) TopPostsBlock(ctx context.Context, a Actor, brandID uuid.UUID, days, n int) (string, error) {
	if err := a.require(PermNewslettersWrite); err != nil {
		return "", err
	}
	if days < 1 || days > MaxTopPostsDays {
		return "", apperr.Invalid("days_invalid", "days", "Look back between 1 and %d days.", MaxTopPostsDays)
	}
	if n < 1 || n > MaxTopPosts {
		return "", apperr.Invalid("count_invalid", "count", "Choose between 1 and %d posts.", MaxTopPosts)
	}
	if _, err := s.Brand(ctx, a, brandID); err != nil {
		return "", err
	}
	until := s.Now()
	rows, err := s.EngagementSummary(ctx, a, EngagementFilter{BrandID: &brandID, GroupBy: GroupByPost, Since: until.Add(-time.Duration(days) * 24 * time.Hour),
		Until: until, Limit: n})
	if err != nil {
		return "", err
	}
	var posts []newsletter.TopPost
	for _, r := range rows {
		if r.ID == nil || r.Total() == 0 {
			continue
		}
		p, err := s.Post(ctx, a, *r.ID)
		if err != nil {
			if apperr.As(err).Kind == apperr.KindNotFound {
				continue // deleted since it was read
			}
			return "", err
		}
		posts = append(posts, s.topPost(p))
	}
	if len(posts) == 0 {
		return "", apperr.Invalid("no_top_posts", "days", "No post of this brand has engagement from the last %d days yet.", days)
	}
	heading := fmt.Sprintf("Top posts of the last %d days", days)
	switch {
	case days == 1:
		heading = "Top posts of the day"
	case days == 7:
		heading = "Top posts of the week"
	case days >= 28 && days <= 31:
		heading = "Top posts of the month"
	}
	return newsletter.TopPostsMarkdown(heading, posts), nil
}

// topPost is p for a block: the text and link of its most engaging
// published copy, and its first image that has alt text.
func (s *Service) topPost(p *model.Post) newsletter.TopPost {
	var best *model.Target
	for i := range p.Targets {
		t := &p.Targets[i]
		if t.Status != model.TargetPublished {
			continue
		}
		if best == nil || t.Engagement != nil && (best.Engagement == nil || t.Engagement.Total() > best.Engagement.Total()) {
			best = t
		}
	}
	var tp newsletter.TopPost
	if best != nil {
		tp.Network, tp.Link = s.ProviderName(best.Provider), best.Permalink
		if len(best.Parts) > 0 {
			tp.Text = best.Parts[0]
		}
	}
	if tp.Text == "" && p.Content != nil {
		tp.Text = p.Content.Body
	}
	for i := range p.Targets {
		if e := p.Targets[i].Engagement; e != nil {
			tp.Likes, tp.Reposts, tp.Replies = tp.Likes+e.Likes, tp.Reposts+e.Reposts, tp.Replies+e.Replies
		}
	}
	for _, m := range p.Media {
		if m.Alt != "" && strings.HasPrefix(m.ContentType, "image/") {
			tp.Image, tp.Alt = id.Format(id.Media, m.ID), m.Alt
			break
		}
	}
	return tp
}
