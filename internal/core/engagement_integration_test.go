// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

func TestEngagementIsReadOnSchedule(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "Launch day"}})
	if err != nil {
		t.Fatal(err)
	}
	got := settle(t, w, p.ID)
	tg := got.Targets[0]
	if tg.Status != model.TargetPublished || tg.Engagement == nil || tg.Engagement.State != model.EngagementCollecting ||
		tg.Engagement.ReadAt != nil || tg.Engagement.NextReadAt == nil {
		t.Fatalf("after publishing: %s, engagement %+v", tg.Status, tg.Engagement)
	}
	firstRead := *tg.Engagement.NextReadAt
	if d := firstRead.Sub(*tg.PublishedAt); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("first reading %s after publishing, want an hour", d)
	}

	// Nothing is due yet.
	if n, err := core.CollectEngagementOrg(w.s, w.org.ID); err != nil || n != 0 {
		t.Fatalf("collecting before the first reading: %d, %v", n, err)
	}

	// Two hours later the first reading happens; the next is at six hours.
	w.s.Now = func() time.Time { return tg.PublishedAt.Add(2 * time.Hour) }
	if n, err := core.CollectEngagementOrg(w.s, w.org.ID); err != nil || n != 1 {
		t.Fatalf("collecting at +2h: %d, %v", n, err)
	}
	got, err = w.s.Post(ctx, w.owner, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	e := got.Targets[0].Engagement
	if e.ReadAt == nil || e.NextReadAt == nil || !e.NextReadAt.Equal(tg.PublishedAt.Add(6*time.Hour)) || e.Views == nil {
		t.Fatalf("after the first reading: %+v", e)
	}
	if v := core.ViewPost(got).Targets[0].Engagement; v == nil || v.Total != e.Total() || v.State != model.EngagementCollecting {
		t.Fatalf("view %+v", v)
	}

	// Past the last scheduled reading, the schedule ends.
	w.s.Now = func() time.Time { return tg.PublishedAt.Add(31 * 24 * time.Hour) }
	if n, err := core.CollectEngagementOrg(w.s, w.org.ID); err != nil || n != 1 {
		t.Fatalf("collecting at +31d: %d, %v", n, err)
	}
	readings, err := w.s.EngagementReadings(ctx, w.owner, tg.ID)
	if err != nil || len(readings) != 2 {
		t.Fatalf("readings: %d, %v", len(readings), err)
	}
	got, _ = w.s.Post(ctx, w.owner, p.ID)
	if e := got.Targets[0].Engagement; e.State != model.EngagementDone || e.NextReadAt != nil {
		t.Fatalf("after the schedule: %+v", e)
	}
	if n, _ := core.CollectEngagementOrg(w.s, w.org.ID); n != 0 {
		t.Fatalf("collected %d after the schedule ended", n)
	}
}

func TestEngagementSummary(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	var published time.Time
	var target uuid.UUID
	for _, body := range []string{"First", "Second", "Third"} {
		p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: body}})
		if err != nil {
			t.Fatal(err)
		}
		tg := settle(t, w, p.ID).Targets[0]
		published, target = *tg.PublishedAt, tg.ID
	}
	w.s.Now = func() time.Time { return published.Add(2 * time.Hour) }
	if n, err := core.CollectEngagementOrg(w.s, w.org.ID); err != nil || n != 3 {
		t.Fatalf("collected %d, %v", n, err)
	}
	window := core.EngagementFilter{Since: published.Add(-time.Hour), Until: published.Add(time.Hour)}

	posts, err := w.s.EngagementSummary(ctx, w.owner, window)
	if err != nil || len(posts) != 3 {
		t.Fatalf("by post: %d rows, %v", len(posts), err)
	}
	for i := 1; i < len(posts); i++ {
		if posts[i].Total() > posts[i-1].Total() {
			t.Fatalf("rows not sorted by engagement: %+v", posts)
		}
	}
	var sum int64
	for _, r := range posts {
		sum += r.Total()
		if r.Posts != 1 || r.Label == "" {
			t.Fatalf("post row %+v", r)
		}
	}
	window.GroupBy = store.GroupByChannel
	chans, err := w.s.EngagementSummary(ctx, w.owner, window)
	if err != nil || len(chans) != 1 || chans[0].Posts != 3 || chans[0].Total() != sum || chans[0].Label != w.channel.DisplayName ||
		chans[0].Provider != "sandbox" || *chans[0].ID != w.channel.ID {
		t.Fatalf("by channel: %+v, %v (total want %d)", chans, err, sum)
	}
	window.GroupBy = store.GroupByTemplate
	tmpls, err := w.s.EngagementSummary(ctx, w.owner, window)
	if err != nil || len(tmpls) != 1 || tmpls[0].ID != nil || tmpls[0].Posts != 3 {
		t.Fatalf("by template: %+v, %v", tmpls, err)
	}

	// Another org sees none of it.
	other := newWorld(t)
	if rows, err := other.s.EngagementSummary(ctx, other.owner, window); err != nil || len(rows) != 0 {
		t.Fatalf("another org's summary: %d rows, %v", len(rows), err)
	}
	if _, err := other.s.EngagementReadings(ctx, other.owner, target); kind(err) != apperr.KindNotFound {
		t.Fatalf("another org's readings: %v, want not found", err)
	}
}

func TestEngagementSummaryValidates(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	now := time.Now()
	for _, f := range []core.EngagementFilter{
		{GroupBy: "day"},
		{Since: now, Until: now.Add(-time.Hour)},
		{Since: now.Add(-400 * 24 * time.Hour), Until: now},
	} {
		if _, err := w.s.EngagementSummary(t.Context(), w.owner, f); kind(err) != apperr.KindInvalid {
			t.Errorf("summary %+v: %v, want invalid", f, err)
		}
	}
}

// A key limited to one brand cannot read another brand's history.
func TestBrandKeysSeeOnlyTheirBrandsHistory(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "history"}})
	if err != nil {
		t.Fatal(err)
	}
	tg := settle(t, w, p.ID).Targets[0]
	other, err := w.s.CreateBrand(ctx, w.owner, core.BrandInput{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := w.s.CreateOperatorAPIKey(ctx, w.owner, core.APIKeyInput{Name: "other brand", BrandID: &other.ID})
	if err != nil {
		t.Fatal(err)
	}
	key, err := w.s.AuthenticateKey(ctx, plain, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Attempts(ctx, key, tg.ID); kind(err) != apperr.KindNotFound {
		t.Errorf("attempts of another brand's target: %v, want not found", err)
	}
	if _, err := w.s.EngagementReadings(ctx, key, tg.ID); kind(err) != apperr.KindNotFound {
		t.Errorf("engagement of another brand's target: %v, want not found", err)
	}
	if at, err := w.s.Attempts(ctx, w.owner, tg.ID); err != nil || len(at) == 0 {
		t.Errorf("the owner's own attempts: %d, %v", len(at), err)
	}
}

func TestPostSearch(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	for _, body := range []string{"Launch: 100% off", "Launch: 100x faster", "under_score sale", "Nothing here"} {
		if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: body}, PublishAt: "next_slot"}); err != nil {
			t.Fatal(err)
		}
	}
	tests := map[string]int{
		"launch":      2, // case insensitive
		"100%":        1, // % is a character, not a wildcard
		"under_score": 1,
		"under score": 0,
		"g_h":         0, // _ is a character too: as a wildcard it would match "Nothing here"
		"absent":      0,
	}
	for q, want := range tests {
		got, _, err := w.s.Posts(ctx, w.owner, core.PostFilter{Query: q}, store.Page{})
		if err != nil || len(got) != want {
			t.Errorf("search %q: %d posts, %v; want %d", q, len(got), err, want)
		}
	}
	counts, err := w.s.PostStatusCounts(ctx, w.owner, core.PostFilter{Query: "launch", Status: "published"})
	if err != nil || counts[model.PostScheduled] != 2 {
		t.Fatalf("counts ignoring status: %v, %v", counts, err)
	}
	if _, _, err := w.s.Posts(ctx, w.owner, core.PostFilter{Query: strings.Repeat("x", 201)}, store.Page{}); kind(err) != apperr.KindInvalid {
		t.Fatalf("a 201-character search: %v", err)
	}
}

// TestEngagementResumesOnNewlyReadablePlatforms checks that posts marked
// unsupported, as those on a platform before its reader was added, are
// read once a version that reads it runs: once per process (ADR 0018).
func TestEngagementResumesOnNewlyReadablePlatforms(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	p, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "Before the reader"}})
	if err != nil {
		t.Fatal(err)
	}
	tg := settle(t, w, p.ID).Targets[0]
	st := open(t)
	if err := st.SetEngagementState(ctx, w.org.ID, tg.ID, model.EngagementUnsupported, nil, ""); err != nil {
		t.Fatal(err)
	}
	w.s.Now = func() time.Time { return tg.PublishedAt.Add(2 * time.Hour) }
	if n, err := core.CollectEngagementOrg(w.s, w.org.ID); err != nil || n != 1 {
		t.Fatalf("the first collection after the upgrade: %d, %v", n, err)
	}
	got, err := w.s.Post(ctx, w.owner, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if e := got.Targets[0].Engagement; e.State != model.EngagementCollecting || e.ReadAt == nil {
		t.Fatalf("after resuming: %+v", e)
	}

	// Later collections do not look again.
	if err := st.SetEngagementState(ctx, w.org.ID, tg.ID, model.EngagementUnsupported, nil, ""); err != nil {
		t.Fatal(err)
	}
	if n, err := core.CollectEngagementOrg(w.s, w.org.ID); err != nil || n != 0 {
		t.Fatalf("a later collection: %d, %v", n, err)
	}
}
