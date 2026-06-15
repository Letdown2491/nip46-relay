package main

import (
	"runtime/debug"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestConfigureMemoryRespectsExplicitLimit(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })

	prevMax := config.MaxMemoryMB
	t.Cleanup(func() { config.MaxMemoryMB = prevMax })
	config.MaxMemoryMB = 0

	const want = 1500 << 20 // simulate operator GOMEMLIMIT=1500MiB
	debug.SetMemoryLimit(want)

	budget := configureMemory()

	if got := debug.SetMemoryLimit(-1); got != want {
		t.Fatalf("GOMEMLIMIT was overridden: got %d, want it left at %d", got, want)
	}
	if budget != want/2 {
		t.Fatalf("event budget = %d, want %d (half the configured limit)", budget, want/2)
	}
}

func TestRejectFilter(t *testing.T) {
	cases := []struct {
		name   string
		filter nostr.Filter
		reject bool
	}{
		// Legitimate signer queries: scoped to a pubkey -> allowed.
		{"signer by #p", nostr.Filter{Kinds: []int{24133}, Tags: nostr.TagMap{"p": {"x"}}}, false},
		{"signer by author", nostr.Filter{Kinds: []int{24135}, Authors: []string{"x"}}, false},

		// Firehose attempts: could match stored NIP-46 events, not scoped -> rejected.
		{"nip46 kind unscoped", nostr.Filter{Kinds: []int{24133}}, true},
		{"open kinds unscoped", nostr.Filter{}, true},
		{"limit only, no kinds", nostr.Filter{Limit: 1}, true},
		{"nip46 mixed kinds unscoped", nostr.Filter{Kinds: []int{24133, 1}}, true},

		// Monitor / non-NIP-46 probes: match nothing we store -> allowed (empty EOSE).
		{"monitor kind 1", nostr.Filter{Kinds: []int{1}, Limit: 1}, false},
		{"kind 1 scoped", nostr.Filter{Kinds: []int{1}, Tags: nostr.TagMap{"p": {"x"}}}, false},

		// Id-scoped queries are allowed even without authors/#p.
		{"id scoped", nostr.Filter{IDs: []string{"abc"}}, false},
		{"nip46 kind id scoped", nostr.Filter{Kinds: []int{24133}, IDs: []string{"abc"}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := rejectFilter(tc.filter)
			if got != tc.reject {
				t.Fatalf("rejectFilter(%+v) = %v (%q), want %v", tc.filter, got, msg, tc.reject)
			}
		})
	}
}
