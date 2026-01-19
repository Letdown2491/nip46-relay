package main

import (
	"context"
	"log"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

func cleanDatabase() {
	ticker := time.NewTicker(time.Duration(config.KeepNotesFor) * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		pruneOldEvents()
	}
}

func pruneOldEvents() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	filter := nostr.Filter{}

	for _, qe := range relay.QueryEvents {
		ch, err := qe(ctx, filter)
		if err != nil {
			log.Printf("can't read from database: %s", err.Error())
			continue
		}

		for ev := range ch {
			duration := time.Since(ev.CreatedAt.Time())
			minutesPassed := int(duration.Minutes())

			if minutesPassed >= config.KeepNotesFor {
				deleteEvent(ctx, ev)
			}
		}
	}
}

func deleteEvent(ctx context.Context, ev *nostr.Event) {
	for _, de := range relay.DeleteEvent {
		if err := de(ctx, ev); err != nil {
			log.Printf("can't delete event %s: %s", ev.ID, err.Error())
			return
		}
	}
}
