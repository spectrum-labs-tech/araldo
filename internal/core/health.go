// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// ChannelCheckEvery is how often each live channel's credentials are
// verified with its platform, so a revoked token or a retired API shows on
// the channel before a post fails.
const ChannelCheckEvery = 24 * time.Hour

// CheckChannels verifies the channels that are due.
func (s *Service) CheckChannels(ctx context.Context) (int, error) {
	return s.checkChannels(ctx, nil)
}

func (s *Service) checkChannels(ctx context.Context, orgID *uuid.UUID) (int, error) {
	now := s.Now()
	var providers []string
	for _, p := range s.platforms.Providers() {
		providers = append(providers, string(p))
	}
	due, err := s.store.ClaimChannelsToCheck(ctx, orgID, providers, now, now.Add(-ChannelCheckEvery), 25)
	if err != nil {
		return 0, err
	}
	for _, ch := range due {
		problem := ""
		adapter, ok := s.platforms.Get(ch.Provider)
		if !ok {
			problem = "This server has no " + string(ch.Provider) + " adapter."
		} else if creds, err := s.usableCredentials(ctx, ch, adapter); err != nil {
			problem = "Could not decrypt the channel's credentials."
		} else if _, err := adapter.Verify(ctx, creds); err != nil {
			problem = truncate("The daily check failed: "+err.Error(), 500)
			var pe *platform.Error
			if errors.As(err, &pe) && pe.Kind == platform.AuthRevoked {
				if err := s.needsReauth(ctx, ch, "The daily check found the credentials no longer work: reconnect the account."); err != nil {
					return 0, err
				}
			} else {
				s.log.WarnContext(ctx, "channel check failed", "channel", id.Format(id.Channel, ch.ID), "err", err)
			}
		}
		if err := s.store.SetChannelCheck(ctx, ch.OrgID, ch.ID, now, problem); err != nil {
			return 0, err
		}
	}
	return len(due), nil
}
