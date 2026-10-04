// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/sandbox"
	"github.com/spectrum-labs-tech/araldo/internal/store"
	"github.com/spectrum-labs-tech/araldo/internal/tmpl"
	"github.com/spectrum-labs-tech/araldo/internal/utm"
)

// Scheduling defaults (ADR 0011).
const (
	DefaultPublishWindow = 24 * time.Hour
	maxScheduleAhead     = 366 * 24 * time.Hour
	// pastTolerance is how far behind a publish_at may be and still mean
	// "now", for a caller whose clock or request runs a little late; older
	// is a mistake (2020 for 2027) that would otherwise publish at once.
	pastTolerance   = 15 * time.Minute
	slotSearchDays  = 56
	maxMetadataKeys = 50
)

// PostInput creates (or previews) a post.
type PostInput struct {
	BrandID uuid.UUID
	// Template is "key", "key@version" or a template ID; or use Content.
	Template string
	Data     json.RawMessage
	Content  *model.Content
	// Channels to publish to; empty means every active channel of the
	// brand in the actor's mode.
	Channels []uuid.UUID
	// PublishAt is "now", "next_slot" or an RFC 3339 time.
	PublishAt string
	PublishBy *time.Time
	Metadata  map[string]string
	// Media are attached in order (ADR 0017).
	Media []uuid.UUID
}

// plan is a validated, rendered post, ready to store.
type plan struct {
	brand    *model.Brand
	template *model.Template
	version  *model.TemplateVersion
	channels []*model.Channel
	media    []*model.Media
	renders  []Rendition
}

func location(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}

// prepare validates in and renders it for every channel. postID goes into
// the links' utm_content when the brand tags links.
func (s *Service) prepare(ctx context.Context, a Actor, in *PostInput, postID uuid.UUID) (*plan, error) {
	if err := a.require(PermPostsWrite); err != nil {
		return nil, err
	}
	b, err := s.Brand(ctx, a, in.BrandID)
	if err != nil {
		return nil, err
	}
	pl := &plan{brand: b}
	var ps apperr.Problems

	// Content source.
	var compiled *tmpl.Compiled
	switch {
	case in.Template != "" && in.Content != nil:
		ps.Add("content_conflict", "content", "Send either a template or content, not both.")
	case in.Template != "":
		pl.template, pl.version, err = s.TemplateByRef(ctx, a, b.ID, in.Template)
		if err != nil {
			return nil, err
		}
		if compiled, err = tmpl.Compile(sourceOf(pl.version)); err != nil {
			return nil, err
		}
		if err := compiled.Validate(in.Data); err != nil {
			return nil, err
		}
	case in.Content != nil:
		if strings.TrimSpace(in.Content.Body) == "" && len(in.Content.Parts) == 0 && len(in.Media) == 0 {
			ps.Add("content_empty", "content.body", "Content needs a body, parts or media.")
		}
		for p, f := range in.Content.Fit {
			if !f.Valid() {
				ps.Add("fit_invalid", "content.fit."+string(p), "Fit must be error, truncate or thread.")
			}
		}
	default:
		ps.Add("content_missing", "template", "Send a template (with data) or content.")
	}

	pl.media = s.postMedia(ctx, a, b.ID, in.Media, &ps)

	// Metadata.
	if len(in.Metadata) > maxMetadataKeys {
		ps.Add("metadata_invalid", "metadata", "Metadata holds at most %d keys.", maxMetadataKeys)
	}
	for k, v := range in.Metadata {
		if k == "" || len(k) > 40 || len(v) > 500 {
			ps.Add("metadata_invalid", "metadata."+k, "Metadata keys are 1-40 characters and values at most 500.")
		}
	}
	if sim, ok := in.Metadata["araldo_simulate"]; ok {
		switch {
		case a.Livemode:
			ps.Add("simulation_in_live_mode", "metadata.araldo_simulate", "Failures can only be simulated in test mode.")
		case !slices.Contains(sandbox.Simulations, sim):
			ps.Add("simulation_invalid", "metadata.araldo_simulate", "Simulations: %s.", strings.Join(sandbox.Simulations, ", "))
		}
	}

	// Channels.
	if len(in.Channels) == 0 {
		all, err := s.store.Channels(ctx, a.OrgID, a.Livemode, &b.ID)
		if err != nil {
			return nil, err
		}
		for _, ch := range all {
			if ch.Status == model.ChannelActive {
				pl.channels = append(pl.channels, ch)
			}
		}
		if len(pl.channels) == 0 {
			mode := "test"
			if a.Livemode {
				mode = "live"
			}
			ps.Add("no_channels", "channels", "Brand %s has no active %s-mode channels.", b.Name, mode)
		}
	}
	seen := map[uuid.UUID]bool{}
	for i, cid := range in.Channels {
		if seen[cid] {
			continue
		}
		seen[cid] = true
		ch, err := s.store.Channel(ctx, a.OrgID, cid)
		param := fmt.Sprintf("channels[%d]", i)
		switch {
		case err != nil || ch.Livemode != a.Livemode:
			ps.Add("channel_missing", param, "No such channel %s in this mode.", id.Format(id.Channel, cid))
		case ch.BrandID != b.ID:
			ps.Add("channel_other_brand", param, "Channel %s belongs to another brand.", id.Format(id.Channel, cid))
		case ch.Status != model.ChannelActive:
			ps.Add("channel_inactive", param, "Channel %s is %s.", ch.DisplayName, ch.Status)
		default:
			pl.channels = append(pl.channels, ch)
		}
	}
	if err := ps.Err("The post is not valid."); err != nil {
		return nil, err
	}

	// Render for each channel, with its platform's rules. Links are tagged
	// before the rules measure them.
	loc := location(b.Timezone)
	campaign := ""
	if pl.template != nil {
		campaign = pl.template.Key
	}
	media := ruleMedia(pl.media)
	for _, ch := range pl.channels {
		rp := ch.RulesProvider()
		rules, _ := platform.RulesFor(rp)
		rules = rules.ForMedia(len(media))
		tag := func(text string) string {
			return utm.Tag(text, b.UTMDomains, utm.Params{Source: string(ch.Provider), Medium: "social", Campaign: campaign,
				Content: id.Format(id.Post, postID)})
		}
		var parts []string
		switch {
		case compiled != nil:
			text, err := compiled.Render(rp, in.Data, loc)
			if err != nil {
				return nil, err
			}
			parts = rules.Split(tag(text), compiled.FitFor(rp))
		case len(in.Content.Parts) > 0 && in.Content.Overrides[rp] == "":
			parts = rules.Split(tag(strings.Join(in.Content.Parts, platform.ThreadBreak)), fitOf(in.Content, rp))
		default:
			text := in.Content.Body
			if o := in.Content.Overrides[rp]; o != "" {
				text = o
			}
			parts = rules.Split(tag(text), fitOf(in.Content, rp))
		}
		r := rendition(rules, parts, id.Format(id.Channel, ch.ID), media)
		pl.renders = append(pl.renders, r)
		for _, v := range r.Violations {
			detail := map[string]any{"channel": r.Channel, "provider": rp, "part": v.Part, "length": v.Length, "limit": v.Limit}
			if v.Media > 0 {
				detail["media"] = id.Format(id.Media, pl.media[v.Media-1].ID)
			}
			ps = append(ps, apperr.Problem{Code: v.Code, Param: "channels." + r.Channel, Message: ch.DisplayName + ": " + v.Message, Detail: detail})
		}
	}
	return pl, ps.Err("The content does not fit every channel's rules.")
}

func fitOf(c *model.Content, p platform.Provider) platform.Fit {
	if f, ok := c.Fit[p]; ok {
		return f
	}
	return platform.FitError
}

// PreviewPost renders a post for each channel without saving it. Rule
// violations are reported in the renditions rather than as an error, so an
// agent can read them and fix its text (ADR 0001).
func (s *Service) PreviewPost(ctx context.Context, a Actor, in PostInput) ([]Rendition, error) {
	// The zero ID has a real ID's length, so a preview measures what
	// publishing will.
	pl, err := s.prepare(ctx, a, &in, uuid.Nil)
	if pl != nil && len(pl.renders) > 0 {
		return pl.renders, nil
	}
	return nil, err
}

// CreatePost validates, renders and schedules a post (ADR 0011). The text
// is frozen now: what was previewed is what gets published.
func (s *Service) CreatePost(ctx context.Context, a Actor, in PostInput) (*model.Post, error) {
	postID := id.New()
	pl, err := s.prepare(ctx, a, &in, postID)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	p := &model.Post{ID: postID, OrgID: a.OrgID, BrandID: pl.brand.ID, Livemode: a.Livemode, Data: in.Data, Content: in.Content,
		Metadata: in.Metadata, CreatedByUser: a.UserID, CreatedByKey: a.KeyID, Media: pl.media}
	if pl.template != nil {
		p.TemplateID, p.TemplateVersion = &pl.template.ID, &pl.version.Version
		p.Content = nil
	}
	p.ApprovalNeeded = approvalNeeded(pl.brand.ApprovalPolicy, pl.template, a)

	at, useSlot, err := parsePublishAt(in.PublishAt, now)
	if err != nil {
		return nil, err
	}
	p.PublishAt = at
	if useSlot && p.ApprovalNeeded {
		// It takes its slot when approved (ADR 0022).
		if in.PublishBy != nil && !in.PublishBy.After(now) {
			return nil, apperr.Invalid("publish_by_invalid", "publish_by", "publish_by must be in the future.")
		}
		useSlot, p.PublishBy = false, in.PublishBy
	}

	var created *model.Post
	for try := 0; try < slotTries; try++ {
		if useSlot {
			slot, err := s.nextSlot(ctx, s.store, pl.brand, a.Livemode, now, in.PublishBy)
			if err != nil {
				return nil, err
			}
			p.PublishAt, p.SlotAt = &slot, &slot
		}
		if p.PublishAt != nil {
			if p.PublishBy, err = publishBy(*p.PublishAt, in.PublishBy); err != nil {
				return nil, err
			}
		}
		p.Status = model.PostScheduled
		targetStatus := model.TargetQueued
		if p.ApprovalNeeded {
			p.Status, targetStatus = model.PostPendingApproval, model.TargetHeld
		}
		p.Targets = p.Targets[:0]
		for i, ch := range pl.channels {
			p.Targets = append(p.Targets, model.Target{ID: id.New(), OrgID: a.OrgID, PostID: p.ID, ChannelID: ch.ID, Livemode: a.Livemode,
				Provider: ch.Provider, Parts: pl.renders[i].Parts, Status: targetStatus, NextAttemptAt: p.PublishAt, PublishBy: p.PublishBy,
				ChannelName: ch.DisplayName})
		}
		err := s.store.InTx(ctx, func(tx *store.Store) error {
			if err := tx.CreatePost(ctx, p); err != nil {
				return err
			}
			if err := s.audit(ctx, tx, a, "post.create", id.Format(id.Post, p.ID), nil); err != nil {
				return err
			}
			stored, err := tx.Post(ctx, a.OrgID, p.ID)
			if err != nil {
				return err
			}
			created = stored
			if err := s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, "post.created", ViewPost(stored)); err != nil {
				return err
			}
			if p.ApprovalNeeded {
				return s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, "post.approval_requested", ViewPost(stored))
			}
			return nil
		})
		if useSlot && store.ConflictOn(err, "posts_slot_idx") {
			continue // someone took the slot; try the next
		}
		if err != nil {
			return nil, err
		}
		s.wakeFor(ctx, created)
		return created, nil
	}
	return nil, errSlotContention
}

// slotTries bounds the retries when another post takes the slot first.
const slotTries = 5

var errSlotContention = apperr.Conflict("slot_contention", "Could not reserve a publishing slot; try again.")

// parsePublishAt reads "now", "next_slot" or a time. For next_slot it
// returns no time: the caller finds the slot.
func parsePublishAt(s string, now time.Time) (*time.Time, bool, error) {
	switch s = strings.TrimSpace(s); s {
	case "", "now":
		return &now, false, nil
	case "next_slot":
		return nil, true, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, false, apperr.Invalid("publish_at_invalid", "publish_at", `publish_at is "now", "next_slot" or an RFC 3339 time.`)
	}
	if t.After(now.Add(maxScheduleAhead)) {
		return nil, false, apperr.Invalid("publish_at_invalid", "publish_at", "Posts can be scheduled at most a year ahead.")
	}
	if t.Before(now.Add(-pastTolerance)) {
		return nil, false, apperr.Invalid("publish_at_invalid", "publish_at",
			`publish_at %s is in the past; use "now" to publish right away.`, t.UTC().Format(time.RFC3339))
	}
	t = maxTime(t, now)
	return &t, false, nil
}

// publishBy is the given deadline, which must come after at, or at plus
// the default window.
func publishBy(at time.Time, given *time.Time) (*time.Time, error) {
	if given == nil {
		by := at.Add(DefaultPublishWindow)
		return &by, nil
	}
	if !given.After(at) {
		return nil, apperr.Invalid("publish_by_invalid", "publish_by", "publish_by must be after publish_at.")
	}
	by := *given
	return &by, nil
}

// wakeFor wakes the publisher if the post is due now.
func (s *Service) wakeFor(ctx context.Context, p *model.Post) {
	if p != nil && p.PublishAt != nil && p.Status == model.PostScheduled {
		s.wake(ctx, *p.PublishAt)
	}
}

// approvalNeeded applies the template's override, or else the brand's
// policy (ADR 0004).
func approvalNeeded(policy model.ApprovalPolicy, t *model.Template, a Actor) bool {
	if t != nil {
		switch t.Approval {
		case model.TemplateApprovalRequired:
			return true
		case model.TemplateApprovalNotRequired:
			return false
		}
	}
	switch policy {
	case model.ApprovalAll:
		return true
	case model.ApprovalEditorsAndKey:
		return a.IsKey() || !a.Role.AtLeast(model.RoleAdmin)
	}
	return false
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// nextSlot finds the brand's earliest free weekly slot after now, and
// before by when given. q is the store or a transaction.
func (s *Service) nextSlot(ctx context.Context, q *store.Store, b *model.Brand, livemode bool, now time.Time, by *time.Time) (time.Time, error) {
	slots, err := q.Slots(ctx, b.OrgID, b.ID)
	if err != nil {
		return time.Time{}, err
	}
	if len(slots) == 0 {
		return time.Time{}, apperr.Invalid("no_slots", "publish_at", "Brand %s has no publishing slots; set some or give a time.", b.Name)
	}
	loc := location(b.Timezone)
	earliest := now.Add(time.Minute)
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	for d := 0; d < slotSearchDays; d++ {
		date := day.AddDate(0, 0, d)
		var times []time.Time
		for _, sl := range slots {
			if sl.Weekday == date.Weekday() {
				times = append(times, time.Date(date.Year(), date.Month(), date.Day(), sl.MinuteOfDay/60, sl.MinuteOfDay%60, 0, 0, loc))
			}
		}
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		for _, t := range times {
			if t.Before(earliest) {
				continue
			}
			if by != nil && !t.Before(*by) {
				return time.Time{}, apperr.Conflict("no_slot_before_publish_by", "No free slot of brand %s comes before publish_by.", b.Name)
			}
			holder, err := q.SlotHolder(ctx, b.ID, livemode, t)
			if err != nil {
				return time.Time{}, err
			}
			if holder == nil {
				return t.UTC(), nil
			}
		}
	}
	return time.Time{}, apperr.Conflict("slots_full", "Every slot in the next %d days is taken.", slotSearchDays)
}

// Post returns one of the actor's posts.
func (s *Service) Post(ctx context.Context, a Actor, postID uuid.UUID) (*model.Post, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, err
	}
	p, err := s.store.Post(ctx, a.OrgID, postID)
	if err != nil {
		return nil, notFound(err, "post")
	}
	if p.Livemode != a.Livemode || a.brandAllowed(p.BrandID) != nil {
		return nil, apperr.NotFound("post")
	}
	return p, nil
}

// PostFilter narrows a listing.
type PostFilter = store.PostFilter

// Posts lists posts newest first.
func (s *Service) Posts(ctx context.Context, a Actor, f PostFilter, page store.Page) ([]*model.Post, bool, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, false, err
	}
	if a.BrandID != nil {
		f.BrandID = a.BrandID
	}
	if len(f.Query) > 200 {
		return nil, false, apperr.Invalid("query_too_long", "q", "Search for at most 200 characters.")
	}
	return s.store.Posts(ctx, a.OrgID, a.Livemode, f, page)
}

// PostStatusCounts counts the posts a filter matches, by status, ignoring
// the filter's own status (for the status filter's labels).
func (s *Service) PostStatusCounts(ctx context.Context, a Actor, f PostFilter) (map[model.PostStatus]int, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, err
	}
	if a.BrandID != nil {
		f.BrandID = a.BrandID
	}
	return s.store.PostStatusCounts(ctx, a.OrgID, a.Livemode, f)
}

// CancelPost stops a post's targets that have not been published.
func (s *Service) CancelPost(ctx context.Context, a Actor, postID uuid.UUID) (*model.Post, error) {
	if err := a.require(PermPostsWrite); err != nil {
		return nil, err
	}
	var out *model.Post
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		p, err := s.lockPost(ctx, tx, a, postID)
		if err != nil {
			return err
		}
		n, err := tx.CancelOpenTargets(ctx, a.OrgID, p.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.Conflict("post_not_cancelable", "Nothing left to cancel: every target is already canceled, published, failed or publishing now.")
		}
		out, err = s.refreshPost(ctx, tx, a.OrgID, p.ID, a.RequestID, model.PostCanceled)
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "post.cancel", id.Format(id.Post, p.ID), map[string]any{"targets": n})
	})
	return out, err
}

// ReviewPost approves or rejects a post waiting for approval. Approving a
// next_slot post gives it the brand's next free slot, so slots go in
// approval order (ADR 0022).
func (s *Service) ReviewPost(ctx context.Context, a Actor, postID uuid.UUID, approve bool, note string) (*model.Post, error) {
	if err := a.require(PermPostsApprove); err != nil {
		return nil, err
	}
	for try := 0; try < slotTries; try++ {
		var out *model.Post
		err := s.store.InTx(ctx, func(tx *store.Store) error {
			p, err := s.lockPost(ctx, tx, a, postID)
			if err != nil {
				return err
			}
			if p.Status != model.PostPendingApproval {
				return apperr.Conflict("post_not_pending", "This post is not waiting for approval.")
			}
			if a.IsKey() && p.CreatedByKey != nil && *p.CreatedByKey == *a.KeyID {
				return apperr.Forbidden("A key cannot review a post it created (ADR 0019).")
			}
			now := s.Now()
			status, action := model.PostScheduled, "post.approved"
			if !approve {
				status, action = model.PostRejected, "post.rejected"
				if _, err := tx.CancelOpenTargets(ctx, a.OrgID, p.ID); err != nil {
					return err
				}
			} else {
				if p.PublishAt == nil {
					if err := s.takeSlot(ctx, tx, p, now); err != nil {
						return err
					}
				}
				if err := tx.ReleaseHeldTargets(ctx, a.OrgID, p.ID); err != nil {
					return err
				}
			}
			if err := tx.ReviewPost(ctx, a.OrgID, p.ID, a.UserID, a.KeyID, status, strings.TrimSpace(note), now); err != nil {
				return err
			}
			if out, err = tx.Post(ctx, a.OrgID, p.ID); err != nil {
				return err
			}
			if err := s.audit(ctx, tx, a, action, id.Format(id.Post, p.ID), nil); err != nil {
				return err
			}
			return s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, action, ViewPost(out))
		})
		if approve && store.ConflictOn(err, "posts_slot_idx") {
			continue // another post took the slot; the next try sees it
		}
		if err != nil {
			return nil, err
		}
		s.wakeFor(ctx, out)
		return out, nil
	}
	return nil, errSlotContention
}

// takeSlot gives a post waiting for a slot the brand's next free one, with
// the caller's publish_by as the bound (ADR 0022).
func (s *Service) takeSlot(ctx context.Context, tx *store.Store, p *model.Post, now time.Time) error {
	b, err := tx.Brand(ctx, p.OrgID, p.BrandID)
	if err != nil {
		return err
	}
	slot, err := s.nextSlot(ctx, tx, b, p.Livemode, now, p.PublishBy)
	if err != nil {
		return err
	}
	by, err := publishBy(slot, p.PublishBy)
	if err != nil {
		return err
	}
	return tx.SchedulePost(ctx, p.OrgID, p.ID, &slot, by, &slot)
}

// RescheduleInput moves a post: to PublishAt ("now", "next_slot" or an
// RFC 3339 time), or trading places with SwapWith (ADR 0022).
type RescheduleInput struct {
	PublishAt string
	PublishBy *time.Time
	SwapWith  *uuid.UUID
}

// ReschedulePost moves a post that has not started publishing, or swaps
// it with another. Approval is kept: it changes when, not what.
func (s *Service) ReschedulePost(ctx context.Context, a Actor, postID uuid.UUID, in RescheduleInput) ([]*model.Post, error) {
	if err := a.require(PermPostsWrite); err != nil {
		return nil, err
	}
	switch {
	case (in.SwapWith == nil) == (strings.TrimSpace(in.PublishAt) == ""):
		return nil, apperr.Invalid("reschedule_invalid", "publish_at", "Give publish_at or swap_with.")
	case in.SwapWith != nil && in.PublishBy != nil:
		return nil, apperr.Invalid("reschedule_invalid", "publish_by", "A swap trades deadlines too; leave out publish_by.")
	case in.SwapWith != nil && *in.SwapWith == postID:
		return nil, apperr.Invalid("reschedule_invalid", "swap_with", "A post cannot swap with itself.")
	}
	for try := 0; try < slotTries; try++ {
		var moved []*model.Post
		err := s.store.InTx(ctx, func(tx *store.Store) error {
			var err error
			if in.SwapWith != nil {
				moved, err = s.swapPosts(ctx, tx, a, postID, *in.SwapWith)
			} else {
				moved, err = s.movePost(ctx, tx, a, postID, in)
			}
			if err != nil {
				return err
			}
			for _, p := range moved {
				if err := s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, "post.rescheduled", ViewPost(p)); err != nil {
					return err
				}
			}
			return nil
		})
		if store.ConflictOn(err, "posts_slot_idx") {
			continue // another post took the slot; the next try sees it
		}
		if err != nil {
			return nil, err
		}
		for _, p := range moved {
			s.wakeFor(ctx, p)
		}
		return moved, nil
	}
	return nil, errSlotContention
}

func (s *Service) movePost(ctx context.Context, tx *store.Store, a Actor, postID uuid.UUID, in RescheduleInput) ([]*model.Post, error) {
	p, err := s.lockMovable(ctx, tx, a, postID)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	at, useSlot, err := parsePublishAt(in.PublishAt, now)
	if err != nil {
		return nil, err
	}
	b, err := tx.Brand(ctx, p.OrgID, p.BrandID)
	if err != nil {
		return nil, err
	}
	// Its own slot is free to take again.
	if err := tx.ReleaseSlot(ctx, p.OrgID, p.ID); err != nil {
		return nil, err
	}
	var by, slot *time.Time
	switch {
	case useSlot && p.Status == model.PostPendingApproval:
		// Back to taking its slot when approved.
		if in.PublishBy != nil && !in.PublishBy.After(now) {
			return nil, apperr.Invalid("publish_by_invalid", "publish_by", "publish_by must be in the future.")
		}
		by = in.PublishBy
	case useSlot:
		t, err := s.nextSlot(ctx, tx, b, p.Livemode, now, in.PublishBy)
		if err != nil {
			return nil, err
		}
		at, slot = &t, &t
	default:
		isSlot, err := onSlot(ctx, tx, b, *at)
		if err != nil {
			return nil, err
		}
		if isSlot {
			holder, err := tx.SlotHolder(ctx, b.ID, p.Livemode, *at)
			if err != nil {
				return nil, err
			}
			if holder != nil {
				msg := fmt.Sprintf("Post %s holds that slot; move it or swap with it.", id.Format(id.Post, *holder))
				return nil, &apperr.Error{Kind: apperr.KindConflict, Code: "slot_taken", Message: msg, Param: "publish_at",
					Problems: []apperr.Problem{{Code: "slot_taken", Param: "publish_at", Message: msg, Detail: map[string]any{"post": id.Format(id.Post, *holder)}}}}
			}
			slot = at
		}
	}
	if at != nil {
		if by, err = publishBy(*at, in.PublishBy); err != nil {
			return nil, err
		}
	}
	if err := tx.SchedulePost(ctx, p.OrgID, p.ID, at, by, slot); err != nil {
		return nil, err
	}
	out, err := tx.Post(ctx, p.OrgID, p.ID)
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"from": timeOrSlot(p.PublishAt), "to": timeOrSlot(out.PublishAt)}
	return []*model.Post{out}, s.audit(ctx, tx, a, "post.reschedule", id.Format(id.Post, p.ID), detail)
}

// swapPosts trades two posts' times, deadlines and slots.
func (s *Service) swapPosts(ctx context.Context, tx *store.Store, a Actor, postID, otherID uuid.UUID) ([]*model.Post, error) {
	// Lock in a fixed order, so two opposite swaps cannot deadlock.
	first, second := postID, otherID
	if bytes.Compare(first[:], second[:]) > 0 {
		first, second = second, first
	}
	locked := map[uuid.UUID]*model.Post{}
	for _, pid := range []uuid.UUID{first, second} {
		p, err := s.lockMovable(ctx, tx, a, pid)
		if err != nil {
			return nil, err
		}
		if p.PublishAt == nil {
			return nil, apperr.Conflict("post_unscheduled", "Post %s has no time yet: it takes a slot when approved.", id.Format(id.Post, p.ID))
		}
		locked[pid] = p
	}
	p, o := locked[postID], locked[otherID]
	if p.BrandID != o.BrandID {
		return nil, apperr.Invalid("swap_other_brand", "swap_with", "Only posts of the same brand can swap.")
	}
	// Free one slot first: the slot index allows one holder at a time.
	if err := tx.ReleaseSlot(ctx, p.OrgID, p.ID); err != nil {
		return nil, err
	}
	if err := tx.SchedulePost(ctx, o.OrgID, o.ID, p.PublishAt, p.PublishBy, p.SlotAt); err != nil {
		return nil, err
	}
	if err := tx.SchedulePost(ctx, p.OrgID, p.ID, o.PublishAt, o.PublishBy, o.SlotAt); err != nil {
		return nil, err
	}
	var out []*model.Post
	for _, pid := range []uuid.UUID{postID, otherID} {
		moved, err := tx.Post(ctx, a.OrgID, pid)
		if err != nil {
			return nil, err
		}
		out = append(out, moved)
	}
	return out, s.audit(ctx, tx, a, "post.reschedule", id.Format(id.Post, p.ID), map[string]any{"swap_with": id.Format(id.Post, o.ID)})
}

// lockMovable locks a post that has not started publishing.
func (s *Service) lockMovable(ctx context.Context, tx *store.Store, a Actor, postID uuid.UUID) (*model.Post, error) {
	p, err := s.lockPost(ctx, tx, a, postID)
	if err != nil {
		return nil, err
	}
	if p.Status != model.PostScheduled && p.Status != model.PostPendingApproval {
		return nil, apperr.Conflict("post_not_movable", "Only scheduled posts and posts waiting for approval can be moved.")
	}
	for _, t := range p.Targets {
		if t.Attempts > 0 {
			return nil, apperr.Conflict("post_not_movable", "Publishing has started on post %s.", id.Format(id.Post, p.ID))
		}
	}
	return p, nil
}

// onSlot reports whether t is one of the brand's weekly slots.
func onSlot(ctx context.Context, tx *store.Store, b *model.Brand, t time.Time) (bool, error) {
	slots, err := tx.Slots(ctx, b.OrgID, b.ID)
	if err != nil {
		return false, err
	}
	local := t.In(location(b.Timezone))
	if local.Second() != 0 || local.Nanosecond() != 0 {
		return false, nil
	}
	for _, sl := range slots {
		if sl.Weekday == local.Weekday() && sl.MinuteOfDay == local.Hour()*60+local.Minute() {
			return true, nil
		}
	}
	return false, nil
}

func timeOrSlot(t *time.Time) string {
	if t == nil {
		return "slot at approval"
	}
	return t.UTC().Format(time.RFC3339)
}

// RetryTarget puts a failed or uncertain target back in the queue now. For
// an uncertain one, the person asserts it did not publish (ADR 0011).
func (s *Service) RetryTarget(ctx context.Context, a Actor, targetID uuid.UUID) (*model.Post, error) {
	return s.resolveTarget(ctx, a, targetID, true, "")
}

// MarkTargetPublished records that an uncertain target did publish.
func (s *Service) MarkTargetPublished(ctx context.Context, a Actor, targetID uuid.UUID, permalink string) (*model.Post, error) {
	return s.resolveTarget(ctx, a, targetID, false, permalink)
}

func (s *Service) resolveTarget(ctx context.Context, a Actor, targetID uuid.UUID, retry bool, permalink string) (*model.Post, error) {
	if err := a.require(PermPostsWrite); err != nil {
		return nil, err
	}
	var out *model.Post
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		t, err := tx.Target(ctx, a.OrgID, targetID)
		if err != nil || t.Livemode != a.Livemode {
			return apperr.NotFound("post target")
		}
		p, err := s.lockPost(ctx, tx, a, t.PostID)
		if err != nil {
			return err
		}
		now := s.Now()
		action := "post_target.retry"
		if retry {
			err = tx.RequeueTarget(ctx, a.OrgID, t.ID, now, now.Add(DefaultPublishWindow))
		} else {
			action = "post_target.mark_published"
			err = tx.MarkTargetPublished(ctx, a.OrgID, t.ID, permalink, now)
		}
		if errors.Is(err, store.ErrNotFound) {
			return apperr.Conflict("target_not_resolvable", "Only failed or uncertain targets can be retried or marked published.")
		}
		if err != nil {
			return err
		}
		if err := s.audit(ctx, tx, a, action, id.Format(id.Target, t.ID), nil); err != nil {
			return err
		}
		out, err = s.refreshPost(ctx, tx, a.OrgID, p.ID, a.RequestID, "")
		return err
	})
	if err == nil && retry {
		s.wake(ctx, s.Now())
	}
	return out, err
}

func (s *Service) lockPost(ctx context.Context, tx *store.Store, a Actor, postID uuid.UUID) (*model.Post, error) {
	p, err := tx.PostForUpdate(ctx, a.OrgID, postID)
	if err != nil {
		return nil, notFound(err, "post")
	}
	if p.Livemode != a.Livemode || a.brandAllowed(p.BrandID) != nil {
		return nil, apperr.NotFound("post")
	}
	return p, nil
}

// refreshPost recomputes a post's status from its targets and emits an
// event when it reaches a final state. force sets a status outright (for
// cancel).
func (s *Service) refreshPost(ctx context.Context, tx *store.Store, orgID, postID uuid.UUID, requestID string, force model.PostStatus) (*model.Post, error) {
	p, err := tx.Post(ctx, orgID, postID)
	if err != nil {
		return nil, err
	}
	status := force
	if status == "" {
		status = deriveStatus(p)
	}
	if status == p.Status {
		return p, nil
	}
	if err := tx.SetPostStatus(ctx, orgID, postID, status); err != nil {
		return nil, err
	}
	p.Status = status
	switch status {
	case model.PostPublished, model.PostPartiallyPublished, model.PostFailed, model.PostCanceled:
		if err := s.emit(ctx, tx, orgID, p.Livemode, requestID, "post."+string(status), ViewPost(p)); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// deriveStatus summarizes targets (ADR 0011).
func deriveStatus(p *model.Post) model.PostStatus {
	if p.Status == model.PostRejected {
		return p.Status
	}
	var published, failed, canceled, held, active int
	for _, t := range p.Targets {
		switch t.Status {
		case model.TargetPublished:
			published++
		case model.TargetFailed:
			failed++
		case model.TargetCanceled:
			canceled++
		case model.TargetHeld:
			held++
		case model.TargetPublishing, model.TargetNeedsAttention:
			active++
		}
	}
	n := len(p.Targets)
	switch {
	case held > 0:
		return model.PostPendingApproval
	case published == n:
		return model.PostPublished
	case published+failed+canceled == n:
		switch {
		case published > 0:
			return model.PostPartiallyPublished
		case failed > 0:
			return model.PostFailed
		default:
			return model.PostCanceled
		}
	case active > 0 || published > 0 || failed > 0:
		return model.PostPublishing
	}
	return model.PostScheduled
}

// Wake is signaled when work is due now, so the publisher need not wait
// for its next poll.
func (s *Service) wake(ctx context.Context, at time.Time) {
	if at.After(s.Now().Add(publishPoll)) {
		return
	}
	_, _ = s.store.Pool().Exec(context.WithoutCancel(ctx), "NOTIFY araldo_publish")
}
