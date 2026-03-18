# SignalStack

A shared, real-time event stream for ops, alerts, and system signals.

Systems and people publish structured events. Anyone can watch them live via CLI, WebSocket, or API. Add a Telegram bridge and it becomes your personal alerting system.

## Quickstart

### Prerequisites

- Go 1.22+
- PostgreSQL

### Build

```bash
go build -o bin/signalstack-server ./server/
go build -o bin/signal ./cli/
go build -o bin/signal-telegram ./bridges/telegram/
```

### Run

```bash
# Create the database
createdb signalstack

# Start the server
export SIGNAL_API_KEY=your-secret-key
export DATABASE_URL=postgres://localhost:5432/signalstack?sslmode=disable
./bin/signalstack-server
```

Tables are created automatically on startup.

### Docker

```bash
docker build -t signalstack .

docker run -p 8080:8080 \
  -e SIGNAL_API_KEY=your-secret-key \
  -e DATABASE_URL=postgres://host.docker.internal:5432/signalstack?sslmode=disable \
  signalstack
```

## Configuration

All configuration is through environment variables.

### Server

| Variable | Required | Default | Description |
|---|---|---|---|
| `SIGNAL_API_KEY` | Yes | — | Shared secret for API authentication |
| `DATABASE_URL` | No | `postgres://localhost:5432/signalstack?sslmode=disable` | Postgres connection string |
| `SIGNAL_ADDR` | No | `:8080` | Listen address |

### CLI

| Variable | Required | Default | Description |
|---|---|---|---|
| `SIGNAL_API_KEY` | Yes | — | Must match the server's key |
| `SIGNAL_HOST` | No | `http://localhost:8080` | Server URL |

### Telegram Bridge

| Variable | Required | Default | Description |
|---|---|---|---|
| `SIGNAL_API_KEY` | Yes | — | Must match the server's key |
| `SIGNAL_HOST` | No | `http://localhost:8080` | Server URL |
| `SIGNAL_CHANNEL` | No | `alerts` | Channel to watch |
| `TELEGRAM_BOT_TOKEN` | Yes | — | Token from @BotFather |
| `TELEGRAM_CHAT_ID` | Yes | — | Your Telegram chat ID |

## CLI Usage

```bash
# Create a channel
signal channel create prod-alerts
signal channel create deploys --name="Deploy Log"

# List channels
signal channel list

# Send an event
signal send prod-alerts --type=alert --text="API error rate >5%" --author=monitor
signal send deploys --type=deployment --text="v2.1 shipped" --author=ci

# Stream events live
signal tail prod-alerts
```

### Event Types

`message`, `breaking_change`, `alert`, `deployment`, `signal`

## API

All endpoints require `Authorization: Bearer <SIGNAL_API_KEY>` header.

### Channels

```
POST /channels              Create a channel
     Body: {"id": "alerts", "name": "Alerts"}

GET  /channels               List all channels
```

### Events

```
POST /channels/:id/events   Publish an event
     Body: {"type": "alert", "text": "something broke", "author": "bot", "data": {}}

GET  /channels/:id/events   Get events (paginated)
     Query: ?cursor=<timestamp>&limit=50
```

### WebSocket

```
GET  /ws?channel=:id         Stream events in real time
     Header: Authorization: Bearer <key>
```

## Python Client

Copy `clients/python/signalstack.py` into your project. Requires `requests`.

```python
from signalstack import SignalStack

ss = SignalStack(author="my-bot")

# Send an alert (rate-limited: same text suppressed for 60s)
ss.alert("Orderbook empty: depth=0")

# Send with custom cooldown
ss.alert("Spread too high", cooldown=300)

# Send any event type
ss.send("deploys", type="deployment", text="v3 live", author="ci")
```

## Telegram Alerts

1. Message [@BotFather](https://t.me/BotFather) on Telegram, create a bot, copy the token.
2. Message your bot, then get your chat ID:
   ```bash
   curl https://api.telegram.org/bot<TOKEN>/getUpdates
   ```
   Your chat ID is in `result[0].message.chat.id`.
3. Run the bridge:
   ```bash
   export TELEGRAM_BOT_TOKEN=your-token
   export TELEGRAM_CHAT_ID=your-chat-id
   export SIGNAL_API_KEY=your-secret-key
   ./bin/signal-telegram
   ```

The bridge watches for `type=alert` events and forwards them to Telegram. It auto-reconnects if the server drops.

## Sharing With Others

Give someone access to your SignalStack:

1. Share the server URL and API key
2. They set the env vars:
   ```bash
   export SIGNAL_HOST=http://your-server:8080
   export SIGNAL_API_KEY=your-secret-key
   ```
3. They can now use the CLI to send, tail, and list channels

Currently uses a single shared API key. All users with the key have full access to all channels.

## Project Structure

```
signalstack/
  server/
    main.go          HTTP server, auth middleware, WebSocket hub, migrations
    channels.go      Channel create/list handlers
    events.go        Event publish/query, cursor pagination, WebSocket streaming
  cli/
    main.go          CLI: tail, send, channel create/list
  bridges/
    telegram/
      main.go        WebSocket→Telegram alert forwarder
  clients/
    python/
      signalstack.py Python client with rate-limited alerts
  Dockerfile         Multi-stage build, ~15MB image
```

## Database Schema

Created automatically on server startup:

```sql
CREATE TABLE channels (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at BIGINT
);

CREATE TABLE events (
    id UUID PRIMARY KEY,
    channel TEXT NOT NULL REFERENCES channels(id),
    type TEXT NOT NULL,
    timestamp BIGINT NOT NULL,
    author TEXT NOT NULL DEFAULT '',
    data JSONB,
    text TEXT NOT NULL DEFAULT ''
);
```
