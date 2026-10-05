// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// ChannelCheckEvery is how often each live channel's credentials are
// verified with its platform, so a revoked token or a retired API shows on
// the channel before a post fails.
const ChannelCheckEvery = 24 * time.Hour

// channelCheckEvery is the check's interval for platforms that charge for
// it: X bills each account lookup, to the app the channel connected
// through, so its channels are checked weekly.
var channelCheckEvery = map[platform.Provider]time.Duration{platform.X: 7 * 24 * time.Hour}

// CheckEvery is how often a provider's channels are checked.
func CheckEvery(p platform.Provider) time.Duration {
	if d, ok := channelCheckEvery[p]; ok {
		return d
	}
	return ChannelCheckEvery
}

// CheckChannels verifies the channels that are due.
func (s *Service) CheckChannels(ctx context.Context) (int, error) {
	return s.checkChannels(ctx, nil)
}

func (s *Service) checkChannels(ctx context.Context, orgID *uuid.UUID) (int, error) {
	now := s.Now()
	byInterval := map[time.Duration][]string{}
	for _, p := range s.platforms.Providers() {
		byInterval[CheckEvery(p)] = append(byInterval[CheckEvery(p)], string(p))
	}
	n := 0
	for every, providers := range byInterval {
		due, err := s.store.ClaimChannelsToCheck(ctx, orgID, providers, now, now.Add(-every), 25)
		if err != nil {
			return n, err
		}
		if err := s.checkEach(ctx, due, now); err != nil {
			return n, err
		}
		n += len(due)
	}
	return n, nil
}

func (s *Service) checkEach(ctx context.Context, due []*model.Channel, now time.Time) error {
	for _, ch := range due {
		problem := ""
		adapter, ok := s.platforms.Get(ch.Provider)
		if !ok {
			problem = "This server has no " + string(ch.Provider) + " adapter."
		} else if creds, err := s.usableCredentials(ctx, ch, adapter); err != nil {
			problem = "Could not decrypt the channel's credentials."
		} else if _, err := adapter.Verify(ctx, creds); err != nil {
			problem = truncate("The credential check failed: "+err.Error(), 500)
			var pe *platform.Error
			if errors.As(err, &pe) && pe.Kind == platform.AuthRevoked {
				if err := s.needsReauth(ctx, ch, "The credential check found the credentials no longer work: reconnect the account."); err != nil {
					return err
				}
			} else {
				s.log.WarnContext(ctx, "channel check failed", "channel", id.Format(id.Channel, ch.ID), "err", err)
			}
		}
		if err := s.store.SetChannelCheck(ctx, ch.OrgID, ch.ID, now, problem); err != nil {
			return err
		}
	}
	return nil
}
