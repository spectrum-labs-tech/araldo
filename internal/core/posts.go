// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
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
	slotSearchDays       = 56
	maxMetadataKeys      = 50
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

	useSlot := false
	switch at := strings.TrimSpace(in.PublishAt); at {
	case "", "now":
		p.PublishAt = now
	case "next_slot":
		useSlot = true
	default:
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return nil, apperr.Invalid("publish_at_invalid", "publish_at", `publish_at is "now", "next_slot" or an RFC 3339 time.`)
		}
		if t.After(now.Add(maxScheduleAhead)) {
			return nil, apperr.Invalid("publish_at_invalid", "publish_at", "Posts can be scheduled at most a year ahead.")
		}
		p.PublishAt = maxTime(t, now)
	}

	var created *model.Post
	for try := 0; try < 5; try++ {
		if useSlot {
			slot, err := s.nextSlot(ctx, pl.brand, a.Livemode, now, try)
			if err != nil {
				return nil, err
			}
			p.PublishAt, p.SlotAt = slot, &slot
		}
		p.PublishBy = p.PublishAt.Add(DefaultPublishWindow)
		if in.PublishBy != nil {
			if !in.PublishBy.After(p.PublishAt) {
				return nil, apperr.Invalid("publish_by_invalid", "publish_by", "publish_by must be after publish_at.")
			}
			p.PublishBy = *in.PublishBy
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
		s.wake(ctx, p.PublishAt)
		return created, nil
	}
	return nil, apperr.Conflict("slot_contention", "Could not reserve a publishing slot; try again.")
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

// nextSlot finds the brand's earliest free weekly slot after now, skipping
// skip taken ones (for retries after a race).
func (s *Service) nextSlot(ctx context.Context, b *model.Brand, livemode bool, now time.Time, skip int) (time.Time, error) {
	slots, err := s.store.Slots(ctx, b.OrgID, b.ID)
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
			taken, err := s.store.SlotTaken(ctx, b.ID, livemode, t)
			if err != nil {
				return time.Time{}, err
			}
			if taken {
				continue
			}
			if skip > 0 {
				skip--
				continue
			}
			return t.UTC(), nil
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
	return s.store.Posts(ctx, a.OrgID, a.Livemode, f, page)
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
			return apperr.Conflict("post_not_cancelable", "Nothing left to cancel: every target has published, failed or is publishing now.")
		}
		out, err = s.refreshPost(ctx, tx, a.OrgID, p.ID, a.RequestID, model.PostCanceled)
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "post.cancel", id.Format(id.Post, p.ID), map[string]any{"targets": n})
	})
	return out, err
}

// ReviewPost approves or rejects a post waiting for approval.
func (s *Service) ReviewPost(ctx context.Context, a Actor, postID uuid.UUID, approve bool, note string) (*model.Post, error) {
	if err := a.require(PermPostsApprove); err != nil {
		return nil, err
	}
	var out *model.Post
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		p, err := s.lockPost(ctx, tx, a, postID)
		if err != nil {
			return err
		}
		if p.Status != model.PostPendingApproval {
			return apperr.Conflict("post_not_pending", "This post is not waiting for approval.")
		}
		now := s.Now()
		status, action := model.PostScheduled, "post.approved"
		if !approve {
			status, action = model.PostRejected, "post.rejected"
			if _, err := tx.CancelOpenTargets(ctx, a.OrgID, p.ID); err != nil {
				return err
			}
		} else if err := tx.ReleaseHeldTargets(ctx, a.OrgID, p.ID); err != nil {
			return err
		}
		if err := tx.ReviewPost(ctx, a.OrgID, p.ID, *a.UserID, status, strings.TrimSpace(note), now); err != nil {
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
	if err == nil && approve {
		s.wake(ctx, out.PublishAt)
	}
	return out, err
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
