// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// The request log (ADR 0032): each authenticated API request, kept briefly
// so an org's developers can see what their integration sent and got back.

// RequestLogRetention is how long logged requests are kept.
const RequestLogRetention = 14 * 24 * time.Hour

const (
	// requestLogBuffer bounds the requests waiting to be written; past it,
	// requests go unlogged rather than slow the API down.
	requestLogBuffer = 4096
	requestLogBatch  = 200
	requestLogEvery  = 500 * time.Millisecond
)

// requestLog writes logged requests in batches, in the background.
type requestLog struct {
	once    sync.Once
	ch      chan model.APIRequest
	dropped atomic.Int64
}

// LogRequest queues a request for the log. It never blocks: with the
// buffer full, the request is dropped and counted. The writer outlives the
// request that starts it, so it keeps its context's values, not its end.
func (s *Service) LogRequest(ctx context.Context, r model.APIRequest) {
	s.requests.once.Do(func() {
		s.requests.ch = make(chan model.APIRequest, requestLogBuffer)
		go s.writeRequestLog(context.WithoutCancel(ctx))
	})
	select {
	case s.requests.ch <- r:
	default:
		if n := s.requests.dropped.Add(1); n == 1 || n%1000 == 0 {
			s.log.Warn("the request log is behind; dropping requests from it", "dropped", n)
		}
	}
}

func (s *Service) writeRequestLog(ctx context.Context) {
	batch := make([]model.APIRequest, 0, requestLogBatch)
	tick := time.NewTicker(requestLogEvery)
	defer tick.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := s.store.InsertAPIRequests(wctx, batch); err != nil {
			s.log.WarnContext(wctx, "writing the request log failed; those requests are not logged", "requests", len(batch), "err", err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case r := <-s.requests.ch:
			if batch = append(batch, r); len(batch) == requestLogBatch {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

// APIRequests lists the actor's org's logged requests in its mode, newest
// first. Those who manage API keys read them.
func (s *Service) APIRequests(ctx context.Context, a Actor, f store.RequestFilter, p store.Page) ([]model.APIRequest, bool, error) {
	if err := a.requireToRead(PermKeysWrite); err != nil {
		return nil, false, err
	}
	return s.store.APIRequests(ctx, a.OrgID, a.Livemode, f, p)
}

// PruneAPIRequests deletes logged requests past RequestLogRetention.
func (s *Service) PruneAPIRequests(ctx context.Context) (int, error) {
	n := 0
	for {
		k, err := s.store.PruneAPIRequests(ctx, s.Now().Add(-RequestLogRetention), 5000)
		n += k
		if err != nil || k < 5000 {
			return n, err
		}
	}
}
