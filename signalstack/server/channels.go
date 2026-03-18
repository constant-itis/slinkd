package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

type Channel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
}

func handleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		handleCreateChannel(w, r)
	case http.MethodGet:
		handleListChannels(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
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

	var ch Channel
	err := db.QueryRow(r.Context(),
		`INSERT INTO channels (id, name) VALUES ($1, $2) RETURNING id, name, created_at`,
		req.ID, req.Name,
	).Scan(&ch.ID, &ch.Name, &ch.CreatedAt)
	if err != nil {
		http.Error(w, "failed to create channel: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(ch)
}

func handleListChannels(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(r.Context(), `SELECT id, name, created_at FROM channels ORDER BY created_at`)
	if err != nil {
		http.Error(w, "failed to list channels", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	channels := []Channel{}
	for rows.Next() {
		var ch Channel
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.CreatedAt); err != nil {
			http.Error(w, "scan error", http.StatusInternalServerError)
			return
		}
		channels = append(channels, ch)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(channels)
}
