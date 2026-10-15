// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestMapErr(t *testing.T) {
	t.Parallel()
	other := errors.New("boom")
	tests := []struct {
		name       string
		err        error
		want       error
		constraint string
	}{
		{"nil", nil, nil, ""},
		{"no rows", pgx.ErrNoRows, ErrNotFound, ""},
		{"unique", &pgconn.PgError{Code: "23505", ConstraintName: "users_email_normalized_key"}, ErrConflict, "users_email_normalized_key"},
		{"foreign key", &pgconn.PgError{Code: "23503", ConstraintName: "posts_brand_fk"}, ErrReferenced, "posts_brand_fk"},
		{"check", &pgconn.PgError{Code: "23514", ConstraintName: "posts_status_check"}, ErrInvalid, "posts_status_check"},
		{"wrapped check", fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23514", ConstraintName: "c"}), ErrInvalid, "c"},
		{"another postgres error", &pgconn.PgError{Code: "40001"}, nil, ""},
		{"not a postgres error", other, other, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapErr(tt.err)
			if tt.want != nil && !errors.Is(got, tt.want) {
				t.Errorf("mapErr = %v, want %v", got, tt.want)
			}
			if tt.err == nil && got != nil {
				t.Errorf("mapErr(nil) = %v", got)
			}
			var ce *ConstraintError
			if tt.constraint != "" && (!errors.As(got, &ce) || ce.Constraint != tt.constraint) {
				t.Errorf("mapErr = %v, want constraint %q", got, tt.constraint)
			}
		})
	}
}

func TestConflictOn(t *testing.T) {
	t.Parallel()
	conflict := mapErr(&pgconn.PgError{Code: "23505", ConstraintName: "brands_org_id_slug_key"})
	tests := []struct {
		name string
		err  error
		on   string
		want bool
	}{
		{"its constraint", conflict, "brands_org_id_slug_key", true},
		{"wrapped", fmt.Errorf("create brand: %w", conflict), "brands_org_id_slug_key", true},
		{"another constraint", conflict, "slug_key", false},
		{"a name that only ends the same", conflict, "org_id_slug_key", false},
		{"a foreign key", mapErr(&pgconn.PgError{Code: "23503", ConstraintName: "brands_org_id_slug_key"}), "brands_org_id_slug_key", false},
		{"a message that merely ends with the name", fmt.Errorf("%w: brands_org_id_slug_key", ErrConflict), "brands_org_id_slug_key", false},
		{"nil", nil, "brands_org_id_slug_key", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ConflictOn(tt.err, tt.on); got != tt.want {
				t.Errorf("ConflictOn(%v, %q) = %t, want %t", tt.err, tt.on, got, tt.want)
			}
		})
	}
}
