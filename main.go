package main

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"

	badgerdb "github.com/dgraph-io/badger/v4"
	"github.com/fiatjaf/eventstore/badger"
	"github.com/fiatjaf/khatru"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

var (
	relay  *khatru.Relay
	config Config

	//go:embed static/index.html
	landingTempl []byte

	// Parsed template (initialized once at startup)
	parsedTemplate *template.Template

	// Rate limiter
	rateLimiter *RateLimiter
)

func main() {
	log.SetPrefix("nip46-relay ")
	log.Printf("Running %s\n", StringVersion())

	relay = khatru.NewRelay()

	LoadConfig()

	// Parse template once at startup
	var err error
	parsedTemplate, err = template.New("webpage").Parse(string(landingTempl))
	if err != nil {
		log.Fatalf("failed to parse template: %s", err)
	}

	// Initialize rate limiter (per pubkey)
	rateLimiter = NewRateLimiter(config.RateLimitPerMinute, time.Minute)
	log.Printf("Rate limit: %d events/minute per pubkey", config.RateLimitPerMinute)

	relay.Info.Name = config.RelayName
	relay.Info.Software = "https://github.com/Letdown2491/nip46-relay"
	relay.Info.Version = StringVersion()
	relay.Info.PubKey = config.RelayPubkey
	relay.Info.Description = config.RelayDescription
	relay.Info.Icon = config.RelayIcon
	relay.Info.Contact = config.RelayContact
	relay.Info.URL = config.RelayURL
	relay.Info.Banner = config.RelayBanner
	relay.Info.SupportedNIPs = []any{1, 46}
	relay.Info.Limitation = &nip11.RelayLimitationDocument{
		MaxMessageLength: 131072, // 128 KB
		MaxSubscriptions: 20,
		MaxEventTags:     100,
		MaxContentLength: 65536, // 64 KB
		MaxLimit:         100,
		RestrictedWrites: true,
	}
	relay.Info.Retention = []*nip11.RelayRetentionDocument{
		{
			Kinds: [][]int{{24133, 24135}},
			Time:  int64(config.KeepNotesFor * 60),
		},
	}

	dbPath := path.Join(config.WorkingDirectory, "database")
	log.Printf("Data directory: %s\n", dbPath)

	mainDB := &badger.BadgerBackend{
		Path:     dbPath,
		MaxLimit: 100,
		BadgerOptionsModifier: func(opts badgerdb.Options) badgerdb.Options {
			// Disable fsync on every write - significantly reduces write latency
			// Safe for ephemeral data that expires in minutes
			opts.SyncWrites = false
			return opts
		},
	}
	mainDB.Init()

	relay.RejectCountFilter = append(relay.RejectCountFilter, func(ctx context.Context, filter nostr.Filter) (reject bool, msg string) {
		return true, "blocked: we don't accept count filters"
	})

	relay.RejectFilter = append(relay.RejectFilter, func(ctx context.Context, filter nostr.Filter) (reject bool, msg string) {
		if len(filter.Kinds) == 0 {
			return true, "blocked: please add kind 24133 or 24135"
		}

		if len(filter.Authors) == 0 && len(filter.Tags["p"]) == 0 {
			return true, "blocked: please add authors or #p"
		}

		for _, v := range filter.Kinds {
			if v != 24133 && v != 24135 {
				return true, "blocked: we only keep kind 24133 or 24135"
			}
		}

		return false, ""
	})

	relay.RejectEvent = append(relay.RejectEvent, func(ctx context.Context, event *nostr.Event) (reject bool, msg string) {
		if (event.Kind != 24133) && (event.Kind != 24135) {
			return true, "blocked: only kind 24133 and 24135 is accepted"
		}

		if !IsInTimeWindow(event.CreatedAt.Time().Unix(), config.AcceptEventsInRange) {
			return true, fmt.Sprintf("invalid: we only accept event on %d minute time frame", config.AcceptEventsInRange)
		}

		// Rate limiting by event author pubkey
		if !rateLimiter.Allow(event.PubKey) {
			return true, "rate-limited: too many events, slow down"
		}

		return false, ""
	})

	relay.OnEphemeralEvent = append(relay.OnEphemeralEvent, func(ctx context.Context, event *nostr.Event) {
		if err := mainDB.SaveEvent(ctx, event); err != nil {
			log.Printf("can't store event: %s\nerror: %s\n", event.String(), err.Error())
		}
	})

	relay.QueryEvents = append(relay.QueryEvents, mainDB.QueryEvents)
	relay.DeleteEvent = append(relay.DeleteEvent, mainDB.DeleteEvent)

	go cleanDatabase()

	mux := relay.Router()
	mux.HandleFunc("GET /{$}", staticViewHandler)

	log.Println("Relay running on port: " + config.RelayPort)

	server := &http.Server{
		Addr:    config.RelayPort,
		Handler: relay,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen error: %s", err)
		}
	}()

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)

	sig := <-sigChan
	log.Printf("Received signal %s: initiating graceful shutdown", sig.String())

	// Graceful shutdown with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %s", err)
	}

	mainDB.Close()
	log.Println("Shutdown complete")
}

func staticViewHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if err := parsedTemplate.Execute(w, relay.Info); err != nil {
		http.Error(w, "Error executing template", http.StatusInternalServerError)
		return
	}
}
