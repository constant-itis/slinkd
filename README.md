# slinkd

A shared, real-time event stream for ops, alerts, and system signals.

Systems and people publish structured events. Anyone can watch them live via CLI, WebSocket, or API. Add a Telegram bridge and it becomes your personal alerting system.

## Quickstart

### Prerequisites

- Go 1.22+
- PostgreSQL

### Build

```bash
go build -o bin/slinkd-server ./server/
go build -o bin/signal ./cli/
go build -o bin/signal-telegram ./bridges/telegram/
```

### Run

```bash
# Create the database
createdb slinkd

# Start the server
export SIGNAL_API_KEY=your-secret-key
export DATABASE_URL=postgres://localhost:5432/slinkd?sslmode=disable
./bin/slinkd-server
```

Tables are created automatically on startup.

### Docker

```bash
docker build -t slinkd .

docker run -p 8080:8080 \
  -e SIGNAL_API_KEY=your-secret-key \
  -e DATABASE_URL=postgres://host.docker.internal:5432/slinkd?sslmode=disable \
  slinkd
```

### Verify it works

Once the server is running, paste this into a terminal to test the full loop:

```bash
export SIGNAL_API_KEY=your-secret-key
export SIGNAL_HOST=http://localhost:8080

# Create a channel, send an event, read it back
curl -s -X POST $SIGNAL_HOST/channels \
  -H "Authorization: Bearer $SIGNAL_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"id":"test","name":"Test Channel"}' && echo

curl -s -X POST $SIGNAL_HOST/channels/test/events \
  -H "Authorization: Bearer $SIGNAL_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"type":"message","text":"hello from slinkd","author":"me"}' && echo

curl -s $SIGNAL_HOST/channels/test/events \
  -H "Authorization: Bearer $SIGNAL_API_KEY"
```

You should see your event come back in the response. If you built the CLI:

```bash
signal channel list
signal events test
signal tail test  # in one terminal
signal send test --type=message --text="it works" --author=me  # in another
```

Or test from any language — it's just HTTP:

```python
import requests

url = "http://localhost:8080"
headers = {"Authorization": "Bearer your-secret-key"}

# send an event
requests.post(f"{url}/channels/test/events", headers=headers, json={
    "type": "alert",
    "text": "disk usage at 92%",
    "author": "monitor"
})

# read events
resp = requests.get(f"{url}/channels/test/events", headers=headers)
print(resp.json())
```

```bash
# or just curl from a cron job, CI pipeline, bash script, anything
curl -X POST http://localhost:8080/channels/deploys/events \
  -H "Authorization: Bearer your-secret-key" \
  -H "Content-Type: application/json" \
  -d '{"type":"deployment","text":"v2.1 deployed","author":"github-actions"}'
```

## Configuration

All configuration is through environment variables.

### Server

| Variable | Required | Default | Description |
|---|---|---|---|
| `SIGNAL_API_KEY` | Yes | — | Shared secret for API authentication |
| `DATABASE_URL` | No | `postgres://localhost:5432/slinkd?sslmode=disable` | Postgres connection string |
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

# View recent events
signal events prod-alerts
signal events prod-alerts --limit=50

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

Copy `clients/python/slinkd.py` into your project. Requires `requests`.

```python
from slinkd import Slinkd

ss = Slinkd(author="my-bot")

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

Give someone access to your slinkd instance:

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
slinkd/
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
      slinkd.py Python client with rate-limited alerts
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
