// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

func TestKeyScopes(t *testing.T) {
	t.Parallel()
	kid := uuid.New()
	key := func(scopes ...string) Actor { return Actor{KeyID: &kid, Scopes: scopes} }
	tests := []struct {
		name string
		a    Actor
		p    Permission
		want bool
	}{
		{"full access posts", key(), PermPostsWrite, true},
		{"full access is not admin", key(), PermKeysWrite, false},
		{"full access cannot approve", key(), PermPostsApprove, false},
		{"full access cannot read audit", key(), PermAuditRead, false},
		{"listed admin scope", key("keys:write"), PermKeysWrite, true},
		{"listing an admin scope ends full access", key("keys:write"), PermPostsWrite, false},
		{"restricted", key("posts:read"), PermPostsRead, true},
		{"restricted lacks write", key("posts:read"), PermPostsWrite, false},
		{"members are people only", key("members:write"), PermMembersWrite, false},
		{"org is people only", key(), PermOrgWrite, false},
		{"admin member approves", Actor{Role: model.RoleAdmin}, PermPostsApprove, true},
		{"admin member reads audit", Actor{Role: model.RoleAdmin}, PermAuditRead, true},
		{"editor cannot read audit", Actor{Role: model.RoleEditor}, PermAuditRead, false},
	}
	for _, tt := range tests {
		if got := tt.a.Can(tt.p); got != tt.want {
			t.Errorf("%s: Can(%s) = %v, want %v", tt.name, tt.p, got, tt.want)
		}
	}
}

func TestCheckGrant(t *testing.T) {
	t.Parallel()
	kid, brand, other := uuid.New(), uuid.New(), uuid.New()
	creator := Actor{KeyID: &kid, Scopes: []string{"posts:read", "posts:write", "keys:write"}, BrandID: &brand}
	tests := []struct {
		name  string
		in    APIKeyInput
		codes []string
	}{
		{"a subset for its brand", APIKeyInput{Scopes: []string{"posts:read"}, BrandID: &brand}, nil},
		{"no scopes means all, which it lacks", APIKeyInput{BrandID: &brand}, []string{"scope_not_held"}},
		{"a scope it lacks", APIKeyInput{Scopes: []string{"channels:write"}, BrandID: &brand}, []string{"scope_not_held"}},
		{"an admin scope", APIKeyInput{Scopes: []string{"keys:write"}, BrandID: &brand}, []string{"scope_not_grantable"}},
		{"another brand", APIKeyInput{Scopes: []string{"posts:read"}, BrandID: &other}, []string{"brand_required"}},
		{"every brand", APIKeyInput{Scopes: []string{"posts:read"}}, []string{"brand_required"}},
		{"the other mode", APIKeyInput{Scopes: []string{"posts:read"}, BrandID: &brand, Livemode: true}, []string{"livemode_mismatch"}},
	}
	for _, tt := range tests {
		var ps apperr.Problems
		checkGrant(creator, tt.in, &ps)
		var codes []string
		seen := map[string]bool{}
		for _, p := range ps {
			if !seen[p.Code] {
				codes = append(codes, p.Code)
				seen[p.Code] = true
			}
		}
		if len(codes) != len(tt.codes) || len(codes) > 0 && codes[0] != tt.codes[0] {
			t.Errorf("%s: problems %v, want %v", tt.name, codes, tt.codes)
		}
	}
}
