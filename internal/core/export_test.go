// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/metric"

	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
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
