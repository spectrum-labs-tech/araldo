// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// OAuth connections (ADR 0021).
const (
	// OAuthStateTTL is how long a sign-in may take.
	OAuthStateTTL = 15 * time.Minute
	// RefreshAhead is how long before expiry tokens are renewed.
	RefreshAhead = 7 * 24 * time.Hour
	// UseAhead is how close to expiry a token is renewed before it is
	// used, in case the hourly renewal fell behind (an X token lasts two
	// hours).
	UseAhead = 5 * time.Minute
)

// ProviderAppInput registers a developer app.
type ProviderAppInput struct {
	Provider     platform.Provider
	Name         string
	ClientID     string
	ClientSecret string
}

func appSecretAAD(appID uuid.UUID) string {
	return keyring.AAD("provider_apps", "client_secret", appID)
}

// connector returns the adapter that signs in for provider: a platform
// whose channels connect with OAuth, or an ad network (ADR 0023) whose apps
// are registered as ads.Provider(network).
func (s *Service) connector(p platform.Provider) (platform.Connector, bool) {
	if n, ok := ads.NetworkOf(p); ok {
		r, ok := s.adNetworks.Get(n)
		if !ok {
			return nil, false
		}
		c, ok := r.(platform.Connector)
		return c, ok
	}
	a, ok := s.platforms.Get(p)
	if !ok {
		return nil, false
	}
	c, ok := a.(platform.Connector)
	return c, ok
}

// OAuthProviders lists the providers that connect with OAuth: platforms,
// then ad networks.
func (s *Service) OAuthProviders() []platform.Provider {
	var out []platform.Provider
	for _, p := range s.platforms.Providers() {
		if _, ok := s.connector(p); ok {
			out = append(out, p)
		}
	}
	for _, n := range s.adNetworks.Networks() {
		if _, ok := s.connector(ads.Provider(n)); ok {
			out = append(out, ads.Provider(n))
		}
	}
	return out
}

// ProviderName is a sign-in provider's name for people: "Threads", or
// "Reddit Ads" for an ad network.
func (s *Service) ProviderName(p platform.Provider) string {
	if n, ok := ads.NetworkOf(p); ok {
		if r, ok := s.adNetworks.Get(n); ok {
			return r.Name() + " Ads"
		}
	}
	if r, ok := platform.RulesFor(p); ok {
		return r.Name
	}
	return string(p)
}

// connectPermission is what managing provider's apps and connections
// needs: ads:write for an ad network, channels:write otherwise.
func connectPermission(p platform.Provider) Permission {
	if _, ok := ads.NetworkOf(p); ok {
		return PermAdsWrite
	}
	return PermChannelsWrite
}

// ConnectRedirectURI is the address to register with a platform's app.
func (s *Service) ConnectRedirectURI(p platform.Provider) string {
	return s.cfg.BaseURL + "/connect/" + string(p) + "/callback"
}

// ProviderApps lists the org's developer apps (never their secrets).
func (s *Service) ProviderApps(ctx context.Context, a Actor) ([]*model.ProviderApp, error) {
	if err := a.require(PermChannelsWrite); err != nil {
		return nil, err
	}
	return s.store.ProviderApps(ctx, a.OrgID)
}

// CreateProviderApp registers a developer app; its secret is encrypted.
func (s *Service) CreateProviderApp(ctx context.Context, a Actor, in ProviderAppInput) (*model.ProviderApp, error) {
	if err := a.require(connectPermission(in.Provider)); err != nil {
		return nil, err
	}
	var ps apperr.Problems
	if _, ok := s.connector(in.Provider); !ok {
		ps.Add("provider_invalid", "provider", "%q does not connect with OAuth.", in.Provider)
	}
	in.Name, in.ClientID, in.ClientSecret = strings.TrimSpace(in.Name), strings.TrimSpace(in.ClientID), strings.TrimSpace(in.ClientSecret)
	if in.Name == "" {
		in.Name = s.ProviderName(in.Provider) + " app"
	}
	if len(in.Name) > 100 {
		ps.Add("name_invalid", "name", "Names are at most 100 characters.")
	}
	if in.ClientID == "" || len(in.ClientID) > 500 {
		ps.Add("client_id_invalid", "client_id", "Give the app's client ID (app ID).")
	}
	if in.ClientSecret == "" || len(in.ClientSecret) > 500 {
		ps.Add("client_secret_invalid", "client_secret", "Give the app's client secret (app secret).")
	}
	if err := ps.Err("The app is not valid."); err != nil {
		return nil, err
	}
	app := &model.ProviderApp{ID: id.New(), OrgID: a.OrgID, Provider: in.Provider, Name: in.Name, ClientID: in.ClientID, CreatedBy: a.UserID}
	sealed, err := s.keys.Encrypt(ctx, a.OrgID, appSecretAAD(app.ID), []byte(in.ClientSecret))
	if err != nil {
		return nil, err
	}
	app.ClientSecret = sealed
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateProviderApp(ctx, app); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Invalid("name_taken", "name", "Another %s app is called %q.", in.Provider, in.Name)
			}
			return err
		}
		return s.audit(ctx, tx, a, "provider_app.create", id.Format(id.ProviderApp, app.ID), map[string]any{"provider": in.Provider})
	})
	return app, err
}

// DeleteProviderApp removes an app. Its channels keep working until their
// tokens need refreshing, then need reconnecting through another app.
// RenameProviderApp changes an app's name, which is only Araldo's label
// for it (one app may post for some accounts and read ads for others).
func (s *Service) RenameProviderApp(ctx context.Context, a Actor, appID uuid.UUID, name string) (*model.ProviderApp, error) {
	app, err := s.store.ProviderApp(ctx, a.OrgID, appID)
	if err != nil {
		return nil, notFound(err, "app")
	}
	if err := a.require(connectPermission(app.Provider)); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return nil, apperr.Invalid("name_invalid", "name", "An app needs a name of 1 to 100 characters.")
	}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.RenameProviderApp(ctx, a.OrgID, appID, name); err != nil {
			return notFound(err, "app")
		}
		return s.audit(ctx, tx, a, "provider_app.rename", id.Format(id.ProviderApp, appID), map[string]any{"name": name})
	})
	app.Name = name
	return app, err
}

func (s *Service) DeleteProviderApp(ctx context.Context, a Actor, appID uuid.UUID) error {
	app, err := s.store.ProviderApp(ctx, a.OrgID, appID)
	if err != nil {
		return notFound(err, "app")
	}
	if err := a.require(connectPermission(app.Provider)); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteProviderApp(ctx, a.OrgID, appID); err != nil {
			return notFound(err, "app")
		}
		return s.audit(ctx, tx, a, "provider_app.delete", id.Format(id.ProviderApp, appID), nil)
	})
}

func (s *Service) appCredentials(ctx context.Context, app *model.ProviderApp) (platform.App, error) {
	return appCredentialsWith(ctx, s.keys, app)
}

func appCredentialsWith(ctx context.Context, keys *keyring.Keyring, app *model.ProviderApp) (platform.App, error) {
	secret, err := keys.Decrypt(ctx, app.OrgID, appSecretAAD(app.ID), app.ClientSecret)
	if err != nil {
		return platform.App{}, err
	}
	return platform.App{ClientID: app.ClientID, ClientSecret: string(secret)}, nil
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func stateHash(state string) []byte {
	sum := sha256.Sum256([]byte(state))
	return sum[:]
}

// BeginConnect starts a sign-in for a brand through an app, and returns the
// platform's address to send the member to. Only members connect, in live
// mode.
func (s *Service) BeginConnect(ctx context.Context, a Actor, brandID, appID uuid.UUID) (string, error) {
	app, err := s.store.ProviderApp(ctx, a.OrgID, appID)
	if err != nil {
		return "", notFound(err, "app")
	}
	if err := a.require(connectPermission(app.Provider)); err != nil {
		return "", err
	}
	if a.UserID == nil {
		return "", apperr.Forbidden("A member connects channels with OAuth, in the dashboard.")
	}
	if !a.Livemode {
		return "", apperr.Invalid("livemode_required", "provider", "Test mode only has sandbox channels. Switch to live mode to connect an account.")
	}
	if _, err := s.Brand(ctx, a, brandID); err != nil {
		return "", err
	}
	conn, ok := s.connector(app.Provider)
	if !ok {
		return "", apperr.Invalid("provider_invalid", "app", "%s does not connect with OAuth.", app.Provider)
	}
	creds, err := s.appCredentials(ctx, app)
	if err != nil {
		return "", err
	}
	state, verifier := randomToken(), randomToken()
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if err := s.store.CreateOAuthState(ctx, stateHash(state), &model.OAuthState{OrgID: a.OrgID, UserID: *a.UserID, BrandID: brandID,
		AppID: app.ID, Provider: app.Provider, Verifier: verifier}); err != nil {
		return "", err
	}
	return conn.AuthorizeURL(creds, s.ConnectRedirectURI(app.Provider), state, challenge), nil
}

// ConnectResult is what a finished sign-in did: connected channels or ad
// accounts, or accounts to choose among.
type ConnectResult struct {
	Channels   []*model.Channel
	AdAccounts []*model.AdAccount
	Choices    []platform.Account
	// State goes back with the choice.
	State string
}

var errConnectExpired = apperr.Invalid("connect_expired", "state", "This sign-in expired or was already used. Start again from Channels.")

// oauthState loads the actor's sign-in for provider.
func (s *Service) oauthState(ctx context.Context, a Actor, provider platform.Provider, state string) (*model.OAuthState, error) {
	if a.UserID == nil || state == "" {
		return nil, errConnectExpired
	}
	st, err := s.store.OAuthState(ctx, stateHash(state), s.Now().Add(-OAuthStateTTL))
	if err != nil || st.OrgID != a.OrgID || st.UserID != *a.UserID || st.Provider != provider {
		return nil, errConnectExpired
	}
	return st, nil
}

func connectionsAAD(state []byte) string {
	return "araldo:v1:oauth_states:connections:" + hex.EncodeToString(state)
}

// FinishConnect completes a sign-in with the code the platform sent back.
// One account becomes a channel at once; several wait for ChooseConnections.
func (s *Service) FinishConnect(ctx context.Context, a Actor, provider platform.Provider, state, code string) (*ConnectResult, error) {
	st, err := s.oauthState(ctx, a, provider, state)
	if err != nil {
		return nil, err
	}
	hash := stateHash(state)
	if len(st.Connections) > 0 {
		return nil, errConnectExpired // the code was already exchanged
	}
	app, err := s.store.ProviderApp(ctx, a.OrgID, st.AppID)
	if err != nil {
		return nil, notFound(err, "app")
	}
	creds, err := s.appCredentials(ctx, app)
	if err != nil {
		return nil, err
	}
	conn, ok := s.connector(provider)
	if !ok || code == "" {
		_ = s.store.DeleteOAuthState(ctx, hash)
		return nil, apperr.Invalid("connect_failed", "code", "The platform did not grant access.")
	}
	conns, err := conn.Exchange(ctx, creds, s.ConnectRedirectURI(provider), code, st.Verifier)
	if err != nil {
		_ = s.store.DeleteOAuthState(ctx, hash)
		return nil, connectError(provider, err)
	}
	if len(conns) == 0 {
		_ = s.store.DeleteOAuthState(ctx, hash)
		return nil, apperr.Invalid("connect_empty", "code", "%s returned no account to connect.", provider)
	}
	if len(conns) == 1 {
		res, err := s.connectChosen(ctx, a, st, conns)
		if err == nil {
			_ = s.store.DeleteOAuthState(ctx, hash)
		}
		return res, err
	}
	raw, err := json.Marshal(conns)
	if err != nil {
		return nil, err
	}
	sealed, err := s.keys.Encrypt(ctx, a.OrgID, connectionsAAD(hash), raw)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetOAuthConnections(ctx, hash, sealed); err != nil {
		return nil, err
	}
	res := &ConnectResult{State: state}
	for _, c := range conns {
		res.Choices = append(res.Choices, c.Account)
	}
	return res, nil
}

// connectChosen connects a sign-in's accounts: as ad accounts for an ad
// network, as channels otherwise.
func (s *Service) connectChosen(ctx context.Context, a Actor, st *model.OAuthState, conns []platform.Connection) (*ConnectResult, error) {
	if n, ok := ads.NetworkOf(st.Provider); ok {
		accts, err := s.connectAdAccounts(ctx, a, st, n, conns)
		return &ConnectResult{AdAccounts: accts}, err
	}
	chs, err := s.connectAll(ctx, a, st, conns)
	return &ConnectResult{Channels: chs}, err
}

// ChooseConnections connects the accounts chosen (by external ID) from a
// sign-in that returned several.
func (s *Service) ChooseConnections(ctx context.Context, a Actor, provider platform.Provider, state string, externalIDs []string) (*ConnectResult, error) {
	st, err := s.oauthState(ctx, a, provider, state)
	if err != nil || len(st.Connections) == 0 {
		return nil, errConnectExpired
	}
	hash := stateHash(state)
	raw, err := s.keys.Decrypt(ctx, a.OrgID, connectionsAAD(hash), st.Connections)
	if err != nil {
		return nil, err
	}
	var conns, chosen []platform.Connection
	if err := json.Unmarshal(raw, &conns); err != nil {
		return nil, err
	}
	for _, c := range conns {
		if slices.Contains(externalIDs, c.Account.ExternalID) {
			chosen = append(chosen, c)
		}
	}
	if len(chosen) == 0 {
		return nil, apperr.Invalid("choice_required", "accounts", "Choose at least one account to connect.")
	}
	res, err := s.connectChosen(ctx, a, st, chosen)
	if err == nil {
		_ = s.store.DeleteOAuthState(ctx, hash)
	}
	return res, err
}

// connectAll turns connections into channels of the sign-in's brand. An
// account the brand already has is reconnected rather than added twice.
func (s *Service) connectAll(ctx context.Context, a Actor, st *model.OAuthState, conns []platform.Connection) ([]*model.Channel, error) {
	existing, err := s.store.Channels(ctx, a.OrgID, true, &st.BrandID)
	if err != nil {
		return nil, err
	}
	var out []*model.Channel
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		for _, c := range conns {
			ch := &model.Channel{ID: id.New(), OrgID: a.OrgID, BrandID: st.BrandID, Livemode: true, Provider: st.Provider,
				Settings: map[string]string{}, Status: model.ChannelActive}
			for _, e := range existing {
				if e.Provider == st.Provider && e.ExternalID == c.Account.ExternalID {
					ch = e
				}
			}
			ch.DisplayName, ch.Handle, ch.ExternalID, ch.ProfileURL = c.Account.DisplayName, c.Account.Handle, c.Account.ExternalID, c.Account.URL
			ch.Status, ch.StatusNote, ch.AppID, ch.TokenExpiresAt = model.ChannelActive, "", &st.AppID, c.ExpiresAt
			raw, err := json.Marshal(c.Credentials)
			if err != nil {
				return err
			}
			if ch.Credentials, err = s.keys.Encrypt(ctx, a.OrgID, credentialsAAD(ch.ID), raw); err != nil {
				return err
			}
			action, event := "channel.connect", "channel.connected"
			if slices.ContainsFunc(existing, func(e *model.Channel) bool { return e.ID == ch.ID }) {
				action, event = "channel.reconnect", ""
				err = tx.UpdateChannelConnection(ctx, ch)
			} else {
				err = tx.CreateChannel(ctx, ch)
			}
			if err != nil {
				return err
			}
			if err := s.audit(ctx, tx, a, action, id.Format(id.Channel, ch.ID), map[string]any{"provider": st.Provider}); err != nil {
				return err
			}
			if event != "" {
				if err := s.emit(ctx, tx, a.OrgID, true, a.RequestID, event, ViewChannel(ch)); err != nil {
					return err
				}
			}
			out = append(out, ch)
		}
		return nil
	})
	return out, err
}

// RefreshTokens renews the tokens of channels expiring within
// RefreshAhead. A token the platform refuses marks its channel
// needs_reauth.
func (s *Service) RefreshTokens(ctx context.Context) (int, error) {
	return s.refreshTokens(ctx, nil)
}

func (s *Service) refreshTokens(ctx context.Context, orgID *uuid.UUID) (int, error) {
	now := s.Now()
	chs, err := s.store.ChannelsExpiring(ctx, orgID, now.Add(RefreshAhead), 100)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ch := range chs {
		adapter, ok := s.platforms.Get(ch.Provider)
		refresher, canRefresh := adapter.(platform.Refresher)
		if !ok || !canRefresh || ch.AppID == nil {
			continue
		}
		err := s.refreshOne(ctx, ch, refresher)
		var pe *platform.Error
		switch {
		case err == nil:
			n++
		case errors.Is(err, platform.ErrNoRefresh):
			if err := s.cannotRefresh(ctx, ch, now); err != nil {
				return n, err
			}
		case errors.As(err, &pe) && pe.Kind == platform.AuthRevoked, errors.Is(err, store.ErrNotFound):
			if err := s.needsReauth(ctx, ch, "The token could not be renewed: reconnect the account."); err != nil {
				return n, err
			}
		default:
			s.log.WarnContext(ctx, "refreshing a token failed; will retry", "channel", id.Format(id.Channel, ch.ID), "err", err)
		}
	}
	return n, nil
}

// refreshOne renews a channel's token with the channel locked, so two
// renewals never race: some platforms (X) replace the refresh token each
// time, and a second renewal with the spent one would read as revoked. If
// the token changed while waiting for the lock, it was just renewed and is
// left alone.
func (s *Service) refreshOne(ctx context.Context, ch *model.Channel, r platform.Refresher) error {
	return s.store.InTx(ctx, func(tx *store.Store) error {
		locked, err := tx.ChannelForUpdate(ctx, ch.OrgID, ch.ID)
		if err != nil {
			return err
		}
		if !sameTime(locked.TokenExpiresAt, ch.TokenExpiresAt) || locked.AppID == nil {
			return nil
		}
		// Keys through the transaction too: see keyring.With.
		keys := s.keys.With(tx)
		app, err := tx.ProviderApp(ctx, locked.OrgID, *locked.AppID)
		if err != nil {
			return err
		}
		appCreds, err := appCredentialsWith(ctx, keys, app)
		if err != nil {
			return err
		}
		creds, err := credentialsWith(ctx, keys, locked)
		if err != nil {
			return err
		}
		fresh, expires, err := r.Refresh(ctx, appCreds, creds)
		if err != nil {
			return err
		}
		// The new tokens replace the old; the rest of what is stored (a Page
		// or board ID) stays. Settings, kept apart, are not copied in.
		secrets, err := secretsWith(ctx, keys, locked)
		if err != nil {
			return err
		}
		for k, v := range fresh {
			secrets[k] = v
		}
		raw, err := json.Marshal(secrets)
		if err != nil {
			return err
		}
		sealed, err := keys.Encrypt(ctx, locked.OrgID, credentialsAAD(locked.ID), raw)
		if err != nil {
			return err
		}
		return tx.SetChannelToken(ctx, locked.OrgID, locked.ID, sealed, expires)
	})
}

// sameTime compares expiries at Postgres's precision (microseconds), so a
// time not yet read back from the database matches its stored copy.
func sameTime(a, b *time.Time) bool {
	return (a == nil) == (b == nil) && (a == nil || a.Truncate(time.Microsecond).Equal(b.Truncate(time.Microsecond)))
}

// cannotRefresh handles a token the platform gives no way to renew (a
// LinkedIn token without a refresh token): the channel says when to sign
// in again, and needs it once the token has expired.
func (s *Service) cannotRefresh(ctx context.Context, ch *model.Channel, now time.Time) error {
	if ch.TokenExpiresAt == nil {
		return nil
	}
	if !ch.TokenExpiresAt.After(now) {
		return s.needsReauth(ctx, ch, "The token expired: sign in again.")
	}
	note := fmt.Sprintf("Sign in again before %s: this token cannot be renewed.", ch.TokenExpiresAt.UTC().Format("Jan 2, 2006 15:04 UTC"))
	if ch.StatusNote == note {
		return nil
	}
	return s.store.SetChannelStatus(ctx, ch.OrgID, ch.ID, ch.Status, note)
}

// usableCredentials are a channel's credentials, its token renewed first
// if it is about to expire.
func (s *Service) usableCredentials(ctx context.Context, ch *model.Channel, adapter platform.Adapter) (platform.Credentials, error) {
	r, canRefresh := adapter.(platform.Refresher)
	if canRefresh && ch.AppID != nil && ch.TokenExpiresAt != nil && ch.TokenExpiresAt.Before(s.Now().Add(UseAhead)) {
		if err := s.refreshOne(ctx, ch, r); err != nil && !errors.Is(err, platform.ErrNoRefresh) {
			// Use the token as it is; if it has expired, the platform says
			// so and the channel is marked for signing in again.
			s.log.WarnContext(ctx, "renewing a token before use failed", "channel", id.Format(id.Channel, ch.ID), "err", err)
		} else if fresh, err := s.store.Channel(ctx, ch.OrgID, ch.ID); err == nil {
			ch = fresh
		}
	}
	return s.credentials(ctx, ch)
}

// needsReauth marks a channel as needing reconnecting and says so.
func (s *Service) needsReauth(ctx context.Context, ch *model.Channel, note string) error {
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SetChannelStatus(ctx, ch.OrgID, ch.ID, model.ChannelNeedsReauth, note); err != nil {
			return err
		}
		ch.Status, ch.StatusNote = model.ChannelNeedsReauth, note
		return s.emit(ctx, tx, ch.OrgID, ch.Livemode, "", "channel.needs_reauth", ViewChannel(ch))
	})
}
