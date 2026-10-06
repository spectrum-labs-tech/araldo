// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Engagement (ADR 0018).
const (
	engagementBatch   = 200
	engagementTimeout = 30 * time.Second
	// MaxEngagementWindow bounds a summary's time range.
	MaxEngagementWindow = 366 * 24 * time.Hour
)

// EngagementSchedule is when a target's engagement is read, by the time
// since it was published: most of it arrives in the first day.
var EngagementSchedule = []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour, 3 * 24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour}

// nextReading is the first scheduled reading after now, or nil when the
// schedule is over.
func nextReading(published, now time.Time) *time.Time {
	for _, d := range EngagementSchedule {
		if at := published.Add(d); at.After(now) {
			return &at
		}
	}
	return nil
}

// CollectEngagement reads the engagement of every target due a reading.
func (s *Service) CollectEngagement(ctx context.Context) (int, error) {
	return s.collectEngagement(ctx, nil)
}

// collectEngagement reads one org's due targets, or (orgID nil) every
// org's, a channel at a time.
func (s *Service) collectEngagement(ctx context.Context, orgID *uuid.UUID) (int, error) {
	now := s.Now()
	if !s.engagementResumed.Swap(true) {
		if err := s.resumeEngagement(ctx, orgID, now); err != nil {
			s.engagementResumed.Store(false)
			return 0, err
		}
	}
	due, err := s.store.DueEngagement(ctx, orgID, now, engagementBatch)
	if err != nil {
		return 0, err
	}
	byChannel := map[uuid.UUID][]store.EngagementDue{}
	var order []uuid.UUID
	for _, d := range due {
		if byChannel[d.ChannelID] == nil {
			order = append(order, d.ChannelID)
		}
		byChannel[d.ChannelID] = append(byChannel[d.ChannelID], d)
	}
	n := 0
	for _, chID := range order {
		done, err := s.readChannelEngagement(ctx, now, byChannel[chID])
		n += done
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// resumeEngagement schedules the posts marked unsupported on platforms
// whose engagement this version reads: after an upgrade that adds a
// reader, the posts published before it are read once, then follow the
// schedule (ADR 0018). It runs once per process.
func (s *Service) resumeEngagement(ctx context.Context, orgID *uuid.UUID, now time.Time) error {
	var readable []string
	for _, p := range append(s.platforms.Providers(), platform.Sandbox) {
		if a, ok := s.platforms.Get(p); ok {
			if _, reads := a.(platform.EngagementReader); reads {
				readable = append(readable, string(p))
			}
		}
	}
	n, err := s.store.ResumeEngagement(ctx, orgID, readable, now)
	if n > 0 {
		s.log.InfoContext(ctx, "engagement resumed for posts on newly readable platforms", "targets", n)
	}
	return err
}

// readChannelEngagement reads one channel's due targets in one request
// and records the outcome of each. Only storage errors are returned: a
// platform's failure reschedules the reading.
func (s *Service) readChannelEngagement(ctx context.Context, now time.Time, due []store.EngagementDue) (int, error) {
	first := due[0]
	reschedule := func(state string, next *time.Time, msg string) error {
		for _, d := range due {
			if err := s.store.SetEngagementState(ctx, d.OrgID, d.TargetID, state, next, truncate(msg, 500)); err != nil {
				return err
			}
		}
		return nil
	}
	adapter, ok := s.platforms.Get(first.Provider)
	reader, canRead := adapter.(platform.EngagementReader)
	if !ok || !canRead {
		return 0, reschedule(model.EngagementUnsupported, nil, "")
	}
	ch, err := s.store.Channel(ctx, first.OrgID, first.ChannelID)
	if err != nil {
		return 0, err
	}
	creds, err := s.usableCredentials(ctx, ch, adapter)
	if err != nil {
		return 0, reschedule(model.EngagementCollecting, ptr(now.Add(time.Hour)), "could not decrypt the channel's credentials")
	}
	var refs []platform.RemoteRef
	for _, d := range due {
		refs = append(refs, d.Posted...)
	}
	rctx, cancel := context.WithTimeout(ctx, engagementTimeout)
	counts, err := reader.Engagement(rctx, creds, refs)
	cancel()
	if err != nil {
		var pe *platform.Error
		retry := 30 * time.Minute
		if errors.As(err, &pe) {
			switch pe.Kind {
			case platform.RateLimited:
				retry = max(pe.RetryAfter, 15*time.Minute)
			case platform.AuthRevoked:
				retry = 6 * time.Hour
			}
		}
		return 0, reschedule(model.EngagementCollecting, ptr(now.Add(retry)), err.Error())
	}
	n := 0
	for _, d := range due {
		total, found := threadCounts(d.Posted, counts)
		if !found {
			if err := s.store.SetEngagementState(ctx, d.OrgID, d.TargetID, model.EngagementDeleted, nil, "the post is no longer on the platform"); err != nil {
				return n, err
			}
			continue
		}
		if err := s.store.RecordEngagement(ctx, d.OrgID, d.TargetID, now, total, nextReading(d.PublishedAt, now)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// threadCounts adds up a thread's parts. Every part after the first
// replies to the one before it, so those replies are the thread's own and
// do not count. found is false when the first part is gone (or there was
// none).
func threadCounts(parts []platform.RemoteRef, counts map[string]platform.Counts) (total platform.Counts, found bool) {
	if len(parts) == 0 {
		return total, false
	}
	if _, ok := counts[parts[0].ID]; !ok {
		return total, false
	}
	own := int64(0)
	for i, p := range parts {
		c, ok := counts[p.ID]
		if !ok {
			continue
		}
		total = total.Add(c)
		if i > 0 {
			own++
		}
	}
	total.Replies = max(total.Replies-own, 0)
	return total, true
}

// EngagementReadings lists a target's engagement readings, oldest first.
func (s *Service) EngagementReadings(ctx context.Context, a Actor, targetID uuid.UUID) ([]model.EngagementReading, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, err
	}
	t, err := s.store.Target(ctx, a.OrgID, targetID)
	if err != nil || t.Livemode != a.Livemode {
		return nil, apperr.NotFound("post target")
	}
	if _, err := s.Post(ctx, a, t.PostID); err != nil {
		return nil, err // a key limited to another brand
	}
	return s.store.EngagementReadings(ctx, a.OrgID, targetID)
}

// EngagementFilter picks what a summary covers.
type EngagementFilter = store.EngagementFilter

// EngagementRow is one group in a summary.
type EngagementRow = store.EngagementRow

// EngagementSummary adds up the latest engagement of targets published in
// a window, grouped by post, channel or template, most engaging first.
func (s *Service) EngagementSummary(ctx context.Context, a Actor, f EngagementFilter) ([]EngagementRow, error) {
	if err := a.require(PermPostsRead); err != nil {
		return nil, err
	}
	if a.BrandID != nil {
		if f.BrandID != nil && *f.BrandID != *a.BrandID {
			return nil, apperr.NotFound("brand")
		}
		f.BrandID = a.BrandID
	}
	switch f.GroupBy {
	case "":
		f.GroupBy = store.GroupByPost
	case store.GroupByPost, store.GroupByChannel, store.GroupByTemplate:
	default:
		return nil, apperr.Invalid("group_by_invalid", "group_by", "Group by post, channel or template.")
	}
	if f.Until.IsZero() {
		f.Until = s.Now()
	}
	if f.Since.IsZero() {
		f.Since = f.Until.Add(-30 * 24 * time.Hour)
	}
	if !f.Since.Before(f.Until) || f.Until.Sub(f.Since) > MaxEngagementWindow {
		return nil, apperr.Invalid("window_invalid", "since", "since must be before until, at most a year apart.")
	}
	return s.store.EngagementSummary(ctx, a.OrgID, a.Livemode, f)
}

// EngagementRowID formats a summary row's ID for its grouping.
func EngagementRowID(group store.EngagementGroup, u *uuid.UUID) string {
	if u == nil {
		return ""
	}
	switch group {
	case store.GroupByChannel:
		return id.Format(id.Channel, *u)
	case store.GroupByTemplate:
		return id.Format(id.Template, *u)
	}
	return id.Format(id.Post, *u)
}
