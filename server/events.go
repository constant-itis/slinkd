package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var validEventTypes = map[string]bool{
	"message":         true,
	"breaking_change": true,
	"alert":           true,
	"deployment":      true,
	"signal":          true,
	"task":            true,
	"task_result":     true,
	"task_update":     true,
}

type Event struct {
	ID        string          `json:"id"`
	Channel   string          `json:"channel"`
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"`
	Author    string          `json:"author"`
	Data      json.RawMessage `json:"data,omitempty"`
	Text      string          `json:"text"`
}

// publishEvent inserts an event into the DB, broadcasts it via WebSocket, and notifies watchers.
func publishEvent(ctx context.Context, channel, eventType, author, text string, data json.RawMessage) (Event, error) {
	ev := Event{
		ID:        uuid.New().String(),
		Channel:   channel,
		Type:      eventType,
		Timestamp: time.Now().UnixMilli(),
		Author:    author,
		Data:      data,
		Text:      text,
	}

	_, err := db.Exec(ctx,
		`INSERT INTO events (id, channel, type, timestamp, author, data, text)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		ev.ID, ev.Channel, ev.Type, ev.Timestamp, ev.Author, ev.Data, ev.Text,
	)
	if err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}

	msg, _ := json.Marshal(ev)
	hub.broadcast(channel, msg)

	// notify watchers (no-op if watchers not configured)
	notifyWatchers(channel, author, ev.Timestamp)

	return ev, nil
}

func handlePublishEvent(w http.ResponseWriter, r *http.Request, channelID string) {
	var req struct {
		Type   string          `json:"type"`
		Author string          `json:"author"`
		Data   json.RawMessage `json:"data,omitempty"`
		Text   string          `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if !validEventTypes[req.Type] {
		http.Error(w, "invalid event type", http.StatusBadRequest)
		return
	}

	ev, err := publishEvent(r.Context(), channelID, req.Type, req.Author, req.Text, req.Data)
	if err != nil {
		http.Error(w, "failed to publish event: "+err.Error(), http.StatusInternalServerError)
		return
	}

	msg, _ := json.Marshal(ev)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(msg)
}

func handleGetEvents(w http.ResponseWriter, r *http.Request, channelID string) {
	cursor := r.URL.Query().Get("cursor")
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	var events []Event
	var err error

	if cursor != "" {
		// cursor is a timestamp — get events after it
		cursorTS, parseErr := strconv.ParseInt(cursor, 10, 64)
		if parseErr != nil {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
		rows, qErr := db.Query(r.Context(),
			`SELECT id, channel, type, timestamp, author, data, text
			 FROM events WHERE channel = $1 AND timestamp > $2
			 ORDER BY timestamp ASC LIMIT $3`,
			channelID, cursorTS, limit,
		)
		if qErr != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		events, err = scanEvents(rows)
	} else {
		rows, qErr := db.Query(r.Context(),
			`SELECT id, channel, type, timestamp, author, data, text
			 FROM events WHERE channel = $1
			 ORDER BY timestamp ASC LIMIT $2`,
			channelID, limit,
		)
		if qErr != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		events, err = scanEvents(rows)
	}

	if err != nil {
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}

	var nextCursor string
	if len(events) == limit {
		nextCursor = strconv.FormatInt(events[len(events)-1].Timestamp, 10)
	}

	resp := struct {
		Events     []Event `json:"events"`
		NextCursor string  `json:"next_cursor,omitempty"`
	}{Events: events, NextCursor: nextCursor}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func scanEvents(rows interface {
	Next() bool
	Scan(dest ...any) error
}) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var ev Event
		if err := rows.Scan(&ev.ID, &ev.Channel, &ev.Type, &ev.Timestamp, &ev.Author, &ev.Data, &ev.Text); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	if events == nil {
		events = []Event{}
	}
	return events, nil
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	channelID := r.URL.Query().Get("channel")
	if channelID == "" {
		http.Error(w, "channel query param required", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	client := &Client{send: make(chan []byte, 64)}
	hub.subscribe(channelID, client)
	defer hub.unsubscribe(channelID, client)

	// read pump — just drain reads to detect close
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				close(client.send)
				return
			}
		}
	}()

	for msg := range client.send {
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}
