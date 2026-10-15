// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// The apps page's guide ticks its redirect-URI and connect steps off only
// once something connected through one of the org's own apps.
func TestUsesOwnApp(t *testing.T) {
	t.Parallel()
	own, install, other := uuid.New(), uuid.New(), uuid.New()
	apps := []*model.ProviderApp{{ID: own}, {ID: install, Install: true}}
	tests := []struct {
		name  string
		chans []*model.Channel
		accts []*model.AdAccount
		want  bool
	}{
		{"nothing connected", nil, nil, false},
		{"a channel without an app", []*model.Channel{{}}, nil, false},
		{"a channel through the install's app", []*model.Channel{{AppID: &install}}, nil, false},
		{"a channel through another org's app", []*model.Channel{{AppID: &other}}, nil, false},
		{"a channel through the org's app", []*model.Channel{{}, {AppID: &own}}, nil, true},
		{"an ad account through the install's app", nil, []*model.AdAccount{{AppID: &install, InstallApp: true}}, false},
		{"an ad account through the org's app", nil, []*model.AdAccount{{AppID: &own}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := usesOwnApp(apps, tt.chans, tt.accts); got != tt.want {
				t.Errorf("usesOwnApp = %v, want %v", got, tt.want)
			}
		})
	}
	if usesOwnApp(nil, []*model.Channel{{AppID: &own}}, nil) {
		t.Error("no apps, yet used")
	}
}
