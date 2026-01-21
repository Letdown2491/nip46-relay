package main

import (
	"context"
	"log"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

func cleanDatabase() {
	// Run cleanup more frequently but with targeted queries
	// This reduces the batch size and spreads the load
	ticker := time.NewTicker(time.Duration(config.KeepNotesFor/2+1) * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		pruneOldEvents()
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

	var toDelete []*nostr.Event

	for _, qe := range relay.QueryEvents {
		ch, err := qe(ctx, filter)
		if err != nil {
			log.Printf("can't read from database: %s", err.Error())
			continue
		}

		// Collect events to delete
		for ev := range ch {
			toDelete = append(toDelete, ev)
		}
	}

	if len(toDelete) == 0 {
		return
	}

	// Delete collected events
	deleted := 0
	for _, ev := range toDelete {
		// Check context in case we're taking too long
		if ctx.Err() != nil {
			log.Printf("cleanup timeout: deleted %d/%d events", deleted, len(toDelete))
			return
		}
		if deleteEvent(ctx, ev) {
			deleted++
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
