package main

import (
	"log"

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

	WorkingDirectory string   `envconfig:"WORKING_DIR" default:"./nip46-relay-data"`
	RelayPort        string   `envconfig:"RELAY_PORT" default:":3334"`
	Admins           []string `envconfig:"ADMIN_PUBKEYS"`

	KeepNotesFor        int `envconfig:"KEEP_IN_MINUTES" default:"10"`
	AcceptEventsInRange int `envconfig:"ACCEPT_WINDOW_IN_MINUTES" default:"1"`
	RateLimitPerMinute  int `envconfig:"RATE_LIMIT_PER_MINUTE" default:"100"`
}

func LoadConfig() {
	if err := envconfig.Process("", &config); err != nil {
		log.Fatalf("failed to read from env: %s", err)
		return
	}
}
