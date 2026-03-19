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

## Tips

- **Don't alert on success.** If everything is working, slinkd should be quiet. Alert on failures, signal on edge cases.
- **Put the error message in the text.** The person reading Telegram needs to know what happened without SSHing in.
- **Use `data` for machine context.** User IDs, amounts, account names — anything you'd want to filter or query later.
- **Keep cooldowns short for critical alerts (60s), long for noisy ones (300s+).** If a cron job fails every 5 minutes, you don't need 12 alerts per hour.
- **Test with a curl first.** Before wiring the client, send a manual event to make sure your channel and Telegram bridge are working.
