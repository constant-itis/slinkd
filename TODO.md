# slinkd TODO

## Telegram bot commands (not yet implemented)

The bridge currently only forwards `type=alert` events from one channel to Telegram. It has no command handling — it can't receive messages from the user.

### Proposed commands

| Command | Description |
|---------|-------------|
| `/channels` | List all channels |
| `/watch <channel>` | Switch which channel the bridge listens to (reconnect WebSocket) |
| `/tail <channel> [N]` | Show last N events from a channel (default 10) |
| `/send <channel> <text>` | Post an alert event from Telegram |
| `/mute [duration]` | Suppress alerts for N minutes (default 30) |
| `/unmute` | Resume alerts |
| `/status` | Show current channel, uptime, mute state |

### Implementation notes

- Add a second goroutine that polls Telegram `getUpdates` for incoming messages
- Parse `/command` messages, call slinkd HTTP API or mutate internal state
- `/watch` needs to tear down and reconnect the WebSocket to a new channel
- `/mute` just sets a flag + timer that the alert forwarder checks before sending
- Restrict commands to `TELEGRAM_CHAT_ID` so only the owner can issue them
- Current code is in `bridges/telegram/main.go` (~110 lines) — all single-file, no refactor needed

### Multi-channel watching

Right now the bridge watches one channel via `SLINKD_CHANNEL` env var. Options:
1. **Multiple bridge instances** — run one per channel (simplest, current approach for gambino)
2. **Comma-separated env var** — `SLINKD_CHANNEL=gambino,alerts,system` — open multiple WebSocket connections
3. **`/watch` command** — let the user add/remove channels dynamically at runtime

Option 2 is probably the most useful near-term improvement. Would require spawning a goroutine per channel, each with its own WebSocket connection, all forwarding to the same Telegram chat.
