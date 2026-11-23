// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// listMailer is a Mailer with three audiences; checkAudiences only reads
// them, so the rest is left to the embedded nil interface.
type listMailer struct{ email.Mailer }

func (listMailer) Name() string { return "Lists" }
func (listMailer) Audiences(context.Context, platform.Credentials) ([]email.Audience, error) {
	return []email.Audience{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}, {ID: "c", Name: "C"}}, nil
}

// limitedMailer sends a campaign to one audience, as some providers do.
type limitedMailer struct{ listMailer }

func (limitedMailer) MaxAudiences() int { return 1 }

func TestCheckAudiencesKeepsTheProvidersLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		m        email.Mailer
		ids      []string
		want     int
		wantCode string
	}{
		{"no limit, all three", listMailer{}, []string{"a", "b", "c"}, 3, ""},
		{"limited, one", limitedMailer{}, []string{"b"}, 1, ""},
		{"limited, a duplicate counts once", limitedMailer{}, []string{"b", " b"}, 1, ""},
		{"limited, two", limitedMailer{}, []string{"a", "b"}, 0, "audience_too_many"},
		{"an unknown audience is named first", limitedMailer{}, []string{"a", "z"}, 0, "audience_unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := (&Service{}).checkAudiences(context.Background(), tt.m, platform.Credentials{}, tt.ids, "audiences")
			if tt.wantCode != "" {
				e := apperr.As(err)
				if e == nil || e.Kind != apperr.KindInvalid || e.Code != tt.wantCode && !hasProblem(e, tt.wantCode) {
					t.Fatalf("err = %v, want %s", err, tt.wantCode)
				}
				return
			}
			if err != nil || len(got) != tt.want {
				t.Fatalf("got %v, %v; want %d audiences", got, err, tt.want)
			}
		})
	}
}

func hasProblem(e *apperr.Error, code string) bool {
	for _, p := range e.Problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

func TestMaxAudiences(t *testing.T) {
	t.Parallel()
	if got := email.MaxAudiences(listMailer{}); got != 0 {
		t.Errorf("unlimited mailer: %d", got)
	}
	if got := email.MaxAudiences(limitedMailer{}); got != 1 {
		t.Errorf("limited mailer: %d", got)
	}
}

// Too many default audiences says the defaults are optional; too many on
// one issue does not, since an issue must go somewhere.
func TestTooManyAudiencesSaysDefaultsAreOptional(t *testing.T) {
	t.Parallel()
	for field, want := range map[string]bool{"default_audiences": true, "deliveries[0].audiences": false} {
		_, err := (&Service{}).checkAudiences(context.Background(), limitedMailer{}, platform.Credentials{}, []string{"a", "b"}, field)
		e := apperr.As(err)
		if e == nil || e.Code != "audience_too_many" || e.Param != field {
			t.Fatalf("%s: err = %v", field, err)
		}
		if got := strings.Contains(e.Message, "Defaults are optional"); got != want {
			t.Errorf("%s: %q, mentions optional defaults = %v, want %v", field, e.Message, got, want)
		}
	}
}
