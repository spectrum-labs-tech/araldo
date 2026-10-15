// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Data for the form partials in templates/_forms.html.

// brandChoice is the brandSelect partial's data.
type brandChoice struct {
	// All labels the option for every brand (value ""); none when empty.
	All     string
	Options []brandOption
}

type brandOption struct {
	Value, Name string
	Selected    bool
}

// brandOptions offers brands by ID, with the one chosen (by ID or slug, as
// ResolveBrand takes either) selected, after an "all" option if all is set.
func brandOptions(brands []*model.Brand, chosen, all string) brandChoice {
	c := brandChoice{All: all, Options: make([]brandOption, len(brands))}
	for i, b := range brands {
		v := id.Format(id.Brand, b.ID)
		c.Options[i] = brandOption{Value: v, Name: b.Name, Selected: chosen != "" && (chosen == v || chosen == b.Slug)}
	}
	return c
}

// providerInput is one connect field for the providerField partial, with
// the value the form had; a secret's is never sent back to the page.
type providerInput struct {
	platform.Field
	Value string
}

func fieldInput(f platform.Field, values map[string]string) providerInput {
	in := providerInput{Field: f}
	if !f.Secret {
		in.Value = values[f.Name]
	}
	return in
}
