package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

func newEvent(id, author, ptag string, createdAt nostr.Timestamp, content string) *nostr.Event {
	return &nostr.Event{
		ID:        id,
		PubKey:    author,
		CreatedAt: createdAt,
		Kind:      24133,
		Tags:      nostr.Tags{{"p", ptag}},
		Content:   content,
		Sig:       "sig",
	}
}

func newStore(t *testing.T, maxBytes int64, ttl time.Duration) *MemStore {
	t.Helper()
	s := NewMemStore(maxBytes, ttl)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func collect(t *testing.T, s *MemStore, filter nostr.Filter) []*nostr.Event {
	t.Helper()
	ch, err := s.QueryEvents(context.Background(), filter)
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	var out []*nostr.Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func ids(evs []*nostr.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

func TestQueryByRecipientTag(t *testing.T) {
	s := newStore(t, 0, time.Minute)
	s.SaveEvent(context.Background(), newEvent("id1", "signer", "alice", 100, "x"))

	got := collect(t, s, nostr.Filter{Kinds: []int{24133}, Tags: nostr.TagMap{"p": {"alice"}}})
	if len(got) != 1 || got[0].ID != "id1" {
		t.Fatalf("p=alice: got %v, want [id1]", ids(got))
	}

	if got := collect(t, s, nostr.Filter{Kinds: []int{24133}, Tags: nostr.TagMap{"p": {"bob"}}}); len(got) != 0 {
		t.Fatalf("p=bob: got %v, want none", ids(got))
	}
}

func TestQueryByAuthor(t *testing.T) {
	s := newStore(t, 0, time.Minute)
	s.SaveEvent(context.Background(), newEvent("id1", "signer", "alice", 100, "x"))

	if got := collect(t, s, nostr.Filter{Authors: []string{"signer"}}); len(got) != 1 {
		t.Fatalf("author=signer: got %v, want [id1]", ids(got))
	}
	if got := collect(t, s, nostr.Filter{Authors: []string{"other"}}); len(got) != 0 {
		t.Fatalf("author=other: got %v, want none", ids(got))
	}
}

func TestQueryByID(t *testing.T) {
	s := newStore(t, 0, time.Minute)
	s.SaveEvent(context.Background(), newEvent("id1", "signer", "alice", 100, "x"))

	if got := collect(t, s, nostr.Filter{IDs: []string{"id1"}}); len(got) != 1 || got[0].ID != "id1" {
		t.Fatalf("id=id1: got %v, want [id1]", ids(got))
	}
	if got := collect(t, s, nostr.Filter{IDs: []string{"missing"}}); len(got) != 0 {
		t.Fatalf("id=missing: got %v, want none", ids(got))
	}
}

func TestQueryKindMismatch(t *testing.T) {
	s := newStore(t, 0, time.Minute)
	s.SaveEvent(context.Background(), newEvent("id1", "signer", "alice", 100, "x"))

	got := collect(t, s, nostr.Filter{Kinds: []int{24135}, Tags: nostr.TagMap{"p": {"alice"}}})
	if len(got) != 0 {
		t.Fatalf("kind mismatch should match nothing, got %v", ids(got))
	}
}

func TestQueryOrderingAndLimit(t *testing.T) {
	s := newStore(t, 0, time.Minute)
	s.SaveEvent(context.Background(), newEvent("id1", "signer", "alice", 100, "x"))
	s.SaveEvent(context.Background(), newEvent("id3", "signer", "alice", 300, "x"))
	s.SaveEvent(context.Background(), newEvent("id2", "signer", "alice", 200, "x"))

	got := collect(t, s, nostr.Filter{Kinds: []int{24133}, Tags: nostr.TagMap{"p": {"alice"}}, Limit: 2})
	if len(got) != 2 || got[0].ID != "id3" || got[1].ID != "id2" {
		t.Fatalf("newest-first limit 2: got %v, want [id3 id2]", ids(got))
	}
}

func TestByteBudgetEviction(t *testing.T) {
	sample := newEvent("id1", "signer", "alice", 100, "hello")
	size := estimateSize(sample)
	s := newStore(t, 2*size, time.Minute)

	s.SaveEvent(context.Background(), newEvent("id1", "signer", "alice", 100, "hello"))
	s.SaveEvent(context.Background(), newEvent("id2", "signer", "alice", 200, "hello"))
	s.SaveEvent(context.Background(), newEvent("id3", "signer", "alice", 300, "hello"))

	st := s.Stats()
	if st.Events != 2 {
		t.Fatalf("events buffered: got %d, want 2", st.Events)
	}
	if st.Evictions != 1 {
		t.Fatalf("evictions: got %d, want 1", st.Evictions)
	}
	if st.Bytes > 2*size {
		t.Fatalf("totalBytes %d exceeds budget %d", st.Bytes, 2*size)
	}

	// Oldest (id1) should be the one dropped.
	got := collect(t, s, nostr.Filter{Kinds: []int{24133}, Tags: nostr.TagMap{"p": {"alice"}}})
	for _, e := range got {
		if e.ID == "id1" {
			t.Fatalf("id1 should have been evicted, still present: %v", ids(got))
		}
	}
}

func TestTTLEviction(t *testing.T) {
	ttl := 50 * time.Millisecond
	s := newStore(t, 0, ttl)

	s.SaveEvent(context.Background(), newEvent("old", "signer", "alice", 100, "x"))
	time.Sleep(ttl + 30*time.Millisecond)
	// Saving a fresh event triggers eviction of anything past the TTL.
	s.SaveEvent(context.Background(), newEvent("new", "signer", "alice", 200, "x"))

	got := collect(t, s, nostr.Filter{Kinds: []int{24133}, Tags: nostr.TagMap{"p": {"alice"}}})
	if len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("after TTL: got %v, want [new]", ids(got))
	}
}

func TestDeleteEvent(t *testing.T) {
	s := newStore(t, 0, time.Minute)
	ev := newEvent("id1", "signer", "alice", 100, "x")
	s.SaveEvent(context.Background(), ev)
	s.DeleteEvent(context.Background(), ev)

	if st := s.Stats(); st.Events != 0 || st.Bytes != 0 {
		t.Fatalf("after delete: events=%d bytes=%d, want 0/0", st.Events, st.Bytes)
	}
	// Index must be cleaned up too, by both query dimensions.
	if got := collect(t, s, nostr.Filter{Tags: nostr.TagMap{"p": {"alice"}}}); len(got) != 0 {
		t.Fatalf("p index not cleaned: %v", ids(got))
	}
	if got := collect(t, s, nostr.Filter{Authors: []string{"signer"}}); len(got) != 0 {
		t.Fatalf("author index not cleaned: %v", ids(got))
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := newStore(t, 0, time.Minute)
	const workers, perWorker = 16, 200

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			ctx := context.Background()
			for i := 0; i < perWorker; i++ {
				id := fmt.Sprintf("w%d-%d", w, i)
				ev := newEvent(id, "signer", "alice", nostr.Timestamp(i), "x")
				s.SaveEvent(ctx, ev)
				collect(t, s, nostr.Filter{Kinds: []int{24133}, Tags: nostr.TagMap{"p": {"alice"}}})
				if i%3 == 0 {
					s.DeleteEvent(ctx, ev)
				}
			}
		}(w)
	}
	wg.Wait()

	// Internal accounting must stay consistent: bytes never negative, and the
	// event count matches what the byte total implies is non-empty.
	st := s.Stats()
	if st.Bytes < 0 {
		t.Fatalf("totalBytes went negative: %d", st.Bytes)
	}
	if (st.Events == 0) != (st.Bytes == 0) {
		t.Fatalf("events/bytes inconsistent: events=%d bytes=%d", st.Events, st.Bytes)
	}
}

func TestDedup(t *testing.T) {
	s := newStore(t, 0, time.Minute)
	ev := newEvent("id1", "signer", "alice", 100, "x")
	s.SaveEvent(context.Background(), ev)
	before := s.Stats()
	s.SaveEvent(context.Background(), ev)
	after := s.Stats()

	if after.Events != 1 {
		t.Fatalf("dedup: events=%d, want 1", after.Events)
	}
	if after.Bytes != before.Bytes {
		t.Fatalf("dedup changed bytes: %d -> %d", before.Bytes, after.Bytes)
	}
}
