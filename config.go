package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	RelayName        string `envconfig:"RELAY_NAME" default:"NIP-46 Relay"`
	RelayPubkey      string `envconfig:"RELAY_PUBKEY"`
	RelayDescription string `envconfig:"RELAY_DESCRIPTION" default:"A NIP-46 remote signing relay"`
	RelayURL         string `envconfig:"RELAY_URL"`
	RelayContact     string `envconfig:"RELAY_CONTACT"`
	RelayIcon        string `envconfig:"RELAY_ICON"`
	RelayBanner      string `envconfig:"RELAY_BANNER"`

	WorkingDirectory string `envconfig:"WORKING_DIR" default:"./nip46-relay-data"`
	RelayPort        string `envconfig:"RELAY_PORT" default:":3334"`

	KeepNotesFor        int `envconfig:"KEEP_IN_MINUTES" default:"10"`
	AcceptEventsInRange int `envconfig:"ACCEPT_WINDOW_IN_MINUTES" default:"1"`
	RateLimitPerMinute  int `envconfig:"RATE_LIMIT_PER_MINUTE" default:"100"`

	// StorageBackend selects how buffered events are held: "memory" (default,
	// ephemeral, no disk) or "badger" (persistent across restarts).
	StorageBackend string `envconfig:"STORAGE_BACKEND" default:"memory"`
	// MaxMemoryMB caps the in-memory store's event byte-budget. 0 = auto-detect
	// from the host/cgroup memory limit.
	MaxMemoryMB int `envconfig:"MAX_MEMORY_MB" default:"0"`
}

func LoadConfig() {
	// Load a local .env file if present so bare/local runs honor it too, matching
	// the systemd (EnvironmentFile) and docker (env_file) deployment paths.
	// godotenv.Load does not override variables already in the environment, so
	// real env vars (and systemd/docker-injected ones) take precedence.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Printf("warning: could not load .env: %s", err)
	}

	if err := envconfig.Process("", &config); err != nil {
		log.Fatalf("failed to read from env: %s", err)
	}
}
