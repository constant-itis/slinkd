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
go build -o bin/slinkd./cli/
go build -o bin/slinkd-telegram ./bridges/telegram/
```

### Run

```bash
# Create the database
createdb slinkd

# Start the server
export SLINKD_API_KEY=your-secret-key
export DATABASE_URL=postgres://localhost:5432/slinkd?sslmode=disable
./bin/slinkd-server
```

Tables are created automatically on startup.

### Docker

```bash
docker build -t slinkd .

docker run -p 8080:8080 \
  -e SLINKD_API_KEY=your-secret-key \
  -e DATABASE_URL=postgres://host.docker.internal:5432/slinkd?sslmode=disable \
  slinkd
```

### Verify it works

Once the server is running, paste this into a terminal to test the full loop:

```bash
export SLINKD_API_KEY=your-secret-key
export SLINKD_HOST=http://localhost:8080

# Create a channel, send an event, read it back
curl -s -X POST $SLINKD_HOST/channels \
  -H "Authorization: Bearer $SLINKD_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"id":"test","name":"Test Channel"}' && echo

curl -s -X POST $SLINKD_HOST/channels/test/events \
  -H "Authorization: Bearer $SLINKD_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"type":"message","text":"hello from slinkd","author":"me"}' && echo

curl -s $SLINKD_HOST/channels/test/events \
  -H "Authorization: Bearer $SLINKD_API_KEY"
```

You should see your event come back in the response. If you built the CLI:

```bash
slinkd channel list
slinkd events test
slinkd tail test  # in one terminal
slinkd send test --type=message --text="it works" --author=me  # in another
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
| `SLINKD_API_KEY` | Yes | — | Read-write key for full API access |
| `SLINKD_READ_KEY` | No | — | Read-only key (can GET events/channels, cannot POST) |
| `DATABASE_URL` | No | `postgres://localhost:5432/slinkd?sslmode=disable` | Postgres connection string |
| `SLINKD_ADDR` | No | `:8080` | Listen address |

### CLI

| Variable | Required | Default | Description |
|---|---|---|---|
| `SLINKD_API_KEY` | Yes | — | Must match the server's key |
| `SLINKD_HOST` | No | `http://localhost:8080` | Server URL |

### Telegram Bridge

| Variable | Required | Default | Description |
|---|---|---|---|
| `SLINKD_API_KEY` | Yes | — | Must match the server's key |
| `SLINKD_HOST` | No | `http://localhost:8080` | Server URL |
| `SLINKD_CHANNEL` | No | `alerts` | Channel to watch |
| `TELEGRAM_BOT_TOKEN` | Yes | — | Token from @BotFather |
| `TELEGRAM_CHAT_ID` | Yes | — | Your Telegram chat ID |

## CLI Usage

```bash
# Create a channel
slinkd channel create prod-alerts
slinkd channel create deploys --name="Deploy Log"

# List channels
slinkd channel list

# Send an event
slinkd send prod-alerts --type=alert --text="API error rate >5%" --author=monitor
slinkd send deploys --type=deployment --text="v2.1 shipped" --author=ci

# View recent events
slinkd events prod-alerts
slinkd events prod-alerts --limit=50

# Stream events live
slinkd tail prod-alerts
```

### Event Types

`message`, `breaking_change`, `alert`, `deployment`, `signal`

## API

All endpoints require `Authorization: Bearer <key>` header.

Two key types are supported:

| Key | Can GET | Can POST |
|---|---|---|
| `SLINKD_API_KEY` | Yes | Yes |
| `SLINKD_READ_KEY` | Yes | No |

Use the read-only key to give someone visibility into your event stream without letting them write to it.

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

## Clients

### Node.js

Copy `clients/nodejs/slinkd.js` into your project. Zero dependencies — uses built-in `fetch` (Node 18+).

```js
const { Slinkd } = require('./slinkd');
const ss = new Slinkd({ defaultChannel: 'my-project', author: 'my-bot' });

// Send an alert (rate-limited: same text suppressed for 60s)
await ss.alert('Orderbook empty: depth=0');

// Send with custom cooldown
await ss.alert('Spread too high', { cooldown: 300 });

// Send any event type
await ss.send('deploys', { type: 'deployment', text: 'v3 live', author: 'ci' });
```

### Python

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

## Integrating into an existing project

See [INTEGRATING.md](./INTEGRATING.md) for a step-by-step walkthrough (with the gambino-backend integration as a worked example).

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
   export SLINKD_API_KEY=your-secret-key
   ./bin/slinkd-telegram
   ```

The bridge watches for `type=alert` events and forwards them to Telegram. It auto-reconnects if the server drops.

## Sharing With Others

Give someone access to your slinkd instance:

**Full access (read + write):**
```bash
export SLINKD_HOST=http://your-server:8080
export SLINKD_API_KEY=your-secret-key
```

**Read-only access (can view and tail, cannot post):**
```bash
export SLINKD_HOST=http://your-server:8080
export SLINKD_API_KEY=your-read-only-key
```

Generate a read key however you want (`openssl rand -hex 16 | sed 's/^/sk-read-/'`), set it as `SLINKD_READ_KEY` on the server, and share it. The read key can list channels, get events, and stream via WebSocket, but any POST request returns 401.

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
    nodejs/
      slinkd.js  Node.js client, zero dependencies (Node 18+)
    python/
      slinkd.py  Python client with rate-limited alerts
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
