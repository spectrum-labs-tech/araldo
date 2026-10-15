// SPDX-License-Identifier: AGPL-3.0-or-later

package bluesky

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Signing in (createSession) is rate limited hard, per account and per
// address: 30 every five minutes and 300 a day
// (https://docs.bsky.app/docs/advanced-guides/rate-limits). So a session is
// kept in memory and renewed with its refresh token, and the adapter signs
// in again only when it has none or renewing fails.

const (
	// maxSessions bounds the cache; past it the session that expires first
	// is forgotten.
	maxSessions = 1024
	// renewAhead renews an access token this long before it expires.
	renewAhead = 5 * time.Minute
	// unknownLife is how long an access token whose expiry cannot be read
	// is trusted.
	unknownLife = 15 * time.Minute
)

// sessionKey identifies a channel's credentials without holding them: a
// hash of the service, identifier and app password, so new credentials
// never reuse an old session.
type sessionKey [sha256.Size]byte

func keyOf(c platform.Credentials) sessionKey {
	h := sha256.New()
	for _, v := range []string{service(c), c["identifier"], c["app_password"]} {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	var k sessionKey
	copy(k[:], h.Sum(nil))
	return k
}

// cachedSession is one channel's session. mu is held while it is signed in
// or renewed, so two publishes on a channel never both sign in, and a
// refresh token (which renewing spends) is never used twice.
type cachedSession struct {
	mu      sync.Mutex
	s       session
	expires time.Time
}

// sessionCache holds sessions by credentials. It never logs, and never
// returns a token to anything but the adapter.
type sessionCache struct {
	mu sync.Mutex
	m  map[sessionKey]*cachedSession
}

func (c *sessionCache) entry(k sessionKey) *cachedSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[sessionKey]*cachedSession{}
	}
	if e, ok := c.m[k]; ok {
		return e
	}
	if len(c.m) >= maxSessions {
		var oldest sessionKey
		var at time.Time
		first := true
		for key, e := range c.m {
			if exp := e.peekExpiry(); first || exp.Before(at) {
				oldest, at, first = key, exp, false
			}
		}
		delete(c.m, oldest)
	}
	e := &cachedSession{}
	c.m[k] = e
	return e
}

// peekExpiry reads an entry's expiry without waiting for a sign-in under
// way (which counts as new).
func (e *cachedSession) peekExpiry() time.Time {
	if !e.mu.TryLock() {
		return time.Now().Add(24 * time.Hour)
	}
	defer e.mu.Unlock()
	return e.expires
}

// forget drops the session k holds if it is still the one with access
// token jwt.
func (c *sessionCache) forget(k sessionKey, jwt string) {
	c.mu.Lock()
	e := c.m[k]
	c.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.s.AccessJwt == jwt {
		e.s, e.expires = session{}, time.Time{}
	}
}

func (c *sessionCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// session returns a signed-in session for c: the cached one while its
// access token is good, else one renewed with the refresh token, else a new
// sign-in. cached reports that the session was not checked just now, so a
// refusal of its token may mean only that it went stale.
func (a *Adapter) session(ctx context.Context, c platform.Credentials) (s session, cached bool, err error) {
	if c["identifier"] == "" || c["app_password"] == "" {
		return session{}, false, platform.Errorf(platform.AuthRevoked, "identifier and app password are required")
	}
	e := a.sessions.entry(keyOf(c))
	e.mu.Lock()
	defer e.mu.Unlock()
	now := a.Now()
	if e.s.AccessJwt != "" && now.Before(e.expires.Add(-renewAhead)) {
		return e.s, true, nil
	}
	if e.s.RefreshJwt != "" {
		fresh, err := a.refresh(ctx, c, e.s)
		if err == nil {
			e.s, e.expires = fresh, expiry(fresh.AccessJwt, now)
			return fresh, false, nil
		}
		if k := platform.KindOf(err); k == platform.Transient || k == platform.RateLimited {
			return session{}, false, err
		}
		// The refresh token was refused or spent: sign in again.
	}
	e.s, e.expires = session{}, time.Time{}
	s, err = a.login(ctx, c)
	if err != nil {
		return session{}, false, err
	}
	e.s, e.expires = s, expiry(s.AccessJwt, now)
	return s, false, nil
}

// refresh renews s with its refresh token
// (https://docs.bsky.app/docs/api/com-atproto-server-refresh-session).
func (a *Adapter) refresh(ctx context.Context, c platform.Credentials, s session) (session, error) {
	var out session
	err := platform.JSON(platform.RetrySafe(ctx), a.Client, http.MethodPost, service(c)+"/xrpc/com.atproto.server.refreshSession",
		map[string]string{"Authorization": "Bearer " + s.RefreshJwt}, nil, &out)
	if err != nil {
		return session{}, err
	}
	if out.AccessJwt == "" {
		return session{}, platform.Errorf(platform.Rejected, "refreshSession returned no token")
	}
	if len(out.DIDDoc.Service) == 0 {
		out.DIDDoc = s.DIDDoc
	}
	if out.RefreshJwt == "" {
		out.RefreshJwt = s.RefreshJwt
	}
	return out, nil
}

// stale reports whether err refused a session's token for being expired
// or no longer valid (401, or 400 ExpiredToken / InvalidToken).
func stale(err error) bool {
	var pe *platform.Error
	if !errors.As(err, &pe) {
		return false
	}
	body := string(pe.Body)
	return pe.Status == http.StatusUnauthorized ||
		pe.Status == http.StatusBadRequest && (strings.Contains(body, "ExpiredToken") || strings.Contains(body, "InvalidToken"))
}

// expiry reads when an access token (a JWT) expires. It only decides when
// to renew, so the token is not verified; if it cannot be read, it is
// trusted for unknownLife.
func expiry(jwt string, now time.Time) time.Time {
	parts := strings.Split(jwt, ".")
	if len(parts) == 3 {
		if raw, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims struct {
				Exp int64 `json:"exp"`
			}
			if json.Unmarshal(raw, &claims) == nil && claims.Exp > 0 {
				return time.Unix(claims.Exp, 0)
			}
		}
	}
	return now.Add(unknownLife)
}
