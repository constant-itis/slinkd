# Why slinkd Exists (and Why It Seems Dumb)

slinkd is dumb. That's the point.

## The Problem

Every system you run — backends, cron jobs, automations, agents — already logs errors. The problem is where. Errors get written to log files on some server, or printed to a terminal nobody has open, or swallowed silently into the void.

Your stuff breaks and you find out hours or days later when something downstream blows up, or a user complains, or you happen to check.

Real example: a Solana token transfer service was silently failing for days because the RPC endpoint went down. The errors were logged. Nobody was watching the logs.

## What slinkd Does

It's a central place that everything can yell into, and anything can listen.

That's it.

Any system that can make an HTTP POST can report to slinkd. slinkd stores the event and streams it to anything that's watching — a CLI tail, a WebSocket consumer, a Telegram bot, a Slack webhook, a custom dashboard, whatever you wire up. The repo includes a Telegram bridge because that's what we use, but slinkd doesn't care what's on the other end. One curl in a catch block and you go from "I had no idea" to "I knew within seconds."

```bash
curl -X POST http://slinkd:8080/channels/my-app/events \
  -H "Authorization: Bearer $SLINKD_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"type":"alert","text":"Payment gateway timed out","author":"my-service"}'
```

Your phone buzzes. You know.

## Why It Feels Dumb

Because conceptually it's just "send an HTTP request when something goes wrong." You could curl Telegram directly. You could email yourself. You could write to a Slack webhook. There's no machine learning, no dashboards, no fancy alerting rules. It's embarrassingly simple.

## Why It's Actually Useful

Because those "just curl Telegram directly" solutions fall apart in practice:

- You hardcode Telegram tokens into 6 different services, then you rotate the token and miss one
- You have no history — once a Telegram message scrolls by, it's gone. slinkd stores everything in a database you can query later
- You can't watch events live across all your systems in one stream — `slinkd tail` gives you a unified live feed
- Different services need different alert channels, but you want one system managing the routing
- You want rate limiting so a flapping service doesn't blow up your phone with 400 identical messages in a minute

slinkd is the difference between "I should set up monitoring someday" and actually having it running. It's low enough friction that you'll actually wire it into things.

## The Value Is Not the Technology

The value is in closing the gap between "something broke" and "I know something broke."

That gap is where silent failures live. Not the errors you catch and handle — the ones you never even notice. A database connection that silently drops. An API key that expired. A cron job that stopped running and nobody realized for a week.

Enterprise shops have Nagios, Datadog, PagerDuty for this. Those are complex systems for teams of people with ops budgets. slinkd is for one person (or a small crew) running a handful of services who just wants their phone to buzz when something goes wrong.

## Use Cases

### The Basics

**Silent failure detection.** Any catch block, error handler, or sad path — add one HTTP POST. Now you know about it instead of discovering it next week.

**Deployment tracking.** CI/CD pipelines post `type: "deployment"` events. You see what shipped and when, in one place.

**Cron job health checks.** If a cron job fails (or doesn't run at all), post an alert. No more assuming things are fine because nobody complained.

**Workflow monitoring.** Automation tools like n8n post events after each run — what happened, what it cost, whether it worked.

### Multi-Agent Systems

This is where slinkd goes from "nice to have" to "how did I ever run this without it."

When you have one script, you watch the terminal. When you have 5 agents running concurrently — researching, writing, coding, reviewing — you're blind. Each agent is off doing its own thing, maybe succeeding, maybe burning tokens in a loop, maybe stuck. slinkd gives you a nervous system for the whole swarm.

**Cost tracking in real time.** Every agent posts its token usage after each LLM call. Your phone buzzes: "research-agent has spent $4.20 in the last 10 minutes." Without that, you find out tomorrow morning when you check your API dashboard and see a $60 bill from a retry loop.

**Agent lifecycle.** Agent spawned, agent finished, agent errored, agent timed out. `slinkd tail --channel agents` shows you a live feed of your entire system without opening 5 terminal windows.

**Quality gates.** A coding agent finishes a PR but the review agent flags something suspicious — maybe it deleted a file it shouldn't have. That's an alert. Your phone buzzes, you go look before it merges.

**Stuck agent detection.** A watchdog checks: did agent X post a `completed` event within the timeout? If not, fire an alert. Now you catch hung agents instead of discovering them hours later.

**Human-in-the-loop checkpoints.** Agent hits a decision it's not confident about. Instead of blocking silently in a terminal, it posts to slinkd, your phone buzzes, you respond. The agent polls the same channel for your approval.

**Cross-agent observability.** Agents post their handoffs — "research done, passing 12 sources to writer." When the final output is bad, you query the events and see exactly where things went sideways. Which agent dropped the ball? What did it receive? What did it send?

**Shared resource awareness.** An agent hits a rate limit on an external API. It posts to slinkd. Now every other agent sharing that resource can see it, and so can you.

**Batch run summaries.** Kick off a fleet of agents at midnight. Wake up and check `slinkd events --channel nightly-run` — 47 tasks completed, 3 failed, 1 alert about a malformed input. All in one place instead of scattered across log files.

## The Bottom Line

slinkd is the ops equivalent of `print("something broke")` — but instead of printing to a console nobody's watching, it prints to a centralized stream that anything can consume. Two database tables, one HTTP server, one WebSocket hub. No Kafka, no Kubernetes, no vendor lock-in. Plug in whatever notification layer you want.

The dumbness is the feature. If your system can POST JSON, it can report to slinkd.
