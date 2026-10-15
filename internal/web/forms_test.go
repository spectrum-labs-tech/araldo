// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestBrandOptions(t *testing.T) {
	t.Parallel()
	a := &model.Brand{ID: uuid.New(), Slug: "araldo", Name: "Araldo"}
	b := &model.Brand{ID: uuid.New(), Slug: "open-b00ks", Name: "Open B00KS"}
	brands := []*model.Brand{a, b}
	tests := []struct {
		name, chosen, all string
		want              int // index selected, -1 for none
	}{
		{"none chosen", "", "All brands", -1},
		{"by ID", id.Format(id.Brand, b.ID), "", 1},
		{"by slug", "araldo", "", 0},
		{"unknown", "brand_nope", "", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := brandOptions(brands, tt.chosen, tt.all)
			if c.All != tt.all || len(c.Options) != 2 {
				t.Fatalf("got %+v", c)
			}
			for i, o := range c.Options {
				if o.Value != id.Format(id.Brand, brands[i].ID) || o.Name != brands[i].Name {
					t.Errorf("option %d: %+v", i, o)
				}
				if o.Selected != (i == tt.want) {
					t.Errorf("option %d selected %v, want %v", i, o.Selected, i == tt.want)
				}
			}
		})
	}
}

// A form shown again keeps what was typed, except secrets.
func TestFieldInput(t *testing.T) {
	t.Parallel()
	values := map[string]string{"site": "araldo.dev", "key": "typed"}
	if in := fieldInput(platform.Field{Name: "site"}, values); in.Value != "araldo.dev" || in.Name != "site" {
		t.Errorf("plain field: %+v", in)
	}
	if in := fieldInput(platform.Field{Name: "key", Secret: true}, values); in.Value != "" {
		t.Errorf("a secret comes back: %+v", in)
	}
	if in := fieldInput(platform.Field{Name: "other"}, nil); in.Value != "" {
		t.Errorf("no values: %+v", in)
	}
}
