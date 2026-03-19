package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	db      *pgxpool.Pool
	apiKey  string
	readKey string
	hub     *Hub
)

// Hub manages WebSocket subscribers per channel.
type Hub struct {
	mu          sync.RWMutex
	subscribers map[string]map[*Client]bool
}

type Client struct {
	send chan []byte
}

func newHub() *Hub {
	return &Hub{subscribers: make(map[string]map[*Client]bool)}
}

func (h *Hub) subscribe(channel string, c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscribers[channel] == nil {
		h.subscribers[channel] = make(map[*Client]bool)
	}
	h.subscribers[channel][c] = true
}

func (h *Hub) unsubscribe(channel string, c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers[channel], c)
}

func (h *Hub) broadcast(channel string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.subscribers[channel] {
		select {
		case c.send <- data:
		default:
			// drop slow clients
		}
	}
}

func main() {
	apiKey = os.Getenv("SLINKD_API_KEY")
	if apiKey == "" {
		log.Fatal("SLINKD_API_KEY is required")
	}
	readKey = os.Getenv("SLINKD_READ_KEY")

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://localhost:5432/slinkd?sslmode=disable"
	}

	var err error
	db, err = pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer db.Close()

	if err := migrate(context.Background()); err != nil {
		log.Fatalf("migration failed: %v", err)
	}

	hub = newHub()

	mux := http.NewServeMux()
	mux.HandleFunc("/channels", authMiddleware(handleChannels))
	mux.HandleFunc("/channels/", authMiddleware(handleChannelRoutes))
	mux.HandleFunc("/ws", authMiddleware(handleWS))

	addr := os.Getenv("SLINKD_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	log.Printf("slinkd listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		key = strings.TrimPrefix(key, "Bearer ")

		if key == apiKey {
			next(w, r)
			return
		}

		if readKey != "" && key == readKey && r.Method == http.MethodGet {
			next(w, r)
			return
		}

		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

func handleChannelRoutes(w http.ResponseWriter, r *http.Request) {
	// Routes: /channels/{id}/events
	path := strings.TrimPrefix(r.URL.Path, "/channels/")
	parts := strings.SplitN(path, "/", 2)
	channelID := parts[0]

	if len(parts) == 2 && parts[1] == "events" {
		switch r.Method {
		case http.MethodPost:
			handlePublishEvent(w, r, channelID)
		case http.MethodGet:
			handleGetEvents(w, r, channelID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	http.NotFound(w, r)
}

func migrate(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS channels (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW()) * 1000)::BIGINT
	);
	CREATE TABLE IF NOT EXISTS events (
		id UUID PRIMARY KEY,
		channel TEXT NOT NULL REFERENCES channels(id),
		type TEXT NOT NULL,
		timestamp BIGINT NOT NULL,
		author TEXT NOT NULL DEFAULT '',
		data JSONB,
		text TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_events_channel_ts ON events(channel, timestamp);
	`
	_, err := db.Exec(ctx, schema)
	return err
}
