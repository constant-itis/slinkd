# Integrating slinkd into an existing project

A walkthrough of wiring slinkd alerts into a running backend, using the gambino-backend integration as a concrete example. The same pattern works for any Node.js, Python, or HTTP-capable service.

## What you're doing

You have a backend that does things that can fail silently (token transfers, payment processing, email sends, cron jobs). You want those failures to surface in Telegram (or any slinkd consumer) instead of rotting in log files.

## Steps

### 1. Create a channel

Each project gets its own channel. Create it once:

```bash
curl -X POST http://<SLINKD_HOST>/channels \
  -H "Authorization: Bearer $SLINKD_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"id":"my-project","name":"My Project"}'
```

### 2. Drop in the client

Copy the client for your language into your project. No package install needed.

**Node.js** — copy `clients/nodejs/slinkd.js`:

```js
const { Slinkd } = require('./lib/slinkd');
const slinkd = new Slinkd({
  defaultChannel: 'my-project',
  author: 'my-backend',
});
```

**Python** — copy `clients/python/slinkd.py`:

```python
from lib.slinkd import Slinkd
slinkd = Slinkd(default_channel="my-project", author="my-backend")
```

**Anything else** — it's one HTTP POST:

```bash
curl -X POST $SLINKD_HOST/channels/my-project/events \
  -H "Authorization: Bearer $SLINKD_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"type":"alert","text":"something broke","author":"my-service"}'
```

### 3. Set env vars

Add to your `.env` or environment:

```
SLINKD_HOST=http://<your-slinkd-host>:8080
SLINKD_API_KEY=<your-key>
```

The client reads these automatically. If the vars are missing, alerts silently do nothing.

### 4. Add alerts at failure points

Find every `catch` block or error path that currently just logs and moves on. Add an alert call. The key design rules:

- **Fire-and-forget** — never `await` in a way that blocks the main flow. The clients already handle this (catch internally, log errors, never throw).
- **Use cooldowns** — repeated identical failures shouldn't spam. `alert()` suppresses duplicate text within its cooldown window (default 60s).
- **Include context in `data`** — the text is for humans reading Telegram. The `data` field is for structured context you might query later.

### 5. Choose severity

| Method     | Type     | Default cooldown | Use for                                         |
|------------|----------|------------------|-------------------------------------------------|
| `alert()`  | `alert`  | 60s              | Failures that need attention now                 |
| `signal()` | `signal` | 300s             | Expected edge cases, deferred work, soft warnings |
| `send()`   | any      | none             | Informational events (deployments, state changes) |

The Telegram bridge only forwards `type=alert` events by default, so signals won't buzz your phone unless you configure the bridge to watch for them too.

---

## Real example: gambino-backend

### Problem

Solana token transfers (signup/KYC bonuses) were silently failing for days because the Alchemy RPC endpoint was down. The errors were logged to stdout, captured by pm2, but nobody was watching pm2 logs. No alerts, no notifications.

### What we did

**Created the channel:**

```bash
curl -X POST http://<your-slinkd-host>:8080/channels \
  -H "Authorization: Bearer $SLINKD_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"id":"gambino","name":"Gambino Backend"}'
```

**Added a minimal client** at `src/lib/slinkd.js` — a thin wrapper around `fetch` with cooldown-based rate limiting. Zero dependencies. ~50 lines. See `clients/nodejs/slinkd.js` for the reusable version.

For gambino, we used a module-level singleton instead of a class instance since there's only one channel:

```js
// src/lib/slinkd.js (gambino's version — simplified singleton)
const SLINKD_HOST = process.env.SLINKD_HOST || 'http://<your-slinkd-host>:8080';
const SLINKD_API_KEY = process.env.SLINKD_API_KEY || '';
const CHANNEL = 'gambino';

const _cooldowns = new Map();

async function send(channel, { type = 'signal', text, author = 'gambino-backend', data }) {
  try {
    const body = { type, text, author };
    if (data) body.data = data;
    await fetch(`${SLINKD_HOST}/channels/${channel}/events`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${SLINKD_API_KEY}`,
      },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(5000),
    });
  } catch (err) {
    console.error(`[slinkd] Failed to send to #${channel}:`, err.message);
  }
}

async function alert(text, { author = 'gambino-backend', data, cooldown = 60 } = {}) {
  const now = Date.now();
  const lastSent = _cooldowns.get(text) || 0;
  if (now - lastSent < cooldown * 1000) return;
  _cooldowns.set(text, now);
  await send(CHANNEL, { type: 'alert', text, author, data });
}

module.exports = { send, alert };
```

**Wired it into `distributionService.js`** at two failure points:

```js
const slinkd = require('../lib/slinkd');

// In distributeTokens() — insufficient balance check:
if (sourceTokenAccount.amount < transferAmount) {
  slinkd.alert(
    `Treasury ${sourceAccount} insufficient balance: ${available} < ${requested} GAMBINO`,
    { data: { sourceAccount, available, requested, recipient } }
  );
  throw new Error(`Insufficient balance...`);
}

// In the catch block — any transfer failure:
catch (error) {
  if (distribution) {
    distribution.status = 'failed';
    distribution.error = error.message;
    await distribution.save();
  }

  slinkd.alert(
    `Token transfer failed: ${amount} GG to ${recipient}: ${error.message}`,
    { data: { amount, recipient, sourceAccount, error: error.message } }
  );

  throw error;
}
```

**Added env vars** to `.env`:

```
SLINKD_HOST=http://<your-slinkd-host>:8080
SLINKD_API_KEY=<your-key>
```

**Restarted:** `pm2 restart gambino-backend`

### Result

Transfer failures now appear in Telegram within seconds. The cooldown prevents flooding when the RPC is fully down (you get one alert per unique error message per minute, not hundreds).

---

## Real example: n8n workflow alerts

### Problem

n8n workflows (Substack article generators) run on a webhook trigger — someone hits a URL, an article gets generated via OpenAI, saved to PocketBase, and uploaded to Google Drive. There's no visibility into when these run, what they cost, or if they fail. You only find out something broke when you check Google Drive and nothing's there.

### What we did

n8n doesn't have a native slinkd client, but it doesn't need one. slinkd is just HTTP — n8n's built-in HTTP Request node handles it directly.

**Created the channel:**

```bash
curl -X POST http://<SLINKD_HOST>/channels \
  -H "Authorization: Bearer $SLINKD_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"id":"content-gen","name":"Content Generation"}'
```

**Added an HTTP Request node** to each workflow. The node sits at the same fork point as the PocketBase save and Google Drive upload — all three fire in parallel after the article is generated and parsed.

Node configuration:

| Setting | Value |
|---------|-------|
| Method | POST |
| URL | `http://<SLINKD_HOST>/channels/content-gen/events` |
| Authentication | Header Auth |
| Header | `Authorization: Bearer <SLINKD_API_KEY>` |
| Body (JSON) | See below |
| Continue On Fail | **true** (critical — don't break article gen if slinkd is down) |
| Timeout | 5000ms |

**Body parameters:**

```json
{
  "type": "alert",
  "author": "VDV Substack",
  "text": "VDV Substack article generated: \"{{ $json.payload?.topic || $json.topic || 'unknown topic' }}\" | model: gpt-4o | cost: ~$0.01"
}
```

The `text` field uses n8n expressions (`={{ }}`) to pull the article topic from the upstream node's output. Adjust the expression path based on your workflow's data shape.

**Where to place it in the workflow:**

```
... → Parse/QC → Prepare Payload ──┬── Save to PocketBase
                                   ├── Convert → Upload to Google Drive → Response
                                   └── Alert: slinkd  ← (new)
```

The alert node connects from the same fork node that feeds your save and upload paths. In n8n's connection model, this means adding it as another output from `Prepare Payload`'s `main[0]` connections array.

**Set `continueOnFail: true`** on the alert node. This is non-negotiable. If slinkd is down, your article generation must still complete. The alert is a nice-to-have, not a gate.

### Adding it by hand in the n8n editor

If you prefer to add the node manually instead of editing JSON:

1. Open your workflow in n8n
2. Add a new **HTTP Request** node
3. Set Method: POST, URL: `http://<SLINKD_HOST>/channels/<your-channel>/events`
4. Under Authentication, choose "Predefined Credential Type" → "Header Auth" → create a credential with Name: `Authorization`, Value: `Bearer <your-key>`
5. Send Body → JSON → add your `type`, `author`, and `text` fields
6. Go to **Settings** tab → toggle on **Continue On Fail**
7. Draw a connection from your fork/split node to this new node
8. Test the workflow — you should get a Telegram message

### Adding it via JSON (programmatic)

If you're scripting workflow modifications (e.g., adding alerts to multiple workflows):

```python
alert_node = {
    "parameters": {
        "method": "POST",
        "url": f"{SLINKD_HOST}/channels/{channel}/events",
        "sendHeaders": True,
        "headerParameters": {
            "parameters": [{
                "name": "Authorization",
                "value": f"Bearer {SLINKD_API_KEY}"
            }]
        },
        "sendBody": True,
        "bodyParameters": {
            "parameters": [
                {"name": "type", "value": "alert"},
                {"name": "author", "value": "my-workflow"},
                {"name": "text", "value": "={{ 'Article generated: ' + $json.topic }}"}
            ]
        },
        "options": {"timeout": 5000}
    },
    "type": "n8n-nodes-base.httpRequest",
    "typeVersion": 4.2,
    "name": "Alert: slinkd",
    "continueOnFail": True
}

# Add to workflow nodes
workflow['nodes'].append(alert_node)

# Connect from your fork node
workflow['connections']['Prepare Payload']['main'][0].append({
    "node": "Alert: slinkd", "type": "main", "index": 0
})

# Import via CLI
# n8n import:workflow --input=updated-workflow.json
```

### Telegram bridge setup

Each slinkd channel needs its own Telegram bridge instance if you want alerts forwarded. The bridge binary only watches one channel at a time.

```bash
# Create a systemd service for the new channel
cat > /etc/systemd/system/slinkd-telegram-content.service << 'EOF'
[Unit]
Description=slinkd Telegram bridge (content-gen)
After=slinkd.service

[Service]
Type=simple
ExecStart=/usr/local/bin/slinkd-telegram
Environment=SLINKD_API_KEY=<your-key>
Environment=SLINKD_HOST=http://localhost:8080
Environment=SLINKD_CHANNEL=content-gen
Environment=TELEGRAM_BOT_TOKEN=<your-bot-token>
Environment=TELEGRAM_CHAT_ID=<your-chat-id>
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now slinkd-telegram-content
```

### Result

Every article generation triggers a Telegram alert with the topic, model, and estimated cost. If slinkd or Telegram is down, article generation is unaffected. If OpenAI is down, you get the n8n error in n8n's execution log (slinkd only fires after successful generation since it's downstream of the AI node).

---

## Tips

- **Don't alert on success.** If everything is working, slinkd should be quiet. Alert on failures, signal on edge cases.
- **Put the error message in the text.** The person reading Telegram needs to know what happened without SSHing in.
- **Use `data` for machine context.** User IDs, amounts, account names — anything you'd want to filter or query later.
- **Keep cooldowns short for critical alerts (60s), long for noisy ones (300s+).** If a cron job fails every 5 minutes, you don't need 12 alerts per hour.
- **Test with a curl first.** Before wiring the client, send a manual event to make sure your channel and Telegram bridge are working.
