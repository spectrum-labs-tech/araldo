// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// TestDeleteOrg checks that deleting an org takes everything in it, its
// files in object storage and its data key too, keeps its people and the
// audit record, and leaves other orgs alone; and that it takes an owner,
// in sudo mode, typing the org's name.
func TestDeleteOrg(t *testing.T) {
	t.Parallel()
	blobs := &memBlobs{objects: map[string][]byte{}}
	w := newWorld(t, withBlobs(blobs))
	other := newWorld(t)
	ctx := t.Context()
	code := func(err error) string { return apperr.As(err).Code }

	m, err := w.s.CreateMedia(ctx, w.owner, core.MediaInput{BrandID: w.brand.ID, Data: pngOf(t, 8, 8), Alt: "x"})
	if err != nil || blobs.len() != 1 {
		t.Fatalf("media in the bucket: %d, %v", blobs.len(), err)
	}
	if _, err := w.s.CreatePost(ctx, w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "goodbye"}, Media: []uuid.UUID{m.ID}}); err != nil {
		t.Fatal(err)
	}
	// The endpoint's signing secret is sealed with the org's data key.
	if _, _, err := w.s.CreateEndpoint(ctx, w.owner, core.EndpointInput{URL: "https://receiver.example/hooks", EventTypes: []string{"post.created"}}); err != nil {
		t.Fatal(err)
	}

	stale := *w.session
	stale.SudoUntil = nil
	if err := w.s.DeleteOrg(ctx, w.owner, &stale, w.org.Name); code(err) != "reauthentication_required" {
		t.Fatalf("without sudo: %v", err)
	}
	if err := w.s.DeleteOrg(ctx, w.owner, w.session, "not the name"); code(err) != "confirm_mismatch" {
		t.Fatalf("a wrong name: %v", err)
	}
	editor, err := w.s.AddMember(ctx, w.owner, w.session, "editor-"+w.user.Email, model.RoleEditor, "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	ed, _, err := w.s.MemberActor(ctx, editor.ID, w.org.ID, false, "req_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.DeleteOrg(ctx, ed, w.session, w.org.Name); apperr.As(err).Kind != apperr.KindForbidden {
		t.Fatalf("an editor: %v", err)
	}

	if err := w.s.DeleteOrg(ctx, w.owner, w.session, w.org.Name); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	st := open(t)
	var orgs, keys, audits int
	if err := st.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM orgs WHERE id = $1), (SELECT count(*) FROM data_keys WHERE scope = $1),
		(SELECT count(*) FROM audit_events WHERE org_id = $1 AND action = 'org.delete')`, w.org.ID).Scan(&orgs, &keys, &audits); err != nil {
		t.Fatal(err)
	}
	if orgs != 0 || keys != 0 || audits != 1 {
		t.Fatalf("after deleting: %d orgs, %d data keys, %d audit records", orgs, keys, audits)
	}
	if blobs.len() != 0 {
		t.Fatalf("%d media files left in the bucket", blobs.len())
	}
	// People stay; the other org is untouched.
	if _, err := w.s.User(ctx, w.user.ID); err != nil {
		t.Fatalf("the owner's account: %v", err)
	}
	if _, err := other.s.Org(ctx, other.owner); err != nil {
		t.Fatalf("another org: %v", err)
	}
	if _, err := other.s.Brands(ctx, other.owner); err != nil {
		t.Fatalf("another org's brands: %v", err)
	}
}
