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

	// Load watchdog config (optional — no config = no watchers)
	cfg := loadConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/channels", authMiddleware(handleChannels))
	mux.HandleFunc("/channels/", authMiddleware(handleChannelRoutes))
	mux.HandleFunc("/ws", authMiddleware(handleWS))
	mux.HandleFunc("/projects", authMiddleware(handleProjects))
	mux.HandleFunc("/projects/", authMiddleware(handleProjectRoutes))
	mux.HandleFunc("/agents", authMiddleware(handleAgents))
	mux.HandleFunc("/agents/", authMiddleware(handleAgentRoutes))
	mux.HandleFunc("/tasks", authMiddleware(handleTasksTopLevel))
	mux.HandleFunc("/tasks/", authMiddleware(handleTaskRoutes))

	// /healthz is unauthenticated so peers can ping it
	instanceName := "slinkd"
	if cfg != nil && cfg.InstanceName != "" {
		instanceName = cfg.InstanceName
	}
	mux.HandleFunc("/healthz", handleHealthz(instanceName))

	if cfg != nil {
		startWatchers(cfg, db, hub)
	}

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

	CREATE TABLE IF NOT EXISTS projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		channel TEXT NOT NULL REFERENCES channels(id),
		description TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW()) * 1000)::BIGINT
	);

	CREATE TABLE IF NOT EXISTS agents (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'idle',
		last_seen BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW()) * 1000)::BIGINT,
		metadata JSONB,
		created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW()) * 1000)::BIGINT
	);

	CREATE TABLE IF NOT EXISTS agent_projects (
		agent_id TEXT NOT NULL REFERENCES agents(id),
		project_id TEXT NOT NULL REFERENCES projects(id),
		role TEXT NOT NULL DEFAULT 'member',
		joined_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW()) * 1000)::BIGINT,
		PRIMARY KEY (agent_id, project_id)
	);

	CREATE TABLE IF NOT EXISTS tasks (
		id UUID PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		title TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'backlog',
		priority INTEGER NOT NULL DEFAULT 0,
		assignee TEXT REFERENCES agents(id),
		parent_task_id UUID REFERENCES tasks(id),
		created_by TEXT NOT NULL DEFAULT '',
		result TEXT NOT NULL DEFAULT '',
		claimed_at BIGINT,
		started_at BIGINT,
		completed_at BIGINT,
		metadata JSONB,
		created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW()) * 1000)::BIGINT,
		updated_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW()) * 1000)::BIGINT
	);
	CREATE INDEX IF NOT EXISTS idx_tasks_project_status ON tasks(project_id, status);
	CREATE INDEX IF NOT EXISTS idx_tasks_assignee ON tasks(assignee);
	CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_task_id);
	`
	_, err := db.Exec(ctx, schema)
	return err
}
