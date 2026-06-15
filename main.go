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
	"strings"
	"syscall"
	"time"

	badgerdb "github.com/dgraph-io/badger/v4"
	"github.com/fiatjaf/eventstore/badger"
	"github.com/fiatjaf/khatru"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

// EventStore is the minimal backend interface the relay needs. Both the
// in-memory store and the persistent badger backend satisfy it.
type EventStore interface {
	Init() error
	SaveEvent(context.Context, *nostr.Event) error
	QueryEvents(context.Context, nostr.Filter) (chan *nostr.Event, error)
	DeleteEvent(context.Context, *nostr.Event) error
	Close()
}

var (
	relay  *khatru.Relay
	config Config

	// Active event store backend (in-memory or badger), initialized in main.
	store EventStore

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

	// Tune the Go runtime for the available memory and size the event budget.
	eventBudget := configureMemory()

	switch strings.ToLower(config.StorageBackend) {
	case "badger":
		dbPath := path.Join(config.WorkingDirectory, "database")
		log.Printf("Storage: badger (persistent), data directory: %s", dbPath)
		store = &badger.BadgerBackend{
			Path:     dbPath,
			MaxLimit: 100,
			BadgerOptionsModifier: func(opts badgerdb.Options) badgerdb.Options {
				// Disable fsync on every write - significantly reduces write latency
				// Safe for ephemeral data that expires in minutes
				opts.SyncWrites = false
				return opts
			},
		}
	default:
		log.Printf("Storage: in-memory (ephemeral), budget %d MB, retention %d min",
			eventBudget>>20, config.KeepNotesFor)
		ms := NewMemStore(eventBudget, time.Duration(config.KeepNotesFor)*time.Minute)
		go reportMemStats(ms)
		store = ms
	}

	if err := store.Init(); err != nil {
		log.Fatalf("failed to initialize storage: %s", err)
	}

	relay.RejectCountFilter = append(relay.RejectCountFilter, func(ctx context.Context, filter nostr.Filter) (reject bool, msg string) {
		return true, "blocked: we don't accept count filters"
	})

	relay.RejectFilter = append(relay.RejectFilter, func(ctx context.Context, filter nostr.Filter) (reject bool, msg string) {
		return rejectFilter(filter)
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
		if err := store.SaveEvent(ctx, event); err != nil {
			log.Printf("can't store event: %s\nerror: %s\n", event.String(), err.Error())
		}
	})

	relay.QueryEvents = append(relay.QueryEvents, store.QueryEvents)
	relay.DeleteEvent = append(relay.DeleteEvent, store.DeleteEvent)

	go cleanDatabase()

	mux := relay.Router()
	mux.HandleFunc("GET /{$}", staticViewHandler)

	log.Println("Relay running on port: " + config.RelayPort)

	server := &http.Server{
		Addr:    config.RelayPort,
		Handler: relay,
		// Bound the time to read request headers to blunt slowloris-style
		// attacks. Safe for WebSockets: it only applies to the HTTP handshake
		// before the connection is hijacked for the relay, not to the long-lived
		// socket afterwards. ReadTimeout/WriteTimeout are intentionally unset so
		// they don't sever live connections.
		ReadHeaderTimeout: 10 * time.Second,
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

	store.Close()
	log.Println("Shutdown complete")
}

func staticViewHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if err := parsedTemplate.Execute(w, relay.Info); err != nil {
		http.Error(w, "Error executing template", http.StatusInternalServerError)
		return
	}
}

// rejectFilter decides whether to reject a subscription filter.
//
// NIP-46 signing traffic may only be read scoped to a specific pubkey or event
// id - never as an unscoped firehose of everyone's messages. Any other filter
// (kinds we don't store) is allowed and simply returns an empty EOSE: standard
// relay behavior, and it lets relay monitors measure read latency instead of
// seeing a rejection.
func rejectFilter(filter nostr.Filter) (reject bool, msg string) {
	// An open-kinds filter (no Kinds set) matches what we store, so it counts as
	// potentially matching NIP-46 events.
	matchesNip46 := len(filter.Kinds) == 0
	for _, v := range filter.Kinds {
		if v == 24133 || v == 24135 {
			matchesNip46 = true
		}
	}

	scoped := len(filter.Authors) > 0 || len(filter.Tags["p"]) > 0 || len(filter.IDs) > 0
	if matchesNip46 && !scoped {
		return true, "blocked: NIP-46 queries must be scoped by authors, #p, or ids"
	}

	return false, ""
}
