package main

import (
	"context"
	"log"
	"time"

	"github.com/fiatjaf/eventstore/badger"
	"github.com/nbd-wtf/go-nostr"
)

func cleanDatabase() {
	// The in-memory store self-evicts; only the persistent badger backend needs
	// an external prune + value-log GC cycle.
	b, ok := store.(*badger.BadgerBackend)
	if !ok {
		return
	}

	// Run cleanup more frequently but with targeted queries
	// This reduces the batch size and spreads the load
	ticker := time.NewTicker(time.Duration(config.KeepNotesFor/2+1) * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		pruneOldEvents()
		// Reclaim disk space from Badger's value log. Deleting events only
		// writes tombstones; without GC the .vlog files grow unbounded under
		// this relay's write-then-delete (ephemeral event) workload.
		runValueLogGC(b)
	}
}

// runValueLogGC repeatedly rewrites Badger value-log files until there is
// nothing left worth reclaiming (RunValueLogGC returns a non-nil error, e.g.
// badger.ErrNoRewrite). Each successful call rewrites at most one file.
func runValueLogGC(b *badger.BadgerBackend) {
	if b == nil || b.DB == nil {
		return
	}
	for {
		if err := b.RunValueLogGC(0.5); err != nil {
			// ErrNoRewrite (nothing to collect) is the normal stop condition.
			return
		}
	}
}

func pruneOldEvents() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Calculate cutoff timestamp - only query events older than retention period
	cutoff := nostr.Timestamp(time.Now().Add(-time.Duration(config.KeepNotesFor) * time.Minute).Unix())

	// Use Until filter to only fetch expired events instead of full table scan
	filter := nostr.Filter{
		Kinds: []int{24133, 24135},
		Until: &cutoff,
	}

	// Delete events as they stream in. Badger uses MVCC, so deleting while the
	// query's read iterator is still open is safe, and avoids buffering the
	// entire expired set in memory.
	//
	// The eventstore producer goroutine sends without selecting on ctx, so we
	// must always drain the channel to completion (even after a timeout) to
	// avoid leaking it; we simply stop issuing deletes once ctx is done.
	timedOut := false
	for _, qe := range relay.QueryEvents {
		ch, err := qe(ctx, filter)
		if err != nil {
			log.Printf("can't read from database: %s", err.Error())
			continue
		}

		for ev := range ch {
			if timedOut {
				continue // drain remaining events, but stop deleting
			}
			if ctx.Err() != nil {
				log.Printf("cleanup timeout while pruning expired events")
				timedOut = true
				continue
			}
			deleteEvent(ctx, ev)
		}
	}
}

func deleteEvent(ctx context.Context, ev *nostr.Event) bool {
	for _, de := range relay.DeleteEvent {
		if err := de(ctx, ev); err != nil {
			log.Printf("can't delete event %s: %s", ev.ID, err.Error())
			return false
		}
	}
	return true
}
