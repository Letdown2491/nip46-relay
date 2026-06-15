# Changelog

All notable changes to this project are documented in this file.

## [1.0.0] - 2026-06-14

First stable release. Graduates the relay from beta and makes the in-memory
store the default backend.

### Added

- In-memory event store (now the default backend), purpose-built for NIP-46:
  O(1) inserts indexed by recipient (`#p` tag) and author, with self-eviction on
  both a time window and a byte budget.
- `STORAGE_BACKEND` config (`memory` default, or `badger` for persistence across
  restarts).
- `MAX_MEMORY_MB` config to cap the in-memory store's byte budget; `0` auto-detects.
- Automatic memory detection (cgroup v2/v1, then `/proc/meminfo`) that tunes
  `GOMEMLIMIT` so the relay stays within its ceiling even on small (1 GB) hosts.
- Periodic in-memory store stats log (events buffered, MB, cumulative evictions)
  for production headroom visibility.
- `ReadHeaderTimeout` on the HTTP server to blunt slowloris-style attacks without
  affecting live WebSocket connections.
- Test suite for the in-memory store covering recipient/author routing, query
  ordering and limits, byte-budget and TTL eviction, delete/index cleanup,
  dedup, and concurrent access (passes under `-race`).
- `.env` is now loaded at startup if present (via `godotenv`), so bare/local
  runs honor it like the systemd (`EnvironmentFile`) and docker paths do. Real
  environment variables still take precedence.

### Changed

- Subscription filters for kinds the relay doesn't store now return an empty
  EOSE instead of a `CLOSED` rejection, so relay monitors (NIP-66) can measure
  read latency and the relay reports as responsive. NIP-46 traffic still must be
  scoped by `authors`, `#p`, or `ids` — unscoped queries that could match stored
  signing messages are still rejected, preventing firehose harvesting. Writes
  remain NIP-46-only, so usage is unchanged.
- The in-memory store now resolves `ids`-scoped queries (direct lookup by event
  id), making id-only filters return correctly.
- The prune + value-log GC cycle now runs only for the Badger backend; the
  in-memory store evicts itself.
- Rate limiter is now a per-key token bucket (O(1) time and memory) instead of a
  per-request scan over a slice of timestamps.
- `docker-compose.yml` now reads configuration from `.env` (`env_file`,
  optional) instead of a hardcoded `environment:` block, so `.env` is a single
  source of truth across local, docker, and systemd deployments. The container's
  `WORKING_DIR` is still pinned to the mounted volume.
- `.env.example` documents every config variable, grouped into required (active)
  and optional (commented-out) sections, so `cp .env.example .env` yields a
  working config that boots as-is.
- Docker builds use `-trimpath` and `-ldflags="-s -w"` for a smaller, more
  reproducible binary.

### Removed

- Unused `ADMIN_PUBKEYS` config field (it was read into config but never
  referenced anywhere).

### Fixed

- Memory auto-tuning now respects an operator-configured `GOMEMLIMIT` instead of
  overriding it; the in-memory event budget is derived from that limit when set.
  Auto-detection only applies when no `GOMEMLIMIT` is configured.
- Badger value-log garbage collection is now run periodically. Previously deleted
  events only wrote tombstones, so `.vlog` files grew without bound under the
  relay's write-then-delete workload.
- `pruneOldEvents` no longer risks leaking the eventstore query goroutine on a
  cleanup timeout: the result channel is always drained to completion.

### Performance

- Removed disk I/O from the signing hot path. Ephemeral messages were previously
  committed to Badger before khatru broadcast them to live subscribers; the
  in-memory backend makes this a cheap map insert and decouples live delivery
  from storage health.
- `pruneOldEvents` (Badger backend) streams deletes as events arrive instead of
  buffering the entire expired set in memory.

[Unreleased]: https://github.com/Letdown2491/nip46-relay/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/Letdown2491/nip46-relay/releases/tag/v1.0.0
