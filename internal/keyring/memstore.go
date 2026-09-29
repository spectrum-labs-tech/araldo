// SPDX-License-Identifier: AGPL-3.0-or-later

package keyring

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// MemStore keeps data keys in memory, for tests and one-shot commands.
type MemStore struct {
	mu   sync.Mutex
	keys map[cacheKey]WrappedKey
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore { return &MemStore{keys: map[cacheKey]WrappedKey{}} }

func (s *MemStore) CurrentDataKey(_ context.Context, scope uuid.UUID) (WrappedKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	best := WrappedKey{}
	for ck, wk := range s.keys {
		if ck.scope == scope && wk.Version > best.Version {
			best = wk
		}
	}
	if best.Version == 0 {
		return WrappedKey{}, ErrNoKey
	}
	return best, nil
}

func (s *MemStore) DataKey(_ context.Context, scope uuid.UUID, version int) (WrappedKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wk, ok := s.keys[cacheKey{scope, version}]
	if !ok {
		return WrappedKey{}, ErrNoKey
	}
	return wk, nil
}

func (s *MemStore) InsertDataKey(_ context.Context, k WrappedKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := cacheKey{k.Scope, k.Version}
	if _, ok := s.keys[ck]; ok {
		return ErrConflict
	}
	s.keys[ck] = k
	return nil
}

func (s *MemStore) DataKeys(_ context.Context) ([]WrappedKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WrappedKey, 0, len(s.keys))
	for _, wk := range s.keys {
		out = append(out, wk)
	}
	return out, nil
}

func (s *MemStore) RewrapDataKey(_ context.Context, k WrappedKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := cacheKey{k.Scope, k.Version}
	if _, ok := s.keys[ck]; !ok {
		return ErrNoKey
	}
	s.keys[ck] = k
	return nil
}

// Delete removes every key of a scope (crypto-shredding in tests).
func (s *MemStore) Delete(scope uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ck := range s.keys {
		if ck.scope == scope {
			delete(s.keys, ck)
		}
	}
}
