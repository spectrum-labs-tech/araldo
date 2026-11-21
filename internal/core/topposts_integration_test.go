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
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// TestTopPostsBlock checks the block names the brand's posts that drew
// engagement, with an image only when it is described, and that it stays
// in its org.
func TestTopPostsBlock(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	m, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 8, 8), Alt: "The new login page"})
	if err != nil {
		t.Fatal(err)
	}
	var published time.Time
	for _, in := range []core.PostInput{
		{BrandID: w.brand.ID, Content: &model.Content{Body: "Passkeys shipped *today*"}, Media: []uuid.UUID{m.ID}},
		{BrandID: w.brand.ID, Content: &model.Content{Body: "Second post"}},
	} {
		p, err := w.s.CreatePost(ctx, w.owner, in)
		if err != nil {
			t.Fatal(err)
		}
		published = *settle(t, w, p.ID).Targets[0].PublishedAt
	}
	w.s.Now = func() time.Time { return published.Add(2 * time.Hour) }
	if _, err := core.CollectEngagementOrg(w.s, w.org.ID); err != nil {
		t.Fatal(err)
	}

	md, err := w.s.TopPostsBlock(ctx, w.owner, w.brand.ID, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Top posts of the day\n", "![The new login page](" + id.Format(id.Media, m.ID) + ")",
		`Passkeys shipped \*today\*`, "Second post", "[On "} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in\n%s", want, md)
		}
	}
	if md, err := w.s.TopPostsBlock(ctx, w.owner, w.brand.ID, 1, 1); err != nil || strings.Count(md, "[On ") != 1 {
		t.Errorf("one post asked for: %v\n%s", err, md)
	}

	for _, tt := range []struct {
		days, n int
		code    string
	}{{0, 3, "days_invalid"}, {91, 3, "days_invalid"}, {7, 0, "count_invalid"}, {7, 11, "count_invalid"}} {
		if _, err := w.s.TopPostsBlock(ctx, w.owner, w.brand.ID, tt.days, tt.n); apperr.As(err).Code != tt.code {
			t.Errorf("%d days, %d posts: %v, want %s", tt.days, tt.n, err, tt.code)
		}
	}
	// Another org cannot read this brand's posts through it.
	other := newWorld(t)
	if _, err := other.s.TopPostsBlock(ctx, other.owner, w.brand.ID, 7, 3); kind(err) != apperr.KindNotFound {
		t.Fatalf("another org's brand: %v, want not found", err)
	}
	// A brand with nothing published says so.
	if _, err := other.s.TopPostsBlock(ctx, other.owner, other.brand.ID, 7, 3); apperr.As(err).Code != "no_top_posts" {
		t.Fatalf("nothing published: %v", err)
	}
}
