// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Publishing (ADR 0011).
const (
	publishPoll    = 5 * time.Second
	publishLease   = 3 * time.Minute
	publishTimeout = 90 * time.Second
	// videoPublishTimeout leaves room to upload a video and for the
	// platform to process it (ADR 0027); the lease is renewed meanwhile.
	videoPublishTimeout = 20 * time.Minute
	leaseRenewEvery     = time.Minute
	publishBatch        = 10
	maxRetryDelay       = 30 * time.Minute
	// publishDrain is how long a stopping worker lets publishes already
	// under way finish. Cutting one off mid-request leaves it uncertain,
	// and on most platforms that means a person checks it by hand. It is
	// under the chart's terminationGracePeriodSeconds (60).
	publishDrain = 45 * time.Second
)

// RunPublisher publishes due targets until ctx ends: it polls every few
// seconds and wakes early on NOTIFY araldo_publish. It keeps up to
// publishBatch publishes under way, claiming more as each finishes, so a
// video taking minutes holds one slot, not the rest. Once ctx ends it
// claims nothing more, and gives the publishes under way publishDrain to
// finish.
func (s *Service) RunPublisher(ctx context.Context, owner string) error {
	wake := make(chan struct{}, 1)
	go s.listen(ctx, "araldo_publish", wake)
	work, stop := drain(ctx, publishDrain)
	defer stop()
	busy := make(chan struct{}, publishBatch) // one per publish under way
	var wg sync.WaitGroup
	defer wg.Wait() // before stop: let them finish within the drain
	for {
		free := publishBatch - len(busy)
		claimed := 0
		if free > 0 && ctx.Err() == nil {
			cts, err := s.claimDue(ctx, owner, free)
			if err != nil && ctx.Err() == nil {
				s.log.ErrorContext(ctx, "claiming due targets failed", "err", err)
			}
			for _, ct := range cts {
				busy <- struct{}{}
				wg.Go(func() {
					defer func() {
						<-busy
						select { // a slot is free: look for more
						case wake <- struct{}{}:
						default:
						}
					}()
					s.publishOne(work, owner, ct)
				})
			}
			claimed = len(cts)
		}
		if free > 0 && claimed == free {
			continue // more may be waiting
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-time.After(publishPoll):
		}
	}
}

// listen forwards Postgres notifications on channel to wake, reconnecting
// after errors.
func (s *Service) listen(ctx context.Context, channel string, wake chan<- struct{}) {
	for ctx.Err() == nil {
		conn, err := s.store.Pool().Acquire(ctx)
		if err != nil {
			sleepCtx(ctx, 5*time.Second)
			continue
		}
		if _, err := conn.Exec(ctx, "LISTEN "+channel); err == nil {
			for {
				if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
					break
				}
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
		conn.Release()
		sleepCtx(ctx, time.Second)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// drain returns a context with ctx's values that ends grace after ctx
// does, so work under way when ctx ends can finish.
func drain(ctx context.Context, grace time.Duration) (context.Context, context.CancelFunc) {
	work, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		select {
		case <-time.After(grace):
			cancel()
		case <-work.Done():
		}
	})
	return work, func() { stop(); cancel() }
}

// PublishDue claims and publishes one batch of due targets, one per
// channel, in parallel, and returns how many it claimed.
func (s *Service) PublishDue(ctx context.Context, owner string) (int, error) {
	return s.publishDue(ctx, ctx, owner)
}

// publishDue claims while ctx lasts and publishes while work does.
func (s *Service) publishDue(ctx, work context.Context, owner string) (int, error) {
	select {
	case <-ctx.Done():
		return 0, nil // stopping: claim nothing more
	default:
	}
	claimed, err := s.claimDue(ctx, owner, publishBatch)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, ct := range claimed {
		wg.Go(func() { s.publishOne(work, owner, ct) })
	}
	wg.Wait()
	return len(claimed), nil
}

// claimDue leases up to n due targets.
func (s *Service) claimDue(ctx context.Context, owner string, n int) ([]store.ClaimedTarget, error) {
	now := s.Now()
	return s.store.ClaimDueTargets(ctx, owner, now, now.Add(publishLease), n)
}

// publishOne publishes a claimed target, logging what cannot be recorded.
func (s *Service) publishOne(work context.Context, owner string, ct store.ClaimedTarget) {
	if err := s.publishTarget(work, owner, ct); err != nil {
		s.log.ErrorContext(work, "recording publish outcome failed", "target", id.Format(id.Target, ct.ID), "err", err)
	}
}

func (s *Service) publishTarget(ctx context.Context, owner string, ct store.ClaimedTarget) error {
	t := ct.Target
	started := s.Now()
	attempt := &model.Attempt{ID: id.New(), OrgID: t.OrgID, TargetID: t.ID, Attempt: t.Attempts, StartedAt: started}
	// The attempt is on record before the platform is called, so a crash
	// leaves evidence (ADR 0011).
	if err := s.store.StartAttempt(ctx, attempt); err != nil {
		return err
	}
	ch, err := s.store.Channel(ctx, t.OrgID, t.ChannelID)
	if err != nil {
		return err
	}
	adapter, ok := s.platforms.Get(ch.Provider)
	var res platform.Result
	var pubErr error
	switch {
	case !ok:
		pubErr = &platform.Error{Kind: platform.Rejected, Code: "provider_unsupported", Msg: "this server has no " + string(ch.Provider) + " adapter"}
	default:
		creds, err := s.usableCredentials(ctx, ch, adapter)
		if err != nil {
			pubErr = &platform.Error{Kind: platform.Transient, Code: "credentials_unreadable", Msg: "could not decrypt the channel's credentials", Err: err}
			break
		}
		media, err := s.store.PostMedia(ctx, t.OrgID, t.PostID)
		if err != nil {
			pubErr = &platform.Error{Kind: platform.Transient, Code: "media_unreadable", Msg: "could not read the post's media", Err: err}
			break
		}
		rules, _ := platform.RulesFor(ch.RulesProvider())
		pm, err := s.payloadMedia(ctx, media, rules.ForMedia(len(media)))
		if err != nil {
			pubErr = err
			break
		}
		payload := platform.Payload{Key: id.Format(id.Target, t.ID), KeyTime: t.CreatedAt, Parts: t.Parts, Posted: t.Posted, Attempt: t.Attempts,
			Media: pm}
		if !t.Livemode {
			payload.Simulate = ct.Metadata["araldo_simulate"]
		}
		timeout := publishTimeout
		for _, m := range pm {
			if m.IsVideo() {
				timeout = videoPublishTimeout
			}
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		stop := s.keepLease(pctx, t.ID, owner)
		res, pubErr = adapter.Publish(pctx, creds, payload, func(ref platform.RemoteRef) error {
			return s.store.RecordPostedPart(context.WithoutCancel(ctx), t.ID, owner, ref)
		})
		stop()
		cancel()
	}
	return s.finishPublish(context.WithoutCancel(ctx), owner, &t, ch, adapter, attempt, res, pubErr)
}

func (s *Service) finishPublish(ctx context.Context, owner string, t *model.Target, ch *model.Channel, adapter platform.Adapter,
	attempt *model.Attempt, res platform.Result, pubErr error) error {
	now := s.Now()
	o := store.TargetOutcome{NextAttemptAt: now}
	outcome, event := historyOutcome(pubErr), "post_target.published"
	var pe *platform.Error
	switch {
	case pubErr == nil:
		o.Status, o.Permalink, o.Posted, o.PublishedAt = model.TargetPublished, res.Permalink, res.Parts, &now
		if o.Permalink == "" && len(res.Parts) > 0 {
			o.Permalink = res.Parts[0].URL
		}
	case errors.As(pubErr, &pe):
		o.ErrorCode, o.ErrorMessage = pe.Code, truncate(pe.Error(), 1000)
		if o.ErrorCode == "" {
			o.ErrorCode = string(pe.Kind)
		}
		event = ""
		switch pe.Kind {
		case platform.RateLimited:
			o.Status, o.NextAttemptAt = model.TargetQueued, now.Add(max(pe.RetryAfter, 30*time.Second))
		case platform.Transient, platform.AuthRevoked:
			o.Status, o.NextAttemptAt = model.TargetQueued, now.Add(retryDelay(t.Attempts))
		case platform.Uncertain:
			if adapter != nil && adapter.Idempotent() {
				o.Status, o.NextAttemptAt = model.TargetQueued, now.Add(retryDelay(t.Attempts))
			} else {
				o.Status, event = model.TargetNeedsAttention, "post_target.needs_attention"
				o.ErrorMessage = "The platform may or may not have published this: " + o.ErrorMessage +
					". Check the account, then retry or mark it published."
			}
		default: // Rejected
			o.Status, event = model.TargetFailed, "post_target.failed"
		}
		if o.Status == model.TargetQueued && t.PublishBy != nil && o.NextAttemptAt.After(*t.PublishBy) {
			o.Status, event = model.TargetFailed, "post_target.failed"
			o.ErrorMessage = "Gave up at the publish_by deadline. Last error: " + o.ErrorMessage
		}
	default:
		o.Status, o.ErrorCode, o.ErrorMessage, event = model.TargetNeedsAttention, "unknown", truncate(pubErr.Error(), 1000), "post_target.needs_attention"
	}
	s.metrics.recordAttempt(ctx, t, ch.Provider, adapter != nil, o.Status, pubErr)
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.FinishTarget(ctx, t.ID, owner, o); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("lease on %s lost before the outcome was recorded", id.Format(id.Target, t.ID))
			}
			return err
		}
		if err := tx.FinishAttempt(ctx, attempt.ID, now, outcome, o.ErrorCode, o.ErrorMessage); err != nil {
			return err
		}
		if o.Status == model.TargetPublished {
			if err := tx.StartEngagement(ctx, t.OrgID, t.ID, now.Add(EngagementSchedule[0])); err != nil {
				return err
			}
		}
		if pe != nil {
			switch pe.Kind {
			case platform.RateLimited:
				if err := tx.HoldChannel(ctx, ch.OrgID, ch.ID, o.NextAttemptAt); err != nil {
					return err
				}
			case platform.AuthRevoked:
				if ch.Status == model.ChannelActive {
					if err := tx.SetChannelStatus(ctx, ch.OrgID, ch.ID, model.ChannelNeedsReauth, truncate(pe.Msg, 500)); err != nil {
						return err
					}
					ch.Status, ch.StatusNote = model.ChannelNeedsReauth, pe.Msg
					if err := s.emit(ctx, tx, ch.OrgID, ch.Livemode, "", "channel.needs_reauth", ViewChannel(ch)); err != nil {
						return err
					}
				}
			}
		}
		if event != "" {
			stored, err := tx.Target(ctx, t.OrgID, t.ID)
			if err != nil {
				return err
			}
			if err := s.emit(ctx, tx, t.OrgID, t.Livemode, "", event, ViewTarget(stored)); err != nil {
				return err
			}
		}
		_, err := s.refreshPost(ctx, tx, t.OrgID, t.PostID, "", "")
		return err
	})
}

// historyOutcome is what an attempt's history row records: "published", the platform's
// classification of the error, or "unknown" for an error the adapter did not classify (which
// sends the target to needs_attention; recording it as published hid the failure).
func historyOutcome(pubErr error) string {
	if pubErr == nil {
		return "published"
	}
	var pe *platform.Error
	if errors.As(pubErr, &pe) {
		return string(pe.Kind)
	}
	return "unknown"
}

// retryDelay is 30 seconds doubling per attempt, up to 30 minutes.
func retryDelay(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempts && d < maxRetryDelay; i++ {
		d *= 2
	}
	return min(d, maxRetryDelay)
}

// ReclaimLostTargets handles targets whose worker vanished mid-publish:
// retried if the platform is idempotent, otherwise held for a person
// (ADR 0011).
func (s *Service) ReclaimLostTargets(ctx context.Context) (int, error) {
	now := s.Now()
	lost, err := s.store.ExpiredLeases(ctx, now, 100)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range lost {
		status, code, msg, event := model.TargetNeedsAttention, "worker_lost",
			"The worker stopped while publishing, so this may or may not have been published. Check the account, then retry or mark it published.",
			"post_target.needs_attention"
		if a, ok := s.platforms.Get(t.Provider); ok && a.Idempotent() {
			status, msg, event = model.TargetQueued, "The worker stopped while publishing; retrying safely.", ""
		}
		err := s.store.InTx(ctx, func(tx *store.Store) error {
			if err := tx.ReclaimTarget(ctx, t.ID, now, status, code, msg); err != nil {
				return err
			}
			if event != "" {
				stored, err := tx.Target(ctx, t.OrgID, t.ID)
				if err != nil {
					return err
				}
				if err := s.emit(ctx, tx, t.OrgID, t.Livemode, "", event, ViewTarget(stored)); err != nil {
					return err
				}
			}
			_, err := s.refreshPost(ctx, tx, t.OrgID, t.PostID, "", "")
			return err
		})
		if errors.Is(err, store.ErrNotFound) {
			continue // finished after all
		}
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ExpireOverdue fails queued targets past their publish_by deadline.
func (s *Service) ExpireOverdue(ctx context.Context) (int, error) {
	var n int
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		expired, err := tx.ExpireOverdueTargets(ctx, s.Now(), 200)
		if err != nil {
			return err
		}
		n = len(expired)
		for i := range expired {
			t := &expired[i]
			if err := s.emit(ctx, tx, t.OrgID, t.Livemode, "", "post_target.failed", ViewTarget(t)); err != nil {
				return err
			}
			if _, err := s.refreshPost(ctx, tx, t.OrgID, t.PostID, "", ""); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// SandboxTarget returns a test-mode target for the sandbox page, which
// shows what the sandbox "published".
func (s *Service) SandboxTarget(ctx context.Context, ref string) (*model.Target, error) {
	tid, err := ParseID(id.Target, ref, "post target")
	if err != nil {
		return nil, err
	}
	t, err := s.store.TargetAnyOrg(ctx, tid)
	if err != nil || t.Status != model.TargetPublished {
		return nil, notFoundID("post target", ref)
	}
	return t, nil
}

// keepLease renews a target's lease while its worker publishes, so a long
// attempt is not taken for a lost one. It stops when stop is called or ctx
// ends.
func (s *Service) keepLease(ctx context.Context, targetID uuid.UUID, owner string) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(leaseRenewEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.store.ExtendLease(context.WithoutCancel(ctx), targetID, owner, s.Now().Add(publishLease)); err != nil {
					s.log.WarnContext(ctx, "renewing a publishing lease failed", "target", id.Format(id.Target, targetID), "err", err)
				}
			}
		}
	}()
	return func() { close(done) }
}
