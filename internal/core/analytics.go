// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Web analytics (ADR 0025): counts of visits and conversions by the UTM
// tags Araldo writes, read daily from each brand's analytics.
const (
	// AnalyticsReadEvery is how often a source is read.
	AnalyticsReadEvery = 24 * time.Hour
	// AnalyticsLookbackDays are re-read on every read: tools revise them.
	AnalyticsLookbackDays = 7
	// AnalyticsBackfillDays are read the first time.
	AnalyticsBackfillDays = 30
	// MaxAnalyticsGoals bounds a source's goals.
	MaxAnalyticsGoals  = 20
	analyticsReadLease = 15 * time.Minute
	analyticsRetry     = time.Hour
)

func analyticsCredentialsAAD(sourceID uuid.UUID) string {
	return keyring.AAD("analytics_sources", "credentials", sourceID)
}

// AnalyticsProviderInfo describes a provider for connect forms.
type AnalyticsProviderInfo struct {
	Provider analytics.Provider
	Name     string
	Fields   []platform.Field
}

// AnalyticsProviders lists the providers sources connect to in a mode: the
// sandbox in test mode, the install's real ones in live mode.
func (s *Service) AnalyticsProviders(livemode bool) []AnalyticsProviderInfo {
	var out []AnalyticsProviderInfo
	for _, p := range s.analytics.Providers() {
		if (p == analytics.Sandbox) == livemode {
			continue
		}
		src, _ := s.analytics.Get(p)
		out = append(out, AnalyticsProviderInfo{Provider: p, Name: src.Name(), Fields: src.Fields()})
	}
	return out
}

// AnalyticsSourceInput connects a site.
type AnalyticsSourceInput struct {
	BrandID  uuid.UUID
	Provider analytics.Provider
	Goals    []string
	Fields   map[string]string
}

// ConnectAnalyticsSource checks credentials with the provider and stores
// the site, to be read soon.
func (s *Service) ConnectAnalyticsSource(ctx context.Context, a Actor, in AnalyticsSourceInput) (*model.AnalyticsSource, error) {
	if err := a.require(PermBrandsWrite); err != nil {
		return nil, err
	}
	b, err := s.Brand(ctx, a, in.BrandID)
	if err != nil {
		return nil, err
	}
	switch {
	case !a.Livemode && in.Provider != analytics.Sandbox:
		return nil, apperr.Invalid("livemode_required", "provider", "Test mode can only connect the sandbox analytics source.")
	case a.Livemode && in.Provider == analytics.Sandbox:
		return nil, apperr.Invalid("testmode_required", "provider", "The sandbox analytics source exists only in test mode.")
	}
	src, ok := s.analytics.Get(in.Provider)
	if !ok {
		return nil, apperr.Invalid("provider_unsupported", "provider", "This install cannot read %q analytics.", in.Provider)
	}
	goals, err := cleanGoals(in.Goals)
	if err != nil {
		return nil, err
	}
	settings, secrets, err := splitFields(src.Name()+" sources", src.Fields(), in.Fields)
	if err != nil {
		return nil, err
	}
	creds := platform.Credentials{}
	for k, v := range settings {
		creds[k] = v
	}
	for k, v := range secrets {
		creds[k] = v
	}
	site, err := src.Verify(ctx, creds)
	if err != nil {
		return nil, connectError(platform.Provider(src.Name()), err)
	}
	as := &model.AnalyticsSource{ID: id.New(), OrgID: a.OrgID, BrandID: b.ID, Livemode: a.Livemode, Provider: string(in.Provider),
		Site: site.ID, Name: site.Name, Timezone: site.Timezone, Goals: goals, Settings: settings, Status: model.AdAccountActive}
	if len(secrets) > 0 {
		raw, err := json.Marshal(secrets)
		if err != nil {
			return nil, err
		}
		if as.Credentials, err = s.keys.Encrypt(ctx, a.OrgID, analyticsCredentialsAAD(as.ID), raw); err != nil {
			return nil, err
		}
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateAnalyticsSource(ctx, as); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Conflict("analytics_source_connected", "%s is already connected to this brand.", site.Name)
			}
			return err
		}
		return s.audit(ctx, tx, a, "analytics_source.connect", id.Format(id.AnalyticsSource, as.ID), map[string]any{"provider": in.Provider})
	})
	return as, err
}

func cleanGoals(goals []string) ([]string, error) {
	out := []string{}
	for _, g := range goals {
		if g = strings.TrimSpace(g); g != "" && !slices.Contains(out, g) {
			if len(g) > 120 {
				return nil, apperr.Invalid("goal_invalid", "goals", "Goal names are at most 120 characters.")
			}
			out = append(out, g)
		}
	}
	if len(out) > MaxAnalyticsGoals {
		return nil, apperr.Invalid("goals_too_many", "goals", "Give at most %d goals.", MaxAnalyticsGoals)
	}
	return out, nil
}

// AnalyticsSources lists the actor's sources, optionally for one brand.
func (s *Service) AnalyticsSources(ctx context.Context, a Actor, brandID *uuid.UUID) ([]*model.AnalyticsSource, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, err
	}
	if a.BrandID != nil {
		if brandID != nil && *brandID != *a.BrandID {
			return nil, apperr.NotFound("brand")
		}
		brandID = a.BrandID
	}
	return s.store.AnalyticsSources(ctx, a.OrgID, a.Livemode, brandID)
}

// AnalyticsSource returns one of the actor's sources.
func (s *Service) AnalyticsSource(ctx context.Context, a Actor, sourceID uuid.UUID) (*model.AnalyticsSource, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, err
	}
	as, err := s.store.AnalyticsSource(ctx, a.OrgID, sourceID)
	if err != nil {
		return nil, notFound(err, "analytics source")
	}
	if as.Livemode != a.Livemode || a.brandAllowed(as.BrandID) != nil {
		return nil, apperr.NotFound("analytics source")
	}
	return as, nil
}

// DeleteAnalyticsSource forgets a source and its counts.
func (s *Service) DeleteAnalyticsSource(ctx context.Context, a Actor, sourceID uuid.UUID) error {
	if err := a.require(PermBrandsWrite); err != nil {
		return err
	}
	as, err := s.AnalyticsSource(ctx, a, sourceID)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteAnalyticsSource(ctx, a.OrgID, as.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "analytics_source.delete", id.Format(id.AnalyticsSource, as.ID), map[string]any{"provider": as.Provider})
	})
}

// AnalyticsFilter narrows an analytics summary.
type AnalyticsFilter = store.AnalyticsFilter

// AnalyticsSummaryRow is one group: its label (a post's first text, or the
// tag's value) and counts.
type AnalyticsSummaryRow struct {
	store.AnalyticsRow
	Label string
}

// AnalyticsSummary is counts added up by group over days Since through
// Until, with the window's totals.
type AnalyticsSummary struct {
	GroupBy      store.AnalyticsGroup
	Since, Until time.Time
	Totals       store.AnalyticsTotals
	Rows         []AnalyticsSummaryRow
}

// AnalyticsSummary adds up the actor's analytics. The window defaults to
// the 30 days ending today (UTC).
func (s *Service) AnalyticsSummary(ctx context.Context, a Actor, f AnalyticsFilter) (*AnalyticsSummary, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, err
	}
	if a.BrandID != nil {
		if f.BrandID != nil && *f.BrandID != *a.BrandID {
			return nil, apperr.NotFound("brand")
		}
		f.BrandID = a.BrandID
	}
	switch f.GroupBy {
	case "":
		f.GroupBy = store.AnalyticsByPost
	case store.AnalyticsByPost, store.AnalyticsBySource, store.AnalyticsByMedium, store.AnalyticsByCampaign, store.AnalyticsByContent, store.AnalyticsByDay:
	default:
		return nil, apperr.Invalid("group_by_invalid", "group_by", "Group by post, source, medium, campaign, content or day.")
	}
	if f.Until.IsZero() {
		f.Until = ads.Date(s.Now(), time.UTC)
	}
	if f.Since.IsZero() {
		f.Since = f.Until.AddDate(0, 0, -29)
	}
	if f.Since.After(f.Until) || f.Until.Sub(f.Since) > MaxAdsWindowDays*24*time.Hour {
		return nil, apperr.Invalid("window_invalid", "since", "since must be on or before until, at most a year apart.")
	}
	if f.Limit <= 0 {
		f.Limit = 20
	}
	rows, totals, err := s.store.AnalyticsSummary(ctx, a.OrgID, a.Livemode, f)
	if err != nil {
		return nil, err
	}
	sum := &AnalyticsSummary{GroupBy: f.GroupBy, Since: f.Since, Until: f.Until, Totals: totals}
	for _, r := range rows {
		row := AnalyticsSummaryRow{AnalyticsRow: r, Label: r.Key}
		switch f.GroupBy {
		case store.AnalyticsByPost:
			row.Label = s.postLabel(ctx, a, r.Key)
		case store.AnalyticsByCampaign:
			row.Label = s.issueLabel(ctx, a, r.Key)
		}
		sum.Rows = append(sum.Rows, row)
	}
	return sum, nil
}

// postLabel is a post's first text, for a utm_content naming it; the ID
// itself when the post is not the actor's to see.
func (s *Service) postLabel(ctx context.Context, a Actor, content string) string {
	pid, err := id.Parse(id.Post, content)
	if err != nil {
		return content
	}
	p, err := s.Post(ctx, a, pid)
	if err != nil || len(p.Targets) == 0 || len(p.Targets[0].Parts) == 0 {
		return content
	}
	text := p.Targets[0].Parts[0]
	if r := []rune(text); len(r) > 80 {
		text = string(r[:80]) + "…"
	}
	return text
}

// issueLabel is a newsletter issue's subject, for a utm_campaign naming
// it (ADR 0024 decision 10); the campaign itself otherwise.
func (s *Service) issueLabel(ctx context.Context, a Actor, campaign string) string {
	iid, err := id.Parse(id.Issue, campaign)
	if err != nil {
		return campaign
	}
	is, err := s.Issue(ctx, a, iid)
	if err != nil {
		return campaign
	}
	return "Newsletter: " + is.Subject
}

// CollectAnalytics reads the sources that are due.
func (s *Service) CollectAnalytics(ctx context.Context) (int, error) {
	return s.collectAnalytics(ctx, nil)
}

func (s *Service) collectAnalytics(ctx context.Context, orgID *uuid.UUID) (int, error) {
	now := s.Now()
	var providers []string
	for _, p := range s.analytics.Providers() {
		providers = append(providers, string(p))
	}
	due, err := s.store.ClaimDueAnalyticsSources(ctx, orgID, providers, now, analyticsReadLease, 20)
	if err != nil {
		return 0, err
	}
	for _, as := range due {
		if err := s.readAnalyticsSource(ctx, as, now); err != nil {
			return 0, err
		}
	}
	return len(due), nil
}

// readAnalyticsSource reads one source's recent days. A failure is recorded
// on the source and retried later, never returned.
func (s *Service) readAnalyticsSource(ctx context.Context, as *model.AnalyticsSource, now time.Time) error {
	src, ok := s.analytics.Get(analytics.Provider(as.Provider))
	if !ok {
		return s.store.SetAnalyticsSourceStatus(ctx, as.OrgID, as.ID, as.Status, fmt.Sprintf("This install cannot read %s analytics.", as.Provider),
			now.Add(AnalyticsReadEvery))
	}
	creds, err := s.analyticsCredentials(ctx, as)
	if err != nil {
		return s.store.SetAnalyticsSourceStatus(ctx, as.OrgID, as.ID, as.Status, "Could not decrypt the source's credentials.", now.Add(analyticsRetry))
	}
	to := ads.Date(now, location(as.Timezone))
	days := AnalyticsLookbackDays
	if as.ReadAt == nil {
		days = AnalyticsBackfillDays
	}
	from := to.AddDate(0, 0, -(days - 1))
	rows, err := src.Report(ctx, creds, from, to, as.Goals)
	var pe *platform.Error
	switch {
	case err == nil:
		for i := range rows {
			rows[i].UTM = analytics.UTM{Source: analytics.CleanTag(rows[i].UTM.Source), Medium: analytics.CleanTag(rows[i].UTM.Medium),
				Campaign: analytics.CleanTag(rows[i].UTM.Campaign), Content: analytics.CleanTag(rows[i].UTM.Content)}
		}
		return s.store.SaveAnalyticsResults(ctx, as.OrgID, as.ID, from, to, rows, now, now.Add(AnalyticsReadEvery))
	case errors.As(err, &pe) && pe.Kind == platform.AuthRevoked:
		return s.store.SetAnalyticsSourceStatus(ctx, as.OrgID, as.ID, model.AdAccountNeedsReauth,
			truncate("The provider refused the credentials: connect the site again. "+pe.Msg, 500), now.Add(AnalyticsReadEvery))
	default:
		s.log.WarnContext(ctx, "reading an analytics source failed; will retry", "analytics_source", id.Format(id.AnalyticsSource, as.ID), "err", err)
		return s.store.SetAnalyticsSourceStatus(ctx, as.OrgID, as.ID, as.Status, truncate("Reading failed; retrying: "+err.Error(), 500),
			now.Add(analyticsRetry))
	}
}

func (s *Service) analyticsCredentials(ctx context.Context, as *model.AnalyticsSource) (platform.Credentials, error) {
	creds := platform.Credentials{}
	for k, v := range as.Settings {
		creds[k] = v
	}
	if len(as.Credentials) == 0 {
		return creds, nil
	}
	raw, err := s.keys.Decrypt(ctx, as.OrgID, analyticsCredentialsAAD(as.ID), as.Credentials)
	if err != nil {
		return nil, err
	}
	var secrets map[string]string
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return nil, err
	}
	for k, v := range secrets {
		creds[k] = v
	}
	return creds, nil
}

// AnalyticsProviderView is a provider in the API.
type AnalyticsProviderView struct {
	Provider      string           `json:"provider"`
	Name          string           `json:"name"`
	ConnectFields []platform.Field `json:"connect_fields"`
}

// ViewAnalyticsProvider renders a provider.
func ViewAnalyticsProvider(p AnalyticsProviderInfo) AnalyticsProviderView {
	fields := p.Fields
	if fields == nil {
		fields = []platform.Field{}
	}
	return AnalyticsProviderView{Provider: string(p.Provider), Name: p.Name, ConnectFields: fields}
}

// AnalyticsSourceView is a source in the API. Credentials never appear.
type AnalyticsSourceView struct {
	ID         string     `json:"id"`
	Object     string     `json:"object"`
	Brand      string     `json:"brand"`
	Livemode   bool       `json:"livemode"`
	Provider   string     `json:"provider"`
	Site       string     `json:"site"`
	Name       string     `json:"name"`
	Timezone   string     `json:"timezone"`
	Goals      []string   `json:"goals"`
	Status     string     `json:"status"`
	StatusNote string     `json:"status_note,omitempty"`
	ReadAt     *time.Time `json:"read_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ViewAnalyticsSource renders a source.
func ViewAnalyticsSource(a *model.AnalyticsSource) AnalyticsSourceView {
	goals := a.Goals
	if goals == nil {
		goals = []string{}
	}
	return AnalyticsSourceView{ID: id.Format(id.AnalyticsSource, a.ID), Object: "analytics_source", Brand: id.Format(id.Brand, a.BrandID),
		Livemode: a.Livemode, Provider: a.Provider, Site: a.Site, Name: a.Name, Timezone: a.Timezone, Goals: goals, Status: string(a.Status),
		StatusNote: a.StatusNote, ReadAt: utc(a.ReadAt), CreatedAt: a.CreatedAt.UTC()}
}

// AnalyticsRowView is one group of a summary in the API.
type AnalyticsRowView struct {
	ID          string           `json:"id"`
	Label       string           `json:"label"`
	Visitors    int64            `json:"visitors"`
	Visits      int64            `json:"visits"`
	Conversions int64            `json:"conversions"`
	Goals       map[string]int64 `json:"goals"`
}

// AnalyticsSummaryView is a summary in the API.
type AnalyticsSummaryView struct {
	Object   string `json:"object"`
	GroupBy  string `json:"group_by"`
	Since    string `json:"since"`
	Until    string `json:"until"`
	Visitors int64  `json:"visitors"`
	Untagged int64  `json:"untagged"`
	// Conversions counts every conversion in the window, tagged or not.
	Conversions int64              `json:"conversions"`
	Data        []AnalyticsRowView `json:"data"`
}

// ViewAnalyticsSummary renders a summary.
func ViewAnalyticsSummary(sum *AnalyticsSummary) AnalyticsSummaryView {
	v := AnalyticsSummaryView{Object: "analytics_summary", GroupBy: string(sum.GroupBy), Since: sum.Since.Format(time.DateOnly),
		Until: sum.Until.Format(time.DateOnly), Visitors: sum.Totals.Visitors, Untagged: sum.Totals.Untagged,
		Conversions: sum.Totals.Conversions,
		Data:        make([]AnalyticsRowView, 0, len(sum.Rows))}
	for _, r := range sum.Rows {
		v.Data = append(v.Data, AnalyticsRowView{ID: r.Key, Label: r.Label, Visitors: r.Visitors, Visits: r.Visits, Conversions: r.Conversions, Goals: r.Goals})
	}
	return v
}
