// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/metric"
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
