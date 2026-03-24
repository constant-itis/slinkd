package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config is the top-level slinkd configuration loaded from JSON.
type Config struct {
	InstanceName string          `json:"instance_name"`
	Watchers     []WatcherConfig `json:"watchers"`
	Peers        []PeerConfig    `json:"peers"`
}

type WatcherConfig struct {
	Channel         string `json:"channel"`
	Author          string `json:"author"`
	ExpectEvery     string `json:"expect_every"`
	AlertChannel    string `json:"alert_channel"`
	AlertAuthor     string `json:"alert_author"`
	Message         string `json:"message"`
	RecoveryMessage string `json:"recovery_message"`
	RemindEvery     string `json:"remind_every"`
}

type PeerConfig struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	HeartbeatEvery string `json:"heartbeat_every"`
	ExpectEvery    string `json:"expect_every"`
	AlertChannel   string `json:"alert_channel"`
}

type WatcherState struct {
	mu        sync.RWMutex
	lastSeen  map[string]int64 // "channel:author" or "peer:<name>" -> unix ms
	alertedAt map[string]int64 // key -> last alert time (unix ms)
	offline   map[string]bool  // key -> currently offline
}

var watcherState *WatcherState

func watcherKey(channel, author string) string {
	if author == "" {
		return channel + ":"
	}
	return channel + ":" + author
}

func peerKey(name string) string {
	return "peer:" + name
}

// notifyWatchers is called from publishEvent on every event. Updates lastSeen
// and fires recovery alerts if a source comes back online.
func notifyWatchers(channel, author string, timestamp int64) {
	if watcherState == nil {
		return
	}

	watcherState.mu.Lock()
	defer watcherState.mu.Unlock()

	// Update both the specific author key and the channel-wide key
	keys := []string{watcherKey(channel, author)}
	if author != "" {
		keys = append(keys, watcherKey(channel, ""))
	}

	for _, key := range keys {
		watcherState.lastSeen[key] = timestamp

		if watcherState.offline[key] {
			watcherState.offline[key] = false
			watcherState.alertedAt[key] = 0

			// Find the watcher config for this key to get recovery message
			if cfg := globalConfig; cfg != nil {
				for _, w := range cfg.Watchers {
					if watcherKey(w.Channel, w.Author) == key && w.RecoveryMessage != "" {
						alertAuthor := w.AlertAuthor
						if alertAuthor == "" {
							alertAuthor = "slinkd-watchdog"
						}
						go func(ch, auth, msg string) {
							if _, err := publishEvent(context.Background(), ch, "alert", auth, msg, nil); err != nil {
								log.Printf("watchdog: failed to publish recovery alert: %v", err)
							}
						}(w.AlertChannel, alertAuthor, w.RecoveryMessage)
						break
					}
				}
			}
		}
	}
}

var globalConfig *Config

// loadConfig reads the slinkd config from SLINKD_CONFIG env or slinkd.json.
func loadConfig() *Config {
	path := os.Getenv("SLINKD_CONFIG")
	if path == "" {
		path = "slinkd.json"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		log.Printf("watchdog: failed to read config %s: %v", path, err)
		return nil
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("watchdog: failed to parse config %s: %v", path, err)
		return nil
	}

	return &cfg
}

// startWatchers seeds lastSeen from DB, then starts the tick loop and peer pingers.
func startWatchers(cfg *Config, pool *pgxpool.Pool, h *Hub) {
	globalConfig = cfg

	watcherState = &WatcherState{
		lastSeen:  make(map[string]int64),
		alertedAt: make(map[string]int64),
		offline:   make(map[string]bool),
	}

	// Seed lastSeen from DB for each watcher
	ctx := context.Background()
	for _, w := range cfg.Watchers {
		key := watcherKey(w.Channel, w.Author)
		var ts *int64

		var err error
		if w.Author != "" {
			err = pool.QueryRow(ctx,
				`SELECT MAX(timestamp) FROM events WHERE channel = $1 AND author = $2`,
				w.Channel, w.Author,
			).Scan(&ts)
		} else {
			err = pool.QueryRow(ctx,
				`SELECT MAX(timestamp) FROM events WHERE channel = $1`,
				w.Channel,
			).Scan(&ts)
		}

		if err == nil && ts != nil {
			watcherState.lastSeen[key] = *ts
			log.Printf("watchdog: seeded %s = %s", key, time.UnixMilli(*ts).Format("15:04:05"))
		} else {
			log.Printf("watchdog: no history for %s (will not alert until first event)", key)
		}
	}

	// Start tick loop
	go tickLoop(cfg)

	// Start peer pingers
	for _, p := range cfg.Peers {
		go peerPinger(p)
	}

	total := len(cfg.Watchers) + len(cfg.Peers)
	log.Printf("watchdog: started (%d watchers, %d peers)", len(cfg.Watchers), len(cfg.Peers))
	_ = total
}

func tickLoop(cfg *Config) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now().UnixMilli()

		// Check channel watchers
		for _, w := range cfg.Watchers {
			checkWatcher(w, now)
		}

		// Check peers (using same logic)
		for _, p := range cfg.Peers {
			checkPeer(p, now)
		}
	}
}

func checkWatcher(w WatcherConfig, now int64) {
	key := watcherKey(w.Channel, w.Author)

	expectEvery, err := time.ParseDuration(w.ExpectEvery)
	if err != nil {
		log.Printf("watchdog: invalid expect_every %q for %s: %v", w.ExpectEvery, key, err)
		return
	}

	var remindEvery time.Duration
	if w.RemindEvery != "" {
		remindEvery, _ = time.ParseDuration(w.RemindEvery)
	}

	alertAuthor := w.AlertAuthor
	if alertAuthor == "" {
		alertAuthor = "slinkd-watchdog"
	}

	watcherState.mu.RLock()
	lastSeen := watcherState.lastSeen[key]
	isOffline := watcherState.offline[key]
	lastAlerted := watcherState.alertedAt[key]
	watcherState.mu.RUnlock()

	// Never seen = don't alert (prevents false alarms on startup)
	if lastSeen == 0 {
		return
	}

	elapsed := time.Duration(now-lastSeen) * time.Millisecond

	if elapsed > expectEvery {
		if !isOffline {
			// Just went offline — fire initial alert
			msg := w.Message
			if msg == "" {
				msg = fmt.Sprintf("%s went silent (no events in %s)", w.Channel, expectEvery)
			}

			watcherState.mu.Lock()
			watcherState.offline[key] = true
			watcherState.alertedAt[key] = now
			watcherState.mu.Unlock()

			if _, err := publishEvent(context.Background(), w.AlertChannel, "alert", alertAuthor, msg, nil); err != nil {
				log.Printf("watchdog: failed to publish alert for %s: %v", key, err)
			} else {
				log.Printf("watchdog: ALERT — %s", msg)
			}
		} else if remindEvery > 0 {
			// Already offline — check if reminder is due
			sinceLast := time.Duration(now-lastAlerted) * time.Millisecond
			if sinceLast >= remindEvery {
				msg := fmt.Sprintf("[reminder] %s", w.Message)

				watcherState.mu.Lock()
				watcherState.alertedAt[key] = now
				watcherState.mu.Unlock()

				if _, err := publishEvent(context.Background(), w.AlertChannel, "alert", alertAuthor, msg, nil); err != nil {
					log.Printf("watchdog: failed to publish reminder for %s: %v", key, err)
				}
			}
		}
	}
}

func checkPeer(p PeerConfig, now int64) {
	key := peerKey(p.Name)

	expectEvery, err := time.ParseDuration(p.ExpectEvery)
	if err != nil {
		log.Printf("watchdog: invalid expect_every %q for peer %s: %v", p.ExpectEvery, p.Name, err)
		return
	}

	watcherState.mu.RLock()
	lastSeen := watcherState.lastSeen[key]
	isOffline := watcherState.offline[key]
	watcherState.mu.RUnlock()

	if lastSeen == 0 {
		return
	}

	elapsed := time.Duration(now-lastSeen) * time.Millisecond

	if elapsed > expectEvery && !isOffline {
		msg := fmt.Sprintf("peer %s is unreachable (no response in %s)", p.Name, expectEvery)

		watcherState.mu.Lock()
		watcherState.offline[key] = true
		watcherState.alertedAt[key] = now
		watcherState.mu.Unlock()

		alertChannel := p.AlertChannel
		if alertChannel == "" {
			alertChannel = "alerts"
		}

		if _, err := publishEvent(context.Background(), alertChannel, "alert", "slinkd-watchdog", msg, nil); err != nil {
			log.Printf("watchdog: failed to publish peer alert for %s: %v", p.Name, err)
		} else {
			log.Printf("watchdog: ALERT — %s", msg)
		}
	} else if elapsed <= expectEvery && isOffline {
		// Peer recovered
		msg := fmt.Sprintf("peer %s is back online", p.Name)

		watcherState.mu.Lock()
		watcherState.offline[key] = false
		watcherState.alertedAt[key] = 0
		watcherState.mu.Unlock()

		alertChannel := p.AlertChannel
		if alertChannel == "" {
			alertChannel = "alerts"
		}

		if _, err := publishEvent(context.Background(), alertChannel, "alert", "slinkd-watchdog", msg, nil); err != nil {
			log.Printf("watchdog: failed to publish peer recovery for %s: %v", p.Name, err)
		} else {
			log.Printf("watchdog: RECOVERY — %s", msg)
		}
	}
}

func peerPinger(p PeerConfig) {
	interval, err := time.ParseDuration(p.HeartbeatEvery)
	if err != nil {
		log.Printf("watchdog: invalid heartbeat_every %q for peer %s: %v", p.HeartbeatEvery, p.Name, err)
		return
	}

	key := peerKey(p.Name)
	client := &http.Client{Timeout: 5 * time.Second}

	// Set initial lastSeen so we don't alert before first check
	watcherState.mu.Lock()
	watcherState.lastSeen[key] = time.Now().UnixMilli()
	watcherState.mu.Unlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		resp, err := client.Get(p.URL + "/healthz")
		if err != nil {
			log.Printf("watchdog: peer %s ping failed: %v", p.Name, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			watcherState.mu.Lock()
			watcherState.lastSeen[key] = time.Now().UnixMilli()
			watcherState.mu.Unlock()
		}
	}
}

// handleHealthz returns instance name and current timestamp. No auth required.
func handleHealthz(instanceName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"name": instanceName,
			"ts":   time.Now().UnixMilli(),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
