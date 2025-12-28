# nip46-relay

A lightweight [NIP-46](https://github.com/nostr-protocol/nips/blob/master/46.md) relay for Nostr remote signing. Use it with self-hosted signers like [nsec.app](https://nsec.app), [Signet](https://github.com/letdown2491/signet), or any NIP-46 compatible application.

Based on [Bunklay](https://github.com/dezh-tech/ddsr) from dezh-tech.

## Features

- **NIP-46 only** - Accepts only kind 24133 and 24135 events
- **Ephemeral storage** - Events auto-expire (default: 10 minutes)
- **Timestamp validation** - Rejects events outside time window
- **Lightweight** - Single binary, minimal dependencies
- **BadgerDB** - Embedded database, no external services needed

## Quick Start

### Build and Run

```bash
go build -o nip46-relay .
./nip46-relay
```

Relay runs on `http://localhost:3334` by default.

### With Docker

```bash
docker compose up -d
```

## Configuration

Copy `.env.example` to `.env` and customize:

```bash
cp .env.example .env
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `RELAY_NAME` | `NIP-46 Relay` | Display name in NIP-11 info |
| `RELAY_DESCRIPTION` | `A NIP-46 remote signing relay` | Relay description |
| `RELAY_URL` | | Public WebSocket URL (e.g., `wss://nip46.example.com`) |
| `RELAY_PUBKEY` | | Operator pubkey (hex format) |
| `RELAY_CONTACT` | | Contact info |
| `RELAY_ICON` | | URL to relay icon |
| `RELAY_BANNER` | | URL to relay banner |
| `RELAY_PORT` | `:3334` | Port to listen on |
| `WORKING_DIR` | `./nip46-relay-data` | Data directory for BadgerDB |
| `KEEP_IN_MINUTES` | `10` | Event retention time |
| `ACCEPT_WINDOW_IN_MINUTES` | `1` | Timestamp validation window |
| `RATE_LIMIT_PER_MINUTE` | `100` | Max events per minute per pubkey |

## Systemd Service

Install as a system service for auto-start and restart on failure:

```bash
# From ~/nip46-relay
cp .env.example .env
nano .env  # configure

# Edit service file to match your username/path
nano nip46-relay.service

# Install service
sudo cp nip46-relay.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable nip46-relay
sudo systemctl start nip46-relay

# Check status
sudo systemctl status nip46-relay
sudo journalctl -u nip46-relay -f
```

## Reverse Proxy with Caddy

Create or edit your Caddyfile:

```caddyfile
nip46.example.com {
    reverse_proxy localhost:3334
}
```

Reload Caddy:

```bash
sudo systemctl reload caddy
```

Caddy automatically provisions TLS certificates and handles WebSocket upgrades.

## How It Works

NIP-46 enables remote signing where a client app requests signatures from a remote signer (like a hardware device or mobile app). This relay acts as the communication channel:

1. Client publishes encrypted request (kind 24133) tagged to signer's pubkey
2. Relay stores and forwards to signer
3. Signer publishes encrypted response tagged to client's pubkey
4. Relay stores and forwards to client

All messages are NIP-44 encrypted end-to-end. The relay only sees opaque blobs.
