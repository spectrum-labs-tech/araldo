// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// PairView is a figure for the period and the one before.
type PairView struct {
	Value    int64 `json:"value"`
	Previous int64 `json:"previous"`
}

func viewPair(p Pair) PairView { return PairView{Value: p.Now, Previous: p.Before} }

// ReportView is a brand's report in the API (ADR 0026).
type ReportView struct {
	Object      string                 `json:"object"`
	Brand       string                 `json:"brand"`
	Since       string                 `json:"since"`
	Until       string                 `json:"until"`
	Previous    ReportPeriodView       `json:"previous"`
	Settling    bool                   `json:"settling"`
	Publishing  *ReportPublishingView  `json:"publishing"`
	Engagement  *ReportEngagementView  `json:"engagement"`
	Web         *ReportWebView         `json:"web"`
	Ads         *ReportAdsView         `json:"ads"`
	Newsletters *ReportNewslettersView `json:"newsletters"`
}

// ReportPeriodView is a period's first and last days.
type ReportPeriodView struct {
	Since string `json:"since"`
	Until string `json:"until"`
}

// ReportPublishingView is what was published.
type ReportPublishingView struct {
	Published PairView           `json:"published"`
	Failed    PairView           `json:"failed"`
	ByNetwork []NetworkCountView `json:"by_network"`
}

// NetworkCountView is one network's published and failed counts.
type NetworkCountView struct {
	Provider  string `json:"provider"`
	Published int64  `json:"published"`
	Failed    int64  `json:"failed"`
}

// ReportEngagementView is the engagement of what was published.
type ReportEngagementView struct {
	Likes     PairView            `json:"likes"`
	Reposts   PairView            `json:"reposts"`
	Replies   PairView            `json:"replies"`
	Quotes    PairView            `json:"quotes"`
	Views     PairView            `json:"views"`
	TopPosts  []ReportEngagedView `json:"top_posts"`
	ByChannel []ReportEngagedView `json:"by_channel"`
}

// ReportEngagedView is a post's or a channel's engagement.
type ReportEngagedView struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Provider string `json:"provider,omitempty"`
	Likes    int64  `json:"likes"`
	Reposts  int64  `json:"reposts"`
	Replies  int64  `json:"replies"`
	Quotes   int64  `json:"quotes"`
}

// ReportWebView is the brand's web analytics.
type ReportWebView struct {
	Visitors     PairView           `json:"visitors"`
	Signups      PairView           `json:"signups"`
	Untagged     PairView           `json:"untagged"`
	TopSources   []AnalyticsRowView `json:"top_sources"`
	TopCampaigns []AnalyticsRowView `json:"top_campaigns"`
	TopPosts     []AnalyticsRowView `json:"top_posts"`
}

// ReportAdsView is ad spend per currency, and by campaign.
type ReportAdsView struct {
	Totals    []ReportSpendView `json:"totals"`
	Campaigns []AdsRowView      `json:"campaigns"`
}

// ReportSpendView is ad totals in one currency, in its minor unit.
type ReportSpendView struct {
	Currency      string   `json:"currency"`
	Spend         PairView `json:"spend"`
	Clicks        PairView `json:"clicks"`
	Signups       PairView `json:"signups"`
	CostPerClick  PairView `json:"cost_per_click"`
	CostPerSignup PairView `json:"cost_per_signup"`
}

// ReportNewslettersView is the issues sent.
type ReportNewslettersView struct {
	Issues       PairView          `json:"issues"`
	Delivered    PairView          `json:"delivered"`
	Clicks       PairView          `json:"clicks"`
	Unsubscribes PairView          `json:"unsubscribes"`
	Sent         []ReportIssueView `json:"sent"`
}

// ReportIssueView is one issue sent in the period.
type ReportIssueView struct {
	ID      string          `json:"id"`
	Subject string          `json:"subject"`
	SendAt  *time.Time      `json:"send_at"`
	Results MailResultsView `json:"results"`
}

// ViewReport renders a report.
func ViewReport(r *Report) ReportView {
	v := ReportView{Object: "report", Brand: id.Format(id.Brand, r.Brand.ID), Since: r.Since.Format(time.DateOnly), Until: r.Until.Format(time.DateOnly),
		Previous: ReportPeriodView{Since: r.PrevSince.Format(time.DateOnly), Until: r.PrevUntil.Format(time.DateOnly)}, Settling: r.Settling}
	if p := r.Publishing; p != nil {
		v.Publishing = &ReportPublishingView{Published: viewPair(p.Published), Failed: viewPair(p.Failed), ByNetwork: make([]NetworkCountView, 0, len(p.ByNetwork))}
		for _, n := range p.ByNetwork {
			v.Publishing.ByNetwork = append(v.Publishing.ByNetwork, NetworkCountView{Provider: n.Provider, Published: n.Published, Failed: n.Failed})
		}
	}
	if e := r.Engagement; e != nil {
		v.Engagement = &ReportEngagementView{Likes: viewPair(e.Likes), Reposts: viewPair(e.Reposts), Replies: viewPair(e.Replies),
			Quotes: viewPair(e.Quotes), Views: viewPair(e.Views), TopPosts: []ReportEngagedView{}, ByChannel: []ReportEngagedView{}}
		for _, row := range e.TopPosts {
			v.Engagement.TopPosts = append(v.Engagement.TopPosts, viewEngaged(id.Post, row))
		}
		for _, row := range e.ByChannel {
			v.Engagement.ByChannel = append(v.Engagement.ByChannel, viewEngaged(id.Channel, row))
		}
	}
	if w := r.Web; w != nil {
		v.Web = &ReportWebView{Visitors: viewPair(w.Visitors), Signups: viewPair(w.Signups), Untagged: viewPair(w.Untagged),
			TopSources: viewAnalyticsRows(w.Sources), TopCampaigns: viewAnalyticsRows(w.Campaigns), TopPosts: viewAnalyticsRows(w.Posts)}
	}
	if ad := r.Ads; ad != nil {
		v.Ads = &ReportAdsView{Totals: []ReportSpendView{}, Campaigns: ViewAdsSummary(&AdsSummary{GroupBy: "campaign", Rows: ad.Campaigns}).Data}
		for _, t := range ad.Totals {
			v.Ads.Totals = append(v.Ads.Totals, ReportSpendView{Currency: t.Currency, Spend: viewPair(t.Spend), Clicks: viewPair(t.Clicks),
				Signups: viewPair(t.Signups), CostPerClick: viewPair(t.CostPerClick), CostPerSignup: viewPair(t.CostPerSignup)})
		}
	}
	if n := r.Newsletters; n != nil {
		v.Newsletters = &ReportNewslettersView{Issues: viewPair(n.Issues), Delivered: viewPair(n.Delivered), Clicks: viewPair(n.Clicks),
			Unsubscribes: viewPair(n.Unsubscribes), Sent: []ReportIssueView{}}
		for _, is := range n.Sent {
			v.Newsletters.Sent = append(v.Newsletters.Sent, ReportIssueView{ID: id.Format(id.Issue, is.ID), Subject: is.Subject,
				SendAt: utc(is.SendAt), Results: ViewIssue(is).Results})
		}
	}
	return v
}

func viewEngaged(prefix id.Prefix, row EngagementRow) ReportEngagedView {
	v := ReportEngagedView{Label: row.Label, Provider: row.Provider, Likes: row.Likes, Reposts: row.Reposts, Replies: row.Replies, Quotes: row.Quotes}
	if row.ID != nil {
		v.ID = id.Format(prefix, *row.ID)
	}
	return v
}

func viewAnalyticsRows(rows []AnalyticsSummaryRow) []AnalyticsRowView {
	return ViewAnalyticsSummary(&AnalyticsSummary{Rows: rows}).Data
}
