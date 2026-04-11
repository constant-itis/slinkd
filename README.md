# slinkd

A real-time event stream and task orchestration system for multi-agent ops.

Any system that can make an HTTP POST can publish events, create tasks, and coordinate work through slinkd. It stores everything in PostgreSQL, streams it live via WebSocket, and makes it available over a simple REST API. Wire it to Telegram, a dashboard, an MCP server, or anything else.

## What It Does

**Events** — A shared nervous system. Services, agents, and cron jobs publish events. Anything can watch the stream live or query history. Your phone buzzes when something breaks.

**Tasks** — A kanban system with atomic state management. Create tasks, assign them to agents, track them through `backlog → todo → claimed → in_progress → done`. Atomic claiming prevents double-work. State transitions emit events so existing consumers see everything.

**Projects** — Group tasks and agents by project. Each project links to a channel where its task events appear.

**Agents** — Register agents (Claude instances, bots, services) with heartbeat tracking. Assign them to projects and tasks.

**Watchdog** — Dead man's switch for anything that should be posting regularly. If a source goes silent, slinkd fires an alert. Peer health checks ping other slinkd instances.

## Quickstart

### Prerequisites

- Go 1.22+
- PostgreSQL

### Build

```bash
go build -o bin/slinkd-server ./server/
go build -o bin/slinkd ./cli/
go build -o bin/slinkd-telegram ./bridges/telegram/
```

### Run

```bash
createdb slinkd

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

## API

All endpoints require `Authorization: Bearer <key>` header.

| Key | Can GET | Can POST/PATCH |
|---|---|---|
| `SLINKD_API_KEY` | Yes | Yes |
| `SLINKD_READ_KEY` | Yes | No |

### Channels & Events

```
POST /channels                        Create a channel
GET  /channels                        List all channels
POST /channels/:id/events             Publish an event
GET  /channels/:id/events?limit=50    Get events (cursor pagination)
GET  /ws?channel=:id                  Stream events via WebSocket
GET  /healthz                         Health check (no auth)
```

Event types: `message`, `alert`, `deployment`, `signal`, `breaking_change`, `task`, `task_result`, `task_update`

### Projects

```
POST /projects                        Create a project
GET  /projects                        List all projects
GET  /projects/:id                    Get a project
GET  /projects/:id/agents             List project members
POST /projects/:id/agents             Add agent to project
```

Creating a project auto-creates its channel if it doesn't exist.

```bash
curl -X POST /projects -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"id":"gambino","name":"Gambino Backend","channel":"gambino"}'
```

### Agents

```
POST /agents                          Register/upsert agent (idempotent)
GET  /agents                          List all agents
GET  /agents/:id                      Get agent details
PATCH /agents/:id                     Update status + heartbeat
```

Agent registration is idempotent — call it on startup, it acts as both registration and heartbeat.

```bash
curl -X POST /agents -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"id":"claude-remote","name":"Claude Remote","metadata":{"host":"lxc-114"}}'
```

### Tasks

```
POST /projects/:id/tasks              Create task in project
GET  /projects/:id/tasks              List project tasks (filtered)
GET  /tasks?project=X&status=Y        List tasks across projects
GET  /tasks/:id                       Get task + subtasks
PATCH /tasks/:id                      Update task fields
POST /tasks/:id/transition            State transition (claim, start, complete...)
```

#### Creating a task

```bash
curl -X POST /projects/gambino/tasks -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"title":"Fix payment webhook","priority":2,"created_by":"claude-remote","status":"todo"}'
```

#### Filtering tasks

```bash
# All open tasks in a project
curl "/projects/gambino/tasks?status=todo,claimed,in_progress"

# Tasks assigned to a specific agent
curl "/projects/gambino/tasks?assignee=claude-remote"

# Cross-project: all blocked tasks
curl "/tasks?status=blocked"
```

#### Task state machine

```
backlog → todo → claimed → in_progress → done
                    ↓            ↓
                   todo       blocked → in_progress
                (unclaim)

Any active state → cancelled → backlog (reopen)
```

Valid transitions:

| From | To |
|---|---|
| `backlog` | `todo`, `cancelled` |
| `todo` | `claimed`, `cancelled` |
| `claimed` | `in_progress`, `todo` (unclaim), `cancelled` |
| `in_progress` | `blocked`, `done`, `cancelled` |
| `blocked` | `in_progress`, `cancelled` |
| `cancelled` | `backlog` (reopen) |
| `done` | (terminal) |

#### Atomic claiming

```bash
# First agent to claim wins. Second gets 409 Conflict.
curl -X POST /tasks/$TASK_ID/transition -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"status":"claimed","agent_id":"claude-remote"}'
```

Uses `UPDATE ... WHERE status = 'todo'` — PostgreSQL row-level locking handles the race. No lockfiles, no distributed locks.

#### Completing a task

```bash
curl -X POST /tasks/$TASK_ID/transition -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"status":"done","agent_id":"claude-remote","result":"Deployed and verified"}'
```

#### Event bridge

Every task mutation (create, update, transition) emits a `task_update` event to the project's channel with structured data:

```json
{
  "type": "task_update",
  "text": "Task todo->claimed: Fix payment webhook [claude-remote]",
  "author": "claude-remote",
  "data": {
    "task_id": "uuid",
    "action": "todo->claimed",
    "title": "Fix payment webhook",
    "status": "claimed",
    "assignee": "claude-remote",
    "project_id": "gambino"
  }
}
```

This means Telegram bridges, WebSocket subscribers, and watchdogs see task activity with zero changes to their code.

## Watchdog

Configure dead man's switches and peer health checks via a JSON config file.

```bash
export SLINKD_CONFIG=/etc/slinkd.json
```

```json
{
  "instance_name": "slinkd-prod",
  "watchers": [
    {
      "channel": "pi-fleet",
      "author": "pi-001",
      "expect_every": "1h",
      "alert_channel": "alerts",
      "message": "pi-001 went silent",
      "recovery_message": "pi-001 is back online",
      "remind_every": "4h"
    }
  ],
  "peers": [
    {
      "name": "slinkd-backup",
      "url": "http://backup-host:8080",
      "heartbeat_every": "30s",
      "expect_every": "2m",
      "alert_channel": "alerts"
    }
  ]
}
```

Watchers monitor channels for activity — if a source stops posting within the expected interval, an alert fires. Recovery alerts fire when the source comes back. Peers ping each other's `/healthz` endpoints.

## Configuration

### Server

| Variable | Required | Default | Description |
|---|---|---|---|
| `SLINKD_API_KEY` | Yes | — | Read-write key |
| `SLINKD_READ_KEY` | No | — | Read-only key |
| `DATABASE_URL` | No | `postgres://localhost:5432/slinkd?sslmode=disable` | Postgres connection |
| `SLINKD_ADDR` | No | `:8080` | Listen address |
| `SLINKD_CONFIG` | No | `slinkd.json` | Watchdog config file |

### CLI

| Variable | Required | Default | Description |
|---|---|---|---|
| `SLINKD_API_KEY` | Yes | — | Must match the server |
| `SLINKD_HOST` | No | `http://localhost:8080` | Server URL |

### Telegram Bridge

| Variable | Required | Default | Description |
|---|---|---|---|
| `SLINKD_API_KEY` | Yes | — | Must match the server |
| `SLINKD_HOST` | No | `http://localhost:8080` | Server URL |
| `SLINKD_CHANNEL` | No | `alerts` | Channel to watch |
| `TELEGRAM_BOT_TOKEN` | Yes | — | Token from @BotFather |
| `TELEGRAM_CHAT_ID` | Yes | — | Your Telegram chat ID |

## Clients

### Node.js

Copy `clients/nodejs/slinkd.js` into your project. Zero dependencies (Node 18+).

```js
const { Slinkd } = require('./slinkd');
const ss = new Slinkd({ defaultChannel: 'my-project', author: 'my-bot' });

await ss.alert('Orderbook empty: depth=0');
await ss.send('deploys', { type: 'deployment', text: 'v3 live', author: 'ci' });
```

### Python

Copy `clients/python/slinkd.py` into your project. Requires `requests`.

```python
from slinkd import Slinkd
ss = Slinkd(author="my-bot")
ss.alert("Orderbook empty: depth=0")
ss.send("deploys", type="deployment", text="v3 live", author="ci")
```

## MCP Integration

slinkd has an MCP server ([mycelium/slinkd_mcp.py](https://github.com/officialbubies/mycelium)) that exposes the full API as MCP tools. Claude Code instances connect over SSE and can:

- `post_task` / `claim_task` / `start_task` / `complete_task` — full task lifecycle
- `list_tasks` / `get_task` / `update_task` — kanban queries
- `send` / `read` / `chat` / `log` / `alert` — event streaming
- Project-scoped routing via `SLINKD_PROJECT` env var

Multiple Claude instances share the same slinkd, coordinate through tasks, and communicate through channels.

## Project Structure

```
slinkd/
  server/
    main.go          HTTP server, auth, WebSocket hub, migrations
    channels.go      Channel CRUD
    events.go        Event publish/query, cursor pagination, WebSocket
    projects.go      Project CRUD, agent membership
    agents.go        Agent registration, heartbeat, status
    tasks.go         Task CRUD, state machine, atomic claiming, event bridge
    watchers.go      Dead man's switch, peer health checks
  cli/
    main.go          CLI: tail, send, channel CRUD
  bridges/
    telegram/
      main.go        WebSocket→Telegram alert forwarder
  clients/
    nodejs/slinkd.js
    python/slinkd.py
  Dockerfile
```

## Database Schema

Created automatically on startup:

```sql
-- Event streaming
channels (id TEXT PK, name TEXT, created_at BIGINT)
events   (id UUID PK, channel TEXT FK, type TEXT, timestamp BIGINT, author TEXT, data JSONB, text TEXT)

-- Task orchestration
projects       (id TEXT PK, name TEXT, channel TEXT FK, description TEXT, created_at BIGINT)
agents         (id TEXT PK, name TEXT, status TEXT, last_seen BIGINT, metadata JSONB, created_at BIGINT)
agent_projects (agent_id TEXT FK, project_id TEXT FK, role TEXT, joined_at BIGINT)
tasks          (id UUID PK, project_id TEXT FK, title TEXT, description TEXT, status TEXT,
                priority INT, assignee TEXT FK, parent_task_id UUID FK, created_by TEXT,
                result TEXT, claimed_at BIGINT, started_at BIGINT, completed_at BIGINT,
                metadata JSONB, created_at BIGINT, updated_at BIGINT)
```

## See Also

- [WHY.md](./WHY.md) — Why slinkd exists and why it seems dumb
- [INTEGRATING.md](./INTEGRATING.md) — Step-by-step integration guide
