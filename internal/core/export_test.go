// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/metric"

	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// InstrumentOrg is Instrument with the database gauges narrowed to one
// org and never cached, so integration tests, which share a database with
// tests running in parallel, see exactly their own rows.
func InstrumentOrg(s *Service, mp metric.MeterProvider, org uuid.UUID) error {
	if err := s.instrument(mp, &org); err != nil {
		return err
	}
	s.metrics.cache = 0
	return nil
}

// PruneUnusedMediaOrg is PruneUnusedMedia for one org, so a test prunes
// only media it created.
func PruneUnusedMediaOrg(s *Service, org uuid.UUID) (int, error) {
	return s.pruneUnusedMedia(context.Background(), &org)
}

// NotifyOrg tells recipients in orgID of a notification of type typ, as
// the code that raises one does.
func NotifyOrg(s *Service, orgID uuid.UUID, livemode bool, typ, subject, dedupe string, recipients []uuid.UUID, except *uuid.UUID) error {
	return s.store.InTx(context.Background(), func(tx *store.Store) error {
		return s.notifyOrg(context.Background(), tx, orgID, livemode, notice{Type: typ, Subject: subject, Link: "/posts", DedupeKey: dedupe},
			recipients, except)
	})
}

// SendNotificationEmailsTo is SendNotificationEmails for one person, so a
// test sends only its own.
func SendNotificationEmailsTo(s *Service, userID uuid.UUID) (int, error) {
	return s.sendNotificationEmails(context.Background(), &userID)
}

// MailReportsOrg is MailReports for one org, so a test mails only its
// own.
func MailReportsOrg(s *Service, orgID uuid.UUID) (int, error) {
	return s.mailReports(context.Background(), &orgID)
}

// CollectEngagementOrg is CollectEngagement for one org, so a test reads
// only its own targets.
func CollectEngagementOrg(s *Service, org uuid.UUID) (int, error) {
	return s.collectEngagement(context.Background(), &org)
}

// RefreshTokensOrg is RefreshTokens for one org.
func RefreshTokensOrg(s *Service, org uuid.UUID) (int, error) {
	return s.refreshTokens(context.Background(), &org)
}

// UsableCredentials is the credentials a publish or engagement read would
// use for a channel, renewing its token first if it is about to expire.
func UsableCredentials(s *Service, ch *model.Channel) (platform.Credentials, error) {
	adapter, _ := s.platforms.Get(ch.Provider)
	return s.usableCredentials(context.Background(), ch, adapter)
}

// CollectAdsOrg is CollectAds for one org, so a test reads only its own
// ad accounts.
// StoredCredentials are a channel's credentials as stored, without
// renewing anything.
func StoredCredentials(s *Service, ch *model.Channel) (platform.Credentials, error) {
	return credentialsWith(context.Background(), s.keys, ch)
}

func CollectAdsOrg(s *Service, org uuid.UUID) (int, error) {
	return s.collectAds(context.Background(), &org)
}

// CheckChannelsOrg is CheckChannels for one org.
func CheckChannelsOrg(s *Service, org uuid.UUID) (int, error) {
	return s.checkChannels(context.Background(), &org)
}

// CollectAnalyticsOrg is CollectAnalytics for one org.
func CollectAnalyticsOrg(s *Service, org uuid.UUID) (int, error) {
	return s.collectAnalytics(context.Background(), &org)
}

// HandOffNewslettersOrg is HandOffNewsletters for one org.
func HandOffNewslettersOrg(s *Service, org uuid.UUID) (int, error) {
	return s.handOffNewsletters(context.Background(), &org)
}

// ReadNewsletterResultsOrg is ReadNewsletterResults for one org.
func ReadNewsletterResultsOrg(s *Service, org uuid.UUID) (int, error) {
	return s.readNewsletterResults(context.Background(), &org)
}

// MediaLinkFor is mediaLinkFor, for tests of resized copies.
func MediaLinkFor(ctx context.Context, s *Service, m *model.Media, p platform.Provider) string {
	return s.mediaLinkFor(ctx, m, p)
}

// RunPublisherOrg is RunPublisher for one org, so a test's publisher takes
// no other test's posts.
func RunPublisherOrg(ctx context.Context, s *Service, owner string, org uuid.UUID) error {
	return s.runPublisher(ctx, owner, &org)
}

// RunDelivererOrg is RunDeliverer for one org.
func RunDelivererOrg(ctx context.Context, s *Service, owner string, org uuid.UUID) error {
	return s.runDeliverer(ctx, owner, &org)
}

// DeliverDue sends one batch of due deliveries in parallel, in every org,
// as the deliverer does each round.
func (s *Service) DeliverDue(ctx context.Context, owner string) (int, error) {
	now := s.Now()
	claimed, err := s.store.ClaimDeliveries(ctx, nil, owner, now, now.Add(deliveryLease), deliveryBatch)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, d := range claimed {
		wg.Go(func() { s.deliverOne(ctx, owner, d) })
	}
	wg.Wait()
	return len(claimed), nil
}

// SetNotificationChoice records the actor's choice for a type in their
// org. Account notices cannot be turned off.
func (s *Service) SetNotificationChoice(ctx context.Context, a Actor, typ string, inApp, email bool) error {
	return s.SetNotificationChoices(ctx, a, []model.NotificationPref{{Type: typ, InApp: inApp, Email: email}})
}

// PublishDueOrg is PublishDue for one org, so a test publishes only its own
// targets.
func PublishDueOrg(s *Service, org uuid.UUID) (int, error) {
	ctx := context.Background()
	claimed, err := s.claimDue(ctx, &org, "test-worker", publishBatch)
	if err != nil {
		return 0, err
	}
	for _, ct := range claimed {
		s.publishOne(ctx, "test-worker", ct)
	}
	return len(claimed), nil
}
