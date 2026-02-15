package state

import (
	"sync"
	"time"
)

type PlayerStore struct {
	mu      sync.RWMutex
	players map[uint32]Player
}

type Player struct {
	DPNID       uint32
	ConnectedAt time.Time
	EvictedAt   time.Time
}

func NewPlayerStore() *PlayerStore {
	return &PlayerStore{players: map[uint32]Player{}}
}

func (s *PlayerStore) Upsert(dpnid uint32, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// If a player was evicted, keep them evicted until a fresh DP8 connect occurs
	// (which would create a new DPNID). This enforces a hard session cap.
	if p, ok := s.players[dpnid]; ok && !p.EvictedAt.IsZero() {
		return
	}
	s.players[dpnid] = Player{DPNID: dpnid, ConnectedAt: now}
}

func (s *PlayerStore) Remove(dpnid uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.players[dpnid]
	delete(s.players, dpnid)
	return ok
}

func (s *PlayerStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, p := range s.players {
		if p.EvictedAt.IsZero() {
			n++
		}
	}
	return n
}

func (s *PlayerStore) IsEvicted(dpnid uint32) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.players[dpnid]
	return ok && !p.EvictedAt.IsZero()
}

func (s *PlayerStore) TouchEvict(dpnid uint32, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.players[dpnid]
	if !ok {
		return false
	}
	if !p.EvictedAt.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	p.EvictedAt = now
	s.players[dpnid] = p
	return true
}

// SweepEvict evicts players connected longer than maxAge.
// Returns the list of DPNIDs newly evicted in this sweep.
func (s *PlayerStore) SweepEvict(now time.Time, maxAge time.Duration) []uint32 {
	if maxAge <= 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var evicted []uint32
	for dpnid, p := range s.players {
		if !p.EvictedAt.IsZero() {
			continue
		}
		if p.ConnectedAt.IsZero() {
			// Defensive: treat unknown age as immediately evictable.
			p.EvictedAt = now
			s.players[dpnid] = p
			evicted = append(evicted, dpnid)
			continue
		}
		if now.Sub(p.ConnectedAt) >= maxAge {
			p.EvictedAt = now
			s.players[dpnid] = p
			evicted = append(evicted, dpnid)
		}
	}
	return evicted
}
