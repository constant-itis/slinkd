package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Session is an ephemeral run of an agent on a specific machine. It is a
// sub-dimension of agent identity, not a replacement: agent_id stays the
// durable identity, while session_id/host/os pin the individual window.
// agent_id is a soft reference (no FK) so registration never fails on an
// unregistered agent — attribution is best-effort, not a gate.
type Session struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	Host      string `json:"host"`
	OS        string `json:"os"`
	Client    string `json:"client"`
	StartedAt int64  `json:"started_at"`
	LastSeen  int64  `json:"last_seen"`
	EndedAt   *int64 `json:"ended_at,omitempty"`
}

const sessionCols = `session_id, agent_id, host, os, client, started_at, last_seen, ended_at`

func scanSession(row interface {
	Scan(dest ...any) error
}) (Session, error) {
	var s Session
	err := row.Scan(&s.SessionID, &s.AgentID, &s.Host, &s.OS, &s.Client, &s.StartedAt, &s.LastSeen, &s.EndedAt)
	return s, err
}

func handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		handleRegisterSession(w, r)
	case http.MethodGet:
		handleListSessions(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleSessionRoutes(w http.ResponseWriter, r *http.Request) {
	// Routes: /sessions/{id}, /sessions/{id}/heartbeat, /sessions/{id}/end
	path := strings.TrimPrefix(r.URL.Path, "/sessions/")
	parts := strings.SplitN(path, "/", 2)
	sessionID := parts[0]

	if sessionID == "" {
		http.NotFound(w, r)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			handleGetSession(w, r, sessionID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	switch parts[1] {
	case "heartbeat":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleSessionHeartbeat(w, r, sessionID)
	case "end":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleEndSession(w, r, sessionID)
	default:
		http.NotFound(w, r)
	}
}

func handleRegisterSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		Host      string `json:"host"`
		OS        string `json:"os"`
		Client    string `json:"client"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	req.SessionID = strings.TrimSpace(req.SessionID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.Host = strings.TrimSpace(req.Host)
	if req.SessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	if req.AgentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}
	if req.Host == "" {
		http.Error(w, "host is required", http.StatusBadRequest)
		return
	}

	now := time.Now().UnixMilli()
	// Re-register (e.g. session resume) reopens the same row: bump last_seen,
	// clear ended_at, keep the original started_at.
	s, err := scanSession(db.QueryRow(r.Context(),
		`INSERT INTO sessions (`+sessionCols+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $6, NULL)
		 ON CONFLICT (session_id) DO UPDATE SET
		   agent_id = EXCLUDED.agent_id,
		   host = EXCLUDED.host,
		   os = EXCLUDED.os,
		   client = EXCLUDED.client,
		   last_seen = EXCLUDED.last_seen,
		   ended_at = NULL
		 RETURNING `+sessionCols,
		req.SessionID, req.AgentID, req.Host, req.OS, req.Client, now,
	))
	if err != nil {
		http.Error(w, "failed to register session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(s)
}

func handleSessionHeartbeat(w http.ResponseWriter, r *http.Request, id string) {
	now := time.Now().UnixMilli()
	s, err := scanSession(db.QueryRow(r.Context(),
		`UPDATE sessions SET last_seen = $1 WHERE session_id = $2 RETURNING `+sessionCols,
		now, id,
	))
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

func handleEndSession(w http.ResponseWriter, r *http.Request, id string) {
	now := time.Now().UnixMilli()
	s, err := scanSession(db.QueryRow(r.Context(),
		`UPDATE sessions SET ended_at = $1, last_seen = $1 WHERE session_id = $2 RETURNING `+sessionCols,
		now, id,
	))
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

func handleGetSession(w http.ResponseWriter, r *http.Request, id string) {
	s, err := scanSession(db.QueryRow(r.Context(),
		`SELECT `+sessionCols+` FROM sessions WHERE session_id = $1`, id,
	))
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

func handleListSessions(w http.ResponseWriter, r *http.Request) {
	q := `SELECT ` + sessionCols + ` FROM sessions`
	conds := []string{}
	args := []any{}

	// ?live=1 → only sessions that haven't ended (the "who's online" view)
	if r.URL.Query().Get("live") == "1" {
		conds = append(conds, "ended_at IS NULL")
	}
	// ?agent=<id> → scope to one agent's sessions
	if agent := strings.TrimSpace(r.URL.Query().Get("agent")); agent != "" {
		args = append(args, agent)
		conds = append(conds, "agent_id = $"+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY last_seen DESC"

	rows, err := db.Query(r.Context(), q, args...)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			http.Error(w, "scan error", http.StatusInternalServerError)
			return
		}
		sessions = append(sessions, s)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}
