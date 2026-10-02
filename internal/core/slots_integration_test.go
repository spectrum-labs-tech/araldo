// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// slotWorld is a world whose clock starts on a Monday at 08:00 in the
// brand's zone (Denver, weekdays at 09:00 and 13:00), years ahead so no
// other test's publisher finds these posts due.
type slotWorld struct {
	*world
	start, clock time.Time
	loc          *time.Location
}

func newSlotWorld(t *testing.T) *slotWorld {
	t.Helper()
	loc := mustLoc(t, "America/Denver")
	start := time.Date(2035, 1, 1, 8, 0, 0, 0, loc)
	for start.Weekday() != time.Monday {
		start = start.AddDate(0, 0, 1)
	}
	w := &slotWorld{world: newWorld(t), start: start, clock: start, loc: loc}
	w.s.Now = func() time.Time { return w.clock }
	return w
}

// at is day days after the first Monday, at hour in Denver.
func (w *slotWorld) at(day, hour int) time.Time {
	return time.Date(w.start.Year(), w.start.Month(), w.start.Day()+day, hour, 0, 0, 0, w.loc).UTC()
}

func (w *slotWorld) setBrand(t *testing.T, policy model.ApprovalPolicy, slots *[]model.Slot) {
	t.Helper()
	if _, err := w.s.UpdateBrand(t.Context(), w.owner, w.brand.ID, core.BrandInput{Name: w.brand.Name, Timezone: w.brand.Timezone,
		ApprovalPolicy: policy, Slots: slots}); err != nil {
		t.Fatal(err)
	}
}

func (w *slotWorld) post(t *testing.T, publishAt string, by *time.Time) *model.Post {
	t.Helper()
	p, err := w.s.CreatePost(t.Context(), w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "slot"},
		PublishAt: publishAt, PublishBy: by})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (w *slotWorld) approve(t *testing.T, p *model.Post) *model.Post {
	t.Helper()
	got, err := w.s.ReviewPost(t.Context(), w.owner, p.ID, true, "")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// wantAt checks a post's time, deadline and slot, and its targets'.
func wantAt(t *testing.T, p *model.Post, at, by *time.Time, slot bool) {
	t.Helper()
	same := func(a, b *time.Time) bool { return (a == nil) == (b == nil) && (a == nil || a.Equal(*b)) }
	if !same(p.PublishAt, at) || !same(p.PublishBy, by) || p.Slotted() != slot || (p.SlotAt != nil && !same(p.SlotAt, p.PublishAt)) {
		t.Fatalf("post at %v by %v slot %v (holds %v); want at %v by %v slot %t", p.PublishAt, p.PublishBy, p.Slotted(), p.SlotAt, at, by, slot)
	}
	for _, tg := range p.Targets {
		if !same(tg.NextAttemptAt, at) || !same(tg.PublishBy, by) {
			t.Fatalf("target at %v by %v; want %v, %v", tg.NextAttemptAt, tg.PublishBy, at, by)
		}
	}
}

func ptr(t time.Time) *time.Time { return &t }

func TestPendingPostsTakeSlotsWhenApproved(t *testing.T) {
	t.Parallel()
	w := newSlotWorld(t)
	w.setBrand(t, model.ApprovalAll, nil)
	first := w.post(t, "next_slot", nil)
	second := w.post(t, "next_slot", nil)
	if first.Status != model.PostPendingApproval || first.Targets[0].Status != model.TargetHeld {
		t.Fatalf("pending post: %s / %s", first.Status, first.Targets[0].Status)
	}
	wantAt(t, first, nil, nil, true)

	// A pending post holds nothing, so a post that needs no approval gets
	// the first slot.
	w.setBrand(t, model.ApprovalNone, nil)
	direct := w.post(t, "next_slot", nil)
	wantAt(t, direct, ptr(w.at(0, 9)), ptr(w.at(1, 9)), true)

	// Slots go in approval order, not creation order.
	got := w.approve(t, second)
	if got.Status != model.PostScheduled || got.Targets[0].Status != model.TargetQueued {
		t.Fatalf("approved: %s / %s", got.Status, got.Targets[0].Status)
	}
	wantAt(t, got, ptr(w.at(0, 13)), ptr(w.at(1, 13)), true)
	wantAt(t, w.approve(t, first), ptr(w.at(1, 9)), ptr(w.at(2, 9)), true)

	// Explicit times are unchanged by approval.
	w.setBrand(t, model.ApprovalAll, nil)
	timed := w.post(t, w.at(3, 10).Format(time.RFC3339), nil)
	wantAt(t, timed, ptr(w.at(3, 10)), ptr(w.at(4, 10)), false)
	wantAt(t, w.approve(t, timed), ptr(w.at(3, 10)), ptr(w.at(4, 10)), false)
}

func TestLateApprovalGetsAFutureSlot(t *testing.T) {
	t.Parallel()
	w := newSlotWorld(t)
	w.setBrand(t, model.ApprovalAll, nil)
	p := w.post(t, "next_slot", nil)
	// Reviewed the next day at 10:00, past three slots and a day after it
	// was created: it neither expired nor takes a slot in the past.
	w.clock = time.Date(w.clock.Year(), w.clock.Month(), w.clock.Day()+1, 10, 0, 0, 0, w.loc)
	if got, err := w.s.Post(t.Context(), w.owner, p.ID); err != nil || got.Status != model.PostPendingApproval || got.Targets[0].PublishBy != nil {
		t.Fatalf("waiting: %v %+v", err, got)
	}
	wantAt(t, w.approve(t, p), ptr(w.at(1, 13)), ptr(w.at(2, 13)), true)
}

func TestApprovalFailsWithoutAFreeSlot(t *testing.T) {
	t.Parallel()
	w := newSlotWorld(t)
	// One slot a week: eight in the 56 days searched.
	w.setBrand(t, model.ApprovalNone, &[]model.Slot{{Weekday: time.Monday, MinuteOfDay: 9 * 60}})
	for range 8 {
		w.post(t, "next_slot", nil)
	}
	w.setBrand(t, model.ApprovalAll, nil)
	full := w.post(t, "next_slot", nil)
	// The caller's deadline bounds the search.
	bounded := w.post(t, "next_slot", ptr(w.at(0, 12)))
	wantAt(t, bounded, nil, ptr(w.at(0, 12)), true)

	tests := []struct {
		post *model.Post
		code string
	}{
		{full, "slots_full"},
		{bounded, "no_slot_before_publish_by"},
	}
	for _, tt := range tests {
		_, err := w.s.ReviewPost(t.Context(), w.owner, tt.post.ID, true, "")
		if kind(err) != apperr.KindConflict || !hasProblem(err, tt.code) {
			t.Fatalf("approving: %v, want %s", err, tt.code)
		}
		got, err := w.s.Post(t.Context(), w.owner, tt.post.ID)
		if err != nil || got.Status != model.PostPendingApproval || got.ReviewedAt != nil {
			t.Fatalf("after a failed approval: %v %s", err, got.Status)
		}
		wantAt(t, got, nil, tt.post.PublishBy, true)
	}

	if _, err := w.s.CreatePost(t.Context(), w.owner, core.PostInput{BrandID: w.brand.ID, Content: &model.Content{Body: "x"},
		PublishAt: "next_slot", PublishBy: ptr(w.clock.Add(-time.Minute))}); !hasProblem(err, "publish_by_invalid") {
		t.Fatalf("a deadline in the past: %v", err)
	}
}

func TestRacingApprovalsGetDistinctSlots(t *testing.T) {
	t.Parallel()
	w := newSlotWorld(t)
	w.setBrand(t, model.ApprovalAll, nil)
	var posts []*model.Post
	for range 4 {
		posts = append(posts, w.post(t, "next_slot", nil))
	}
	var wg sync.WaitGroup
	got := make([]*model.Post, len(posts))
	errs := make([]error, len(posts))
	for i, p := range posts {
		wg.Go(func() { got[i], errs[i] = w.s.ReviewPost(t.Context(), w.owner, p.ID, true, "") })
	}
	wg.Wait()
	seen := map[time.Time]bool{}
	for i, p := range got {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if seen[p.PublishAt.UTC()] {
			t.Fatalf("slot %s given twice", p.PublishAt)
		}
		seen[p.PublishAt.UTC()] = true
	}
	for _, want := range []time.Time{w.at(0, 9), w.at(0, 13), w.at(1, 9), w.at(1, 13)} {
		if !seen[want] {
			t.Fatalf("slots %v leave out %s", seen, want)
		}
	}
}

func TestReschedule(t *testing.T) {
	t.Parallel()
	w := newSlotWorld(t)
	w.setBrand(t, model.ApprovalNone, nil)
	ctx := t.Context()
	move := func(p *model.Post, in core.RescheduleInput) ([]*model.Post, error) {
		return w.s.ReschedulePost(ctx, w.owner, p.ID, in)
	}
	mustMove := func(p *model.Post, in core.RescheduleInput) *model.Post {
		t.Helper()
		moved, err := move(p, in)
		if err != nil {
			t.Fatal(err)
		}
		return moved[0]
	}
	a := w.post(t, "next_slot", nil) // Monday 09:00
	b := w.post(t, "next_slot", nil) // Monday 13:00

	// To a time that is not a slot: it gives its slot up.
	a = mustMove(a, core.RescheduleInput{PublishAt: w.at(2, 11).Format(time.RFC3339)})
	wantAt(t, a, ptr(w.at(2, 11)), ptr(w.at(3, 11)), false)
	// Its old slot is free again.
	c := w.post(t, "next_slot", nil)
	wantAt(t, c, ptr(w.at(0, 9)), ptr(w.at(1, 9)), true)

	// To a slot another post holds: refused, naming the holder.
	_, err := move(a, core.RescheduleInput{PublishAt: w.at(0, 13).Format(time.RFC3339)})
	if !hasProblem(err, "slot_taken") || kind(err) != apperr.KindConflict {
		t.Fatalf("moving onto a held slot: %v", err)
	}
	var ae *apperr.Error
	if !errors.As(err, &ae) || len(ae.Problems) != 1 || ae.Problems[0].Detail["post"] != id.Format(id.Post, b.ID) {
		t.Fatalf("slot_taken names %+v, want %s", ae, id.Format(id.Post, b.ID))
	}
	// To a free slot, with a deadline: it takes the slot.
	deadline := w.at(1, 15)
	a = mustMove(a, core.RescheduleInput{PublishAt: w.at(1, 13).Format(time.RFC3339), PublishBy: &deadline})
	wantAt(t, a, ptr(w.at(1, 13)), &deadline, true)
	// next_slot when it already holds the earliest free one keeps it.
	wantAt(t, mustMove(c, core.RescheduleInput{PublishAt: "next_slot"}), ptr(w.at(0, 9)), ptr(w.at(1, 9)), true)

	// Swapping two slot holders trades times, deadlines and slots.
	moved, err := move(a, core.RescheduleInput{SwapWith: &b.ID})
	if err != nil || len(moved) != 2 {
		t.Fatalf("swap: %v", err)
	}
	wantAt(t, moved[0], ptr(w.at(0, 13)), ptr(w.at(1, 13)), true)
	wantAt(t, moved[1], ptr(w.at(1, 13)), &deadline, true)
	// And a slot holder with a post at a plain time.
	free := w.post(t, w.at(4, 7).Format(time.RFC3339), nil)
	moved, err = move(c, core.RescheduleInput{SwapWith: &free.ID})
	if err != nil {
		t.Fatal(err)
	}
	wantAt(t, moved[0], ptr(w.at(4, 7)), ptr(w.at(5, 7)), false)
	wantAt(t, moved[1], ptr(w.at(0, 9)), ptr(w.at(1, 9)), true)

	// Approval is kept; a pending post can go back to waiting for a slot.
	w.setBrand(t, model.ApprovalAll, nil)
	pending := w.post(t, "next_slot", nil)
	approved := w.approve(t, w.post(t, "next_slot", nil))
	got := mustMove(approved, core.RescheduleInput{PublishAt: w.at(6, 10).Format(time.RFC3339)})
	if got.Status != model.PostScheduled || got.ReviewedAt == nil || got.Targets[0].Status != model.TargetQueued {
		t.Fatalf("moving an approved post: %s, reviewed %v, target %s", got.Status, got.ReviewedAt, got.Targets[0].Status)
	}
	pending = mustMove(pending, core.RescheduleInput{PublishAt: w.at(3, 9).Format(time.RFC3339)})
	wantAt(t, pending, ptr(w.at(3, 9)), ptr(w.at(4, 9)), true)
	pending = mustMove(pending, core.RescheduleInput{PublishAt: "next_slot"})
	if pending.Status != model.PostPendingApproval {
		t.Fatalf("pending post moved: %s", pending.Status)
	}
	wantAt(t, pending, nil, nil, true)
	// It has no time to swap.
	if _, err := move(pending, core.RescheduleInput{SwapWith: &approved.ID}); kind(err) != apperr.KindConflict {
		t.Fatalf("swapping a post with no time: %v", err)
	}

	canceled, err := w.s.CancelPost(ctx, w.owner, w.post(t, w.at(9, 9).Format(time.RFC3339), nil).ID)
	if err != nil {
		t.Fatal(err)
	}
	refused := map[string]core.RescheduleInput{
		"post_not_movable":   {PublishAt: "now"},
		"reschedule_invalid": {},
	}
	for code, in := range refused {
		if _, err := move(canceled, in); !hasProblem(err, code) {
			t.Errorf("%s: %v", code, err)
		}
	}
	if _, err := move(a, core.RescheduleInput{SwapWith: &a.ID}); !hasProblem(err, "reschedule_invalid") {
		t.Errorf("swapping with itself: %v", err)
	}
	if _, err := move(a, core.RescheduleInput{SwapWith: ptrID(uuid.New())}); kind(err) != apperr.KindNotFound {
		t.Errorf("swapping with a missing post: %v", err)
	}
}

func ptrID(u uuid.UUID) *uuid.UUID { return &u }
