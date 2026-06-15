package main

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// perEventOverhead approximates the non-payload heap cost of a stored event
// (struct headers, map/index bookkeeping, allocator rounding) so the byte
// budget tracks real memory rather than just string lengths.
const perEventOverhead = 320

// memEvent is a stored event plus accounting metadata.
type memEvent struct {
	evt     *nostr.Event
	size    int64
	addedAt time.Time // arrival time; monotonic in insertion order, drives eviction
}

// MemStore is an in-memory, ephemeral event store tailored to NIP-46 routing.
// Inserts are O(1) and indexed by recipient (#p tag) and author so reconnect
// catch-up queries don't scan the whole buffer. It self-evicts on a time
// window and a total byte budget, giving a predictable memory ceiling.
type MemStore struct {
	mu         sync.RWMutex
	events     map[string]*memEvent           // id -> event (authoritative)
	order      []string                       // ids oldest-first, for eviction
	byP        map[string]map[string]struct{} // #p tag value -> set of ids
	byAuthor   map[string]map[string]struct{} // author pubkey -> set of ids
	totalBytes int64
	evictions  uint64 // cumulative count of events dropped by eviction
	maxBytes   int64
	ttl        time.Duration
	stop       chan struct{}
}

// MemStats is a point-in-time snapshot of the store's footprint.
type MemStats struct {
	Events    int
	Bytes     int64
	Evictions uint64
}

// Stats returns the current buffer footprint for observability.
func (s *MemStore) Stats() MemStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return MemStats{Events: len(s.events), Bytes: s.totalBytes, Evictions: s.evictions}
}

// NewMemStore creates a store with the given byte budget and retention window.
func NewMemStore(maxBytes int64, ttl time.Duration) *MemStore {
	return &MemStore{maxBytes: maxBytes, ttl: ttl}
}

// Init satisfies the EventStore interface and starts the background evictor.
func (s *MemStore) Init() error {
	s.events = make(map[string]*memEvent)
	s.byP = make(map[string]map[string]struct{})
	s.byAuthor = make(map[string]map[string]struct{})
	s.stop = make(chan struct{})
	go s.sweepLoop()
	return nil
}

// Close stops the background evictor.
func (s *MemStore) Close() {
	if s.stop != nil {
		close(s.stop)
	}
}

// SaveEvent stores an event. It never returns an error so live delivery (which
// khatru gates on a nil write result) is never blocked by the store.
func (s *MemStore) SaveEvent(_ context.Context, evt *nostr.Event) error {
	size := estimateSize(evt)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.events[evt.ID]; exists {
		return nil
	}

	me := &memEvent{evt: evt, size: size, addedAt: time.Now()}
	s.events[evt.ID] = me
	s.order = append(s.order, evt.ID)
	s.totalBytes += size
	s.indexLocked(evt.ID, evt)

	s.evictLocked(me.addedAt)
	return nil
}

// DeleteEvent removes an event if present.
func (s *MemStore) DeleteEvent(_ context.Context, evt *nostr.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if me, ok := s.events[evt.ID]; ok {
		s.removeLocked(evt.ID, me)
	}
	return nil
}

// QueryEvents returns events matching the filter, newest first, honoring Limit.
func (s *MemStore) QueryEvents(ctx context.Context, filter nostr.Filter) (chan *nostr.Event, error) {
	var matched []*nostr.Event

	s.mu.RLock()
	for id := range s.candidateIDsLocked(filter) {
		if me := s.events[id]; me != nil && filter.Matches(me.evt) {
			matched = append(matched, me.evt)
		}
	}
	s.mu.RUnlock()

	sort.Slice(matched, func(i, j int) bool {
		return matched[i].CreatedAt > matched[j].CreatedAt
	})
	if filter.Limit > 0 && len(matched) > filter.Limit {
		matched = matched[:filter.Limit]
	}

	ch := make(chan *nostr.Event, 1)
	go func() {
		defer close(ch)
		for _, e := range matched {
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// candidateIDsLocked narrows the search to events indexed under the filter's #p
// tags and authors. Callers hold at least an RLock. The relay's RejectFilter
// guarantees every accepted filter has authors and/or a #p tag.
func (s *MemStore) candidateIDsLocked(filter nostr.Filter) map[string]struct{} {
	out := make(map[string]struct{})
	for _, p := range filter.Tags["p"] {
		for id := range s.byP[p] {
			out[id] = struct{}{}
		}
	}
	for _, a := range filter.Authors {
		for id := range s.byAuthor[a] {
			out[id] = struct{}{}
		}
	}
	// Events are keyed by id, so an id-scoped query is a direct lookup.
	for _, id := range filter.IDs {
		if _, ok := s.events[id]; ok {
			out[id] = struct{}{}
		}
	}
	return out
}

// evictLocked drops oldest events while over the byte budget or past the TTL.
// order is oldest-first, so it stops at the first event worth keeping.
func (s *MemStore) evictLocked(now time.Time) {
	cutoff := now.Add(-s.ttl)
	i := 0
	for i < len(s.order) {
		id := s.order[i]
		me, ok := s.events[id]
		if !ok {
			i++ // already deleted; skip the stale order entry
			continue
		}
		overBudget := s.maxBytes > 0 && s.totalBytes > s.maxBytes
		expired := me.addedAt.Before(cutoff)
		if !overBudget && !expired {
			break
		}
		s.removeLocked(id, me)
		s.evictions++
		i++
	}
	if i > 0 {
		s.order = append(s.order[:0], s.order[i:]...)
	}
}

func (s *MemStore) sweepLoop() {
	interval := s.ttl / 4
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.mu.Lock()
			s.evictLocked(time.Now())
			s.mu.Unlock()
		case <-s.stop:
			return
		}
	}
}

func (s *MemStore) indexLocked(id string, evt *nostr.Event) {
	for _, tag := range evt.Tags {
		if len(tag) >= 2 && tag[0] == "p" {
			bucketAdd(s.byP, tag[1], id)
		}
	}
	bucketAdd(s.byAuthor, evt.PubKey, id)
}

func (s *MemStore) removeLocked(id string, me *memEvent) {
	delete(s.events, id)
	s.totalBytes -= me.size
	for _, tag := range me.evt.Tags {
		if len(tag) >= 2 && tag[0] == "p" {
			bucketDel(s.byP, tag[1], id)
		}
	}
	bucketDel(s.byAuthor, me.evt.PubKey, id)
}

func bucketAdd(m map[string]map[string]struct{}, key, id string) {
	b := m[key]
	if b == nil {
		b = make(map[string]struct{})
		m[key] = b
	}
	b[id] = struct{}{}
}

func bucketDel(m map[string]map[string]struct{}, key, id string) {
	if b := m[key]; b != nil {
		delete(b, id)
		if len(b) == 0 {
			delete(m, key)
		}
	}
}

// reportMemStats periodically logs the in-memory store's footprint so operators
// can see headroom against the configured budget. Runs for the lifetime of the
// process.
func reportMemStats(s *MemStore) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		st := s.Stats()
		log.Printf("Store: %d events, %d MB buffered, %d evicted (cumulative)",
			st.Events, st.Bytes>>20, st.Evictions)
	}
}

// estimateSize approximates the heap footprint of an event for budgeting.
func estimateSize(evt *nostr.Event) int64 {
	n := int64(len(evt.ID) + len(evt.PubKey) + len(evt.Sig) + len(evt.Content))
	for _, tag := range evt.Tags {
		for _, t := range tag {
			n += int64(len(t))
		}
	}
	return n + perEventOverhead
}
