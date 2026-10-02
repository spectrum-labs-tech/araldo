// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"

	contract "github.com/spectrum-labs-tech/araldo/api"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Posts render as the contract says, including a next_slot post that has
// no time until it is approved (ADR 0022).
func TestPostViewsMatchContract(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["Post"].Value
	at := time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)
	by := at.Add(core.DefaultPublishWindow)
	post := func(status model.PostStatus, at, by, slot *time.Time, target model.TargetStatus) *model.Post {
		return &model.Post{ID: uuid.New(), BrandID: uuid.New(), Status: status, Content: &model.Content{Body: "x"}, PublishAt: at, PublishBy: by,
			SlotAt: slot, ApprovalNeeded: status == model.PostPendingApproval,
			Targets: []model.Target{{ID: uuid.New(), Status: target, NextAttemptAt: at, PublishBy: by, Parts: []string{"x"}}}}
	}
	tests := []struct {
		name     string
		post     *model.Post
		wantAt   any
		wantSlot bool
	}{
		{"waiting for a slot", post(model.PostPendingApproval, nil, nil, nil, model.TargetHeld), nil, true},
		{"waiting for a slot before a deadline", post(model.PostPendingApproval, nil, &by, nil, model.TargetHeld), nil, true},
		{"holding a slot", post(model.PostScheduled, &at, &by, &at, model.TargetQueued), "2026-10-05T15:00:00Z", true},
		{"at a time", post(model.PostScheduled, &at, &by, nil, model.TargetQueued), "2026-10-05T15:00:00Z", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(core.ViewPost(tt.post))
			if err != nil {
				t.Fatal(err)
			}
			var v map[string]any
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			if err := schema.VisitJSON(v); err != nil {
				t.Fatalf("does not match the contract: %v\n%s", err, raw)
			}
			if at, present := v["publish_at"]; !present || at != tt.wantAt || v["slot"] != tt.wantSlot {
				t.Fatalf("publish_at %v (present %t), slot %v; want %v, %t", at, present, v["slot"], tt.wantAt, tt.wantSlot)
			}
		})
	}
}
