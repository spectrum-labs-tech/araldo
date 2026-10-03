// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Reports (ADR 0026): one brand's results over a period, beside the period
// of the same length before it, computed from what Araldo already reads.

// MaxReportDays bounds a report's period.
const MaxReportDays = 366

// reportTop is how many rows a report's lists show.
const reportTop = 5

// ReportInput chooses a report's brand and period: Month ("2006-01") in
// the brand's time zone, or Since and Until (dates, inclusive). With
// neither, the last full month.
type ReportInput struct {
	BrandID      uuid.UUID
	Month        string
	Since, Until time.Time
}

// Pair is a figure for the period and for the one before.
type Pair struct {
	Now, Before int64
}

// Report is a brand's results over a period. A section is nil when the
// brand has nothing in it, or the reader may not see it.
type Report struct {
	Brand *model.Brand
	// Since and Until are the period's first and last days, and PrevSince
	// and PrevUntil the previous period's, as midnight UTC.
	Since, Until         time.Time
	PrevSince, PrevUntil time.Time
	// Settling is set when the period takes in the last week, whose
	// analytics and ad numbers sources still revise.
	Settling    bool
	Publishing  *ReportPublishing
	Engagement  *ReportEngagement
	Web         *ReportWeb
	Ads         *ReportAds
	Newsletters *ReportNewsletters
}

// ReportPublishing is what was published, and what failed.
type ReportPublishing struct {
	Published, Failed Pair
	ByNetwork         []store.NetworkCount
}

// ReportEngagement is the engagement of posts published in the period.
type ReportEngagement struct {
	Likes, Reposts, Replies, Quotes, Views Pair
	TopPosts                               []EngagementRow
	ByChannel                              []EngagementRow
}

// Interactions adds up likes, reposts, replies and quotes.
func (e *ReportEngagement) Interactions() Pair {
	return Pair{Now: e.Likes.Now + e.Reposts.Now + e.Replies.Now + e.Quotes.Now,
		Before: e.Likes.Before + e.Reposts.Before + e.Replies.Before + e.Quotes.Before}
}

// ReportWeb is the brand's web analytics.
type ReportWeb struct {
	Visitors, Signups, Untagged Pair
	Sources, Campaigns, Posts   []AnalyticsSummaryRow
}

// ReportAds is ad spend and what it brought, a total per currency.
type ReportAds struct {
	Totals    []ReportSpend
	Campaigns []store.AdsRow
}

// ReportSpend is ad totals in one currency.
type ReportSpend struct {
	Currency                    string
	Spend, Clicks, Signups      Pair
	CostPerSignup, CostPerClick Pair
}

// ReportNewsletters is the issues sent in the period.
type ReportNewsletters struct {
	Issues, Delivered, Clicks, Unsubscribes Pair
	Sent                                    []*model.Issue
}

// reportPeriod resolves a report's period, in the brand's time zone.
func reportPeriod(in ReportInput, b *model.Brand, now time.Time) (since, until time.Time, err error) {
	loc := location(b.Timezone)
	today := ads.Date(now, loc)
	switch {
	case in.Month != "":
		m, perr := time.Parse("2006-01", in.Month)
		if perr != nil {
			return since, until, apperr.Invalid("month_invalid", "month", "month is YYYY-MM.")
		}
		since, until = m, m.AddDate(0, 1, -1)
	case !in.Since.IsZero() || !in.Until.IsZero():
		since, until = in.Since, in.Until
		if until.IsZero() {
			until = today
		}
		if since.IsZero() {
			since = until.AddDate(0, 0, -29)
		}
	default:
		first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		since, until = first.AddDate(0, -1, 0), first.AddDate(0, 0, -1)
	}
	switch {
	case since.After(until):
		return since, until, apperr.Invalid("period_invalid", "since", "since must be on or before until.")
	case until.Sub(since) >= MaxReportDays*24*time.Hour:
		return since, until, apperr.Invalid("period_invalid", "since", "A report covers at most a year.")
	case since.After(today):
		return since, until, apperr.Invalid("period_invalid", "since", "The period has not started yet.")
	}
	return since, until, nil
}

// BrandReport computes a brand's report.
func (s *Service) BrandReport(ctx context.Context, a Actor, in ReportInput) (*Report, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, err
	}
	b, err := s.Brand(ctx, a, in.BrandID)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	r := &Report{Brand: b}
	if r.Since, r.Until, err = reportPeriod(in, b, now); err != nil {
		return nil, err
	}
	days := int(r.Until.Sub(r.Since).Hours()/24) + 1
	r.PrevUntil = r.Since.AddDate(0, 0, -1)
	r.PrevSince = r.PrevUntil.AddDate(0, 0, -(days - 1))
	r.Settling = !r.Until.Before(ads.Date(now, location(b.Timezone)).AddDate(0, 0, -AnalyticsLookbackDays))

	steps := []func(context.Context, Actor, *Report) error{s.reportPublishing, s.reportEngagement, s.reportWeb}
	if a.Can(PermAdsRead) {
		steps = append(steps, s.reportAds)
	}
	if a.Can(PermNewslettersRead) {
		steps = append(steps, s.reportNewsletters)
	}
	for _, step := range steps {
		if err := step(ctx, a, r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// window is a period's start and end as instants: midnight of its first
// day to midnight after its last, in the brand's time zone.
func (r *Report) window(since, until time.Time) (time.Time, time.Time) {
	loc := location(r.Brand.Timezone)
	return time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, loc),
		time.Date(until.Year(), until.Month(), until.Day()+1, 0, 0, 0, 0, loc)
}

func (s *Service) reportPublishing(ctx context.Context, a Actor, r *Report) error {
	from, to := r.window(r.Since, r.Until)
	cur, err := s.store.PublishingCounts(ctx, a.OrgID, a.Livemode, r.Brand.ID, from, to)
	if err != nil {
		return err
	}
	from, to = r.window(r.PrevSince, r.PrevUntil)
	prev, err := s.store.PublishingCounts(ctx, a.OrgID, a.Livemode, r.Brand.ID, from, to)
	if err != nil {
		return err
	}
	p := &ReportPublishing{ByNetwork: cur}
	for _, n := range cur {
		p.Published.Now += n.Published
		p.Failed.Now += n.Failed
	}
	for _, n := range prev {
		p.Published.Before += n.Published
		p.Failed.Before += n.Failed
	}
	if p.Published != (Pair{}) || p.Failed != (Pair{}) {
		r.Publishing = p
	}
	return nil
}

func (s *Service) reportEngagement(ctx context.Context, a Actor, r *Report) error {
	e := &ReportEngagement{}
	for i, period := range [][2]time.Time{{r.Since, r.Until}, {r.PrevSince, r.PrevUntil}} {
		from, to := r.window(period[0], period[1])
		f := EngagementFilter{BrandID: &r.Brand.ID, Since: from, Until: to, GroupBy: store.GroupByChannel, Limit: 100}
		channels, err := s.EngagementSummary(ctx, a, f)
		if err != nil {
			return err
		}
		for _, c := range channels {
			add := func(p *Pair, n int64) {
				if i == 0 {
					p.Now += n
				} else {
					p.Before += n
				}
			}
			add(&e.Likes, c.Likes)
			add(&e.Reposts, c.Reposts)
			add(&e.Replies, c.Replies)
			add(&e.Quotes, c.Quotes)
			if c.Views != nil {
				add(&e.Views, *c.Views)
			}
		}
		if i == 0 {
			e.ByChannel = channels
			f.GroupBy, f.Limit = store.GroupByPost, reportTop
			if e.TopPosts, err = s.EngagementSummary(ctx, a, f); err != nil {
				return err
			}
		}
	}
	if len(e.ByChannel) > 0 || e.Interactions().Before > 0 {
		r.Engagement = e
	}
	return nil
}

func (s *Service) reportWeb(ctx context.Context, a Actor, r *Report) error {
	sources, err := s.AnalyticsSources(ctx, a, &r.Brand.ID)
	if err != nil || len(sources) == 0 {
		return err
	}
	w := &ReportWeb{}
	f := AnalyticsFilter{BrandID: &r.Brand.ID, Since: r.Since, Until: r.Until, Limit: reportTop}
	for _, g := range []struct {
		group store.AnalyticsGroup
		rows  *[]AnalyticsSummaryRow
	}{{store.AnalyticsBySource, &w.Sources}, {store.AnalyticsByCampaign, &w.Campaigns}, {store.AnalyticsByPost, &w.Posts}} {
		f.GroupBy = g.group
		sum, err := s.AnalyticsSummary(ctx, a, f)
		if err != nil {
			return err
		}
		*g.rows = sum.Rows
		w.Visitors.Now, w.Untagged.Now, w.Signups.Now = sum.Totals.Visitors, sum.Totals.Untagged, sum.Totals.Conversions
	}
	f.Since, f.Until, f.GroupBy = r.PrevSince, r.PrevUntil, store.AnalyticsBySource
	prev, err := s.AnalyticsSummary(ctx, a, f)
	if err != nil {
		return err
	}
	w.Visitors.Before, w.Untagged.Before, w.Signups.Before = prev.Totals.Visitors, prev.Totals.Untagged, prev.Totals.Conversions
	r.Web = w
	return nil
}

func (s *Service) reportAds(ctx context.Context, a Actor, r *Report) error {
	accts, err := s.AdAccounts(ctx, a, &r.Brand.ID)
	if err != nil || len(accts) == 0 {
		return err
	}
	ad := &ReportAds{}
	byCurrency := map[string]*ReportSpend{}
	for i, period := range [][2]time.Time{{r.Since, r.Until}, {r.PrevSince, r.PrevUntil}} {
		sum, err := s.AdsSummary(ctx, a, AdsFilter{BrandID: &r.Brand.ID, GroupBy: store.AdsByCampaign, Since: period[0], Until: period[1], Limit: 100})
		if err != nil {
			return err
		}
		for _, row := range sum.Rows {
			t := byCurrency[row.Currency]
			if t == nil {
				t = &ReportSpend{Currency: row.Currency}
				byCurrency[row.Currency] = t
			}
			if i == 0 {
				t.Spend.Now += row.Spend
				t.Clicks.Now += row.Clicks
				t.Signups.Now += row.Signups
			} else {
				t.Spend.Before += row.Spend
				t.Clicks.Before += row.Clicks
				t.Signups.Before += row.Signups
			}
		}
		if i == 0 {
			ad.Campaigns = sum.Rows
			if len(ad.Campaigns) > 10 {
				ad.Campaigns = ad.Campaigns[:10]
			}
		}
	}
	for _, t := range byCurrency {
		t.CostPerSignup = Pair{Now: CostPer(t.Spend.Now, t.Signups.Now), Before: CostPer(t.Spend.Before, t.Signups.Before)}
		t.CostPerClick = Pair{Now: CostPer(t.Spend.Now, t.Clicks.Now), Before: CostPer(t.Spend.Before, t.Clicks.Before)}
		ad.Totals = append(ad.Totals, *t)
	}
	sort.Slice(ad.Totals, func(i, j int) bool { return ad.Totals[i].Spend.Now > ad.Totals[j].Spend.Now })
	r.Ads = ad
	return nil
}

func (s *Service) reportNewsletters(ctx context.Context, a Actor, r *Report) error {
	n := &ReportNewsletters{}
	for i, period := range [][2]time.Time{{r.Since, r.Until}, {r.PrevSince, r.PrevUntil}} {
		from, to := r.window(period[0], period[1])
		issues, err := s.store.IssuesSent(ctx, a.OrgID, a.Livemode, r.Brand.ID, from, to)
		if err != nil {
			return err
		}
		var res model.MailResults
		for _, is := range issues {
			for _, d := range is.Deliveries {
				res.Delivered += d.Results.Delivered
				res.Clicks += d.Results.Clicks
				res.Unsubscribes += d.Results.Unsubscribes
			}
		}
		if i == 0 {
			n.Sent = issues
			n.Issues.Now, n.Delivered.Now, n.Clicks.Now, n.Unsubscribes.Now = int64(len(issues)), res.Delivered, res.Clicks, res.Unsubscribes
		} else {
			n.Issues.Before, n.Delivered.Before, n.Clicks.Before, n.Unsubscribes.Before = int64(len(issues)), res.Delivered, res.Clicks, res.Unsubscribes
		}
	}
	if n.Issues != (Pair{}) {
		r.Newsletters = n
	}
	return nil
}
