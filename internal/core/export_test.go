// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"

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
