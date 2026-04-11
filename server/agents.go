package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type Agent struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	LastSeen  int64           `json:"last_seen"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt int64           `json:"created_at"`
}

func handleAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		handleRegisterAgent(w, r)
	case http.MethodGet:
		handleListAgents(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleAgentRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/agents/")
	parts := strings.SplitN(path, "/", 2)
	agentID := parts[0]

	if agentID == "" {
		http.NotFound(w, r)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			handleGetAgent(w, r, agentID)
		case http.MethodPatch:
			handleUpdateAgent(w, r, agentID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	http.NotFound(w, r)
}

func handleRegisterAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Metadata json.RawMessage `json:"metadata,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	req.Name = strings.TrimSpace(req.Name)
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		req.Name = req.ID
	}

	now := time.Now().UnixMilli()
	var a Agent
	err := db.QueryRow(r.Context(),
		`INSERT INTO agents (id, name, last_seen, metadata) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, last_seen = $3, metadata = COALESCE(EXCLUDED.metadata, agents.metadata)
		 RETURNING id, name, status, last_seen, metadata, created_at`,
		req.ID, req.Name, now, req.Metadata,
	).Scan(&a.ID, &a.Name, &a.Status, &a.LastSeen, &a.Metadata, &a.CreatedAt)
	if err != nil {
		http.Error(w, "failed to register agent: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(a)
}

func handleListAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(r.Context(),
		`SELECT id, name, status, last_seen, metadata, created_at FROM agents ORDER BY created_at`)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	agents := []Agent{}
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.Name, &a.Status, &a.LastSeen, &a.Metadata, &a.CreatedAt); err != nil {
			http.Error(w, "scan error", http.StatusInternalServerError)
			return
		}
		agents = append(agents, a)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agents)
}

func handleGetAgent(w http.ResponseWriter, r *http.Request, id string) {
	var a Agent
	err := db.QueryRow(r.Context(),
		`SELECT id, name, status, last_seen, metadata, created_at FROM agents WHERE id = $1`, id,
	).Scan(&a.ID, &a.Name, &a.Status, &a.LastSeen, &a.Metadata, &a.CreatedAt)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(a)
}

func handleUpdateAgent(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Status   *string         `json:"status"`
		Metadata json.RawMessage `json:"metadata,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	now := time.Now().UnixMilli()
	ctx := r.Context()

	var a Agent
	if req.Status != nil {
		err := db.QueryRow(ctx,
			`UPDATE agents SET status = $1, last_seen = $2, metadata = COALESCE($3, metadata)
			 WHERE id = $4
			 RETURNING id, name, status, last_seen, metadata, created_at`,
			*req.Status, now, req.Metadata, id,
		).Scan(&a.ID, &a.Name, &a.Status, &a.LastSeen, &a.Metadata, &a.CreatedAt)
		if err != nil {
			http.Error(w, "agent not found", http.StatusNotFound)
			return
		}
	} else {
		err := db.QueryRow(ctx,
			`UPDATE agents SET last_seen = $1, metadata = COALESCE($2, metadata)
			 WHERE id = $3
			 RETURNING id, name, status, last_seen, metadata, created_at`,
			now, req.Metadata, id,
		).Scan(&a.ID, &a.Name, &a.Status, &a.LastSeen, &a.Metadata, &a.CreatedAt)
		if err != nil {
			http.Error(w, "agent not found", http.StatusNotFound)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(a)
}
