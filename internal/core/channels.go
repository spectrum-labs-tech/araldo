// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

func notFoundID(what, s string) error {
	return &apperr.Error{Kind: apperr.KindNotFound, Code: "resource_missing", Message: fmt.Sprintf("No such %s: %q.", what, s)}
}

func credentialsAAD(channelID uuid.UUID) string {
	return keyring.AAD("channels", "credentials", channelID)
}

// ConnectInput connects a channel.
type ConnectInput struct {
	BrandID  uuid.UUID
	Provider platform.Provider
	// Fields are the adapter's settings and secrets (see Adapter.Fields);
	// for a sandbox channel, "emulates".
	Fields map[string]string
}

// ConnectChannel verifies credentials with the platform and stores the
// channel, secrets encrypted (ADR 0008). Test mode connects only sandbox
// channels, and live mode never does (ADR 0006).
func (s *Service) ConnectChannel(ctx context.Context, a Actor, in ConnectInput) (*model.Channel, error) {
	if err := a.require(PermChannelsWrite); err != nil {
		return nil, err
	}
	b, err := s.Brand(ctx, a, in.BrandID)
	if err != nil {
		return nil, err
	}
	switch {
	case in.Provider == "":
		return nil, apperr.Invalid("field_required", "provider", "Name the platform to connect, e.g. bluesky.")
	case !a.Livemode && in.Provider != platform.Sandbox:
		return nil, apperr.Invalid("livemode_required", "provider",
			"Test mode can only connect sandbox channels. Switch to live mode to connect a real %s account.", in.Provider)
	case a.Livemode && in.Provider == platform.Sandbox:
		return nil, apperr.Invalid("testmode_required", "provider", "Sandbox channels exist only in test mode.")
	}
	adapter, ok := s.platforms.Get(in.Provider)
	if !ok {
		return nil, apperr.Invalid("provider_unsupported", "provider", "Araldo cannot connect to %q yet.", in.Provider)
	}
	settings, secrets, err := splitFields(adapter.Rules().Name+" channels", adapter.Fields(), in.Fields)
	if err != nil {
		return nil, err
	}
	creds := platform.Credentials{}
	for k, v := range settings {
		creds[k] = v
	}
	for k, v := range secrets {
		creds[k] = v
	}
	acct, err := adapter.Verify(ctx, creds)
	if err != nil {
		return nil, connectError(in.Provider, err)
	}
	ch := &model.Channel{
		ID: id.New(), OrgID: a.OrgID, BrandID: b.ID, Livemode: a.Livemode, Provider: in.Provider, DisplayName: acct.DisplayName,
		Handle: acct.Handle, ExternalID: acct.ExternalID, ProfileURL: acct.URL, Settings: settings, Status: model.ChannelActive,
	}
	if in.Provider == platform.Sandbox {
		ch.Emulates = platform.Provider(settings["emulates"])
	}
	if len(secrets) > 0 {
		raw, err := json.Marshal(secrets)
		if err != nil {
			return nil, err
		}
		if ch.Credentials, err = s.keys.Encrypt(ctx, a.OrgID, credentialsAAD(ch.ID), raw); err != nil {
			return nil, err
		}
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateChannel(ctx, ch); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, a, "channel.connect", id.Format(id.Channel, ch.ID), map[string]any{"provider": in.Provider}); err != nil {
			return err
		}
		return s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, "channel.connected", ViewChannel(ch))
	})
	return ch, err
}

// ReconnectChannel replaces a channel's credentials (after they were
// revoked) and makes it active again.
func (s *Service) ReconnectChannel(ctx context.Context, a Actor, channelID uuid.UUID, fields map[string]string) (*model.Channel, error) {
	if err := a.require(PermChannelsWrite); err != nil {
		return nil, err
	}
	ch, err := s.Channel(ctx, a, channelID)
	if err != nil {
		return nil, err
	}
	adapter, ok := s.platforms.Get(ch.Provider)
	if !ok {
		return nil, apperr.Invalid("provider_unsupported", "provider", "Araldo cannot connect to %q.", ch.Provider)
	}
	// Settings left out keep their current values; secrets must be sent.
	merged := maps.Clone(ch.Settings)
	if merged == nil {
		merged = map[string]string{}
	}
	maps.Copy(merged, fields)
	settings, secrets, err := splitFields(adapter.Rules().Name+" channels", adapter.Fields(), merged)
	if err != nil {
		return nil, err
	}
	creds := platform.Credentials{}
	for k, v := range settings {
		creds[k] = v
	}
	for k, v := range secrets {
		creds[k] = v
	}
	acct, err := adapter.Verify(ctx, creds)
	if err != nil {
		return nil, connectError(ch.Provider, err)
	}
	ch.DisplayName, ch.Handle, ch.ExternalID, ch.ProfileURL, ch.Settings = acct.DisplayName, acct.Handle, acct.ExternalID, acct.URL, settings
	ch.Status, ch.StatusNote, ch.Credentials = model.ChannelActive, "", nil
	if len(secrets) > 0 {
		raw, _ := json.Marshal(secrets)
		if ch.Credentials, err = s.keys.Encrypt(ctx, a.OrgID, credentialsAAD(ch.ID), raw); err != nil {
			return nil, err
		}
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.UpdateChannelConnection(ctx, ch); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "channel.reconnect", id.Format(id.Channel, ch.ID), nil)
	})
	return ch, err
}

// splitFields checks fields against the adapter's list and separates
// settings from secrets.
func splitFields(what string, defs []platform.Field, fields map[string]string) (settings, secrets map[string]string, err error) {
	settings, secrets = map[string]string{}, map[string]string{}
	var ps apperr.Problems
	known := map[string]bool{}
	for _, f := range defs {
		known[f.Name] = true
		v := strings.TrimSpace(fields[f.Name])
		if v == "" {
			v = f.Default
		}
		switch {
		case v == "" && !f.Optional:
			ps.Add("field_required", "fields."+f.Name, "%s is required.", f.Label)
		case v == "":
		case f.Secret:
			secrets[f.Name] = v
		default:
			settings[f.Name] = v
		}
	}
	for k := range fields {
		if !known[k] {
			ps.Add("field_unknown", "fields."+k, "%s have no field %q.", what, k)
		}
	}
	return settings, secrets, ps.Err("The channel settings are not valid.")
}

func connectError(p platform.Provider, err error) error {
	var pe *platform.Error
	if errors.As(err, &pe) {
		switch pe.Kind {
		case platform.AuthRevoked, platform.Rejected:
			return &apperr.Error{Kind: apperr.KindInvalid, Code: "connect_failed", Param: "fields",
				Message: fmt.Sprintf("%s did not accept these credentials: %s", p, pe.Msg)}
		default:
			return &apperr.Error{Kind: apperr.KindInvalid, Code: "provider_unavailable",
				Message: fmt.Sprintf("Could not reach %s to check the credentials; try again. (%s)", p, pe.Kind)}
		}
	}
	return err
}

// Channel returns one of the actor's channels (in the actor's mode).
func (s *Service) Channel(ctx context.Context, a Actor, channelID uuid.UUID) (*model.Channel, error) {
	if err := a.require(PermChannelsRead); err != nil && !a.Can(PermPostsWrite) {
		return nil, err
	}
	ch, err := s.store.Channel(ctx, a.OrgID, channelID)
	if err != nil {
		return nil, notFound(err, "channel")
	}
	if ch.Livemode != a.Livemode || a.brandAllowed(ch.BrandID) != nil {
		return nil, apperr.NotFound("channel")
	}
	return ch, nil
}

// Channels lists channels in the actor's mode, optionally for one brand.
func (s *Service) Channels(ctx context.Context, a Actor, brandID *uuid.UUID) ([]*model.Channel, error) {
	if err := a.require(PermChannelsRead); err != nil && !a.Can(PermPostsWrite) {
		return nil, err
	}
	if a.BrandID != nil {
		if brandID != nil && *brandID != *a.BrandID {
			return nil, apperr.NotFound("brand")
		}
		brandID = a.BrandID
	}
	return s.store.Channels(ctx, a.OrgID, a.Livemode, brandID)
}

// SetChannelEnabled pauses or resumes a channel.
func (s *Service) SetChannelEnabled(ctx context.Context, a Actor, channelID uuid.UUID, enabled bool) error {
	if err := a.require(PermChannelsWrite); err != nil {
		return err
	}
	ch, err := s.Channel(ctx, a, channelID)
	if err != nil {
		return err
	}
	status := model.ChannelDisabled
	if enabled {
		status = model.ChannelActive
		if ch.Status == model.ChannelNeedsReauth {
			return apperr.Conflict("needs_reauth", "Reconnect this channel to enable it.")
		}
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SetChannelStatus(ctx, a.OrgID, ch.ID, status, ""); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "channel."+map[bool]string{true: "enable", false: "disable"}[enabled], id.Format(id.Channel, ch.ID), nil)
	})
}

// DeleteChannel disconnects a channel and drops its history of targets.
func (s *Service) DeleteChannel(ctx context.Context, a Actor, channelID uuid.UUID) error {
	if err := a.require(PermChannelsWrite); err != nil {
		return err
	}
	ch, err := s.Channel(ctx, a, channelID)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteChannel(ctx, a.OrgID, ch.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "channel.delete", id.Format(id.Channel, ch.ID), map[string]any{"provider": ch.Provider})
	})
}

// credentials decrypts a channel's secrets and merges its settings.
func (s *Service) credentials(ctx context.Context, ch *model.Channel) (platform.Credentials, error) {
	return credentialsWith(ctx, s.keys, ch)
}

func credentialsWith(ctx context.Context, keys *keyring.Keyring, ch *model.Channel) (platform.Credentials, error) {
	creds := platform.Credentials{}
	for k, v := range ch.Settings {
		creds[k] = v
	}
	if len(ch.Credentials) == 0 {
		return creds, nil
	}
	raw, err := keys.Decrypt(ctx, ch.OrgID, credentialsAAD(ch.ID), ch.Credentials)
	if err != nil {
		return nil, err
	}
	var secrets map[string]string
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return nil, err
	}
	for k, v := range secrets {
		creds[k] = v
	}
	return creds, nil
}

// ProviderInfo describes a platform for connect forms.
type ProviderInfo struct {
	Provider platform.Provider
	Name     string
	Fields   []platform.Field
	Rules    platform.Rules
	// OAuth providers connect with a sign-in through a developer app.
	OAuth bool
}

// Providers lists the platforms channels can connect to in a mode.
func (s *Service) Providers(livemode bool) []ProviderInfo {
	if !livemode {
		a, _ := s.platforms.Get(platform.Sandbox)
		return []ProviderInfo{{Provider: platform.Sandbox, Name: "Sandbox", Fields: a.Fields(), Rules: a.Rules()}}
	}
	var out []ProviderInfo
	for _, p := range s.platforms.Providers() {
		a, _ := s.platforms.Get(p)
		_, oauth := a.(platform.Connector)
		out = append(out, ProviderInfo{Provider: p, Name: a.Rules().Name, Fields: a.Fields(), Rules: a.Rules(), OAuth: oauth})
	}
	return out
}
