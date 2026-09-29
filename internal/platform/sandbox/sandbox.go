// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sandbox is the test-mode platform (ADR 0006). A sandbox channel
// imitates a real platform's rules but publishes nowhere: the post's
// permalink points at Araldo's own sandbox page. Failures can be simulated
// through a post's metadata.
package sandbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Simulations accepted in metadata.araldo_simulate.
const (
	SimRateLimited      = "rate_limited"
	SimAuthRevoked      = "auth_revoked"
	SimRejected         = "rejected"
	SimTimeoutAfterSend = "timeout_after_send"
	SimSlow             = "slow"
)

// Simulations lists every accepted simulation.
var Simulations = []string{SimRateLimited, SimAuthRevoked, SimRejected, SimTimeoutAfterSend, SimSlow}

// Adapter is the sandbox platform.
type Adapter struct {
	// BaseURL is the Araldo server's public URL; permalinks go to
	// BaseURL/sandbox/<key>.
	BaseURL string
	// Sleep waits for the "slow" simulation; tests replace it.
	Sleep func(context.Context, time.Duration) error
}

// New returns a sandbox adapter linking to baseURL.
func New(baseURL string) *Adapter {
	return &Adapter{BaseURL: strings.TrimRight(baseURL, "/"), Sleep: sleep}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (a *Adapter) Provider() platform.Provider { return platform.Sandbox }

// Rules are permissive; a sandbox channel is checked against the rules of
// the platform it emulates.
func (a *Adapter) Rules() platform.Rules {
	return platform.Rules{Provider: platform.Sandbox, Name: "Sandbox", MaxLength: 100_000, Counting: platform.CountRunes, Threads: true, MaxThreadParts: 100, MaxMedia: 100}
}

func (a *Adapter) Fields() []platform.Field {
	return []platform.Field{{Name: "emulates", Label: "Platform to imitate", Help: "Its rules apply to every post", Default: string(platform.Bluesky)}}
}

func (a *Adapter) Verify(_ context.Context, c platform.Credentials) (platform.Account, error) {
	p := platform.Provider(c["emulates"])
	r, ok := platform.RulesFor(p)
	if !ok {
		return platform.Account{}, platform.Errorf(platform.Rejected, "sandbox cannot imitate %q", c["emulates"])
	}
	return platform.Account{ExternalID: "sandbox-" + string(p), Handle: "sandbox", DisplayName: "Sandbox " + r.Name}, nil
}

func (a *Adapter) Idempotent() bool { return false }

func (a *Adapter) Publish(ctx context.Context, _ platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	first := p.Attempt <= 1
	switch p.Simulate {
	case SimRateLimited:
		if first {
			return platform.Result{}, &platform.Error{Kind: platform.RateLimited, Code: "rate_limited", Msg: "simulated rate limit", RetryAfter: 5 * time.Second}
		}
	case SimAuthRevoked:
		return platform.Result{}, &platform.Error{Kind: platform.AuthRevoked, Code: "unauthorized", Msg: "simulated revoked authorization"}
	case SimRejected:
		return platform.Result{}, &platform.Error{Kind: platform.Rejected, Code: "rejected", Msg: "simulated rejection by the platform"}
	case SimTimeoutAfterSend:
		if first {
			return platform.Result{}, &platform.Error{Kind: platform.Uncertain, Code: "network", Msg: "simulated timeout after sending"}
		}
	case SimSlow:
		if err := a.Sleep(ctx, 3*time.Second); err != nil {
			return platform.Result{}, &platform.Error{Kind: platform.Transient, Code: "network", Err: err}
		}
	}
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	for i := len(p.Posted); i < len(p.Parts); i++ {
		ref := platform.RemoteRef{ID: fmt.Sprintf("sbx_%s_%d", p.Key, i+1), URL: fmt.Sprintf("%s/sandbox/%s#part-%d", a.BaseURL, p.Key, i+1)}
		if onPart != nil {
			if err := onPart(ref); err != nil {
				return res, err
			}
		}
		res.Parts = append(res.Parts, ref)
	}
	res.Permalink = a.BaseURL + "/sandbox/" + p.Key
	return res, nil
}
