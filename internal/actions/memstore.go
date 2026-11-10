package actions

import (
	"context"
	"sync"
	"time"
)

// MemoryStore keeps pending actions in memory. It suits tests and
// single-process setups without a data directory; tokens do not survive a
// restart.
type MemoryStore struct {
	mu sync.Mutex
	m  map[string]Pending
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string]Pending{}} }

func (s *MemoryStore) SavePending(_ context.Context, a Pending) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[a.ID] = a
	return nil
}

func (s *MemoryStore) LoadPending(_ context.Context, id string) (Pending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[id]
	if !ok {
		return Pending{}, ErrNotFound
	}
	return a, nil
}

func (s *MemoryStore) ConsumePending(_ context.Context, id string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[id]
	if !ok || a.ConsumedAt != nil {
		return false, nil
	}
	a.ConsumedAt = &at
	a.ParamsJSON = "{}"
	s.m[a.ID] = a
	return true, nil
}
