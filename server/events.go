package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
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
	ID         string          `json:"id"`
	Channel    string          `json:"channel"`
	Type       string          `json:"type"`
	Timestamp  int64           `json:"timestamp"`
	Author     string          `json:"author"`
	Data       json.RawMessage `json:"data,omitempty"`
	Text       string          `json:"text"`
	TaskRef    *string         `json:"task_ref,omitempty"`
	MsgStatus  string          `json:"msg_status,omitempty"`
	AckedBy    string          `json:"acked_by,omitempty"`
	AckedAt    *int64          `json:"acked_at,omitempty"`
	ArchivedAt *int64          `json:"archived_at,omitempty"`
}

// publishEvent inserts an event into the DB, broadcasts it via WebSocket, and notifies watchers.
func publishEvent(ctx context.Context, channel, eventType, author, text string, data json.RawMessage) (Event, error) {
	return publishEventWithRef(ctx, channel, eventType, author, text, data, nil)
}

func publishEventWithRef(ctx context.Context, channel, eventType, author, text string, data json.RawMessage, taskRef *string) (Event, error) {
	msgStatus := ""
	if eventType == "message" {
		msgStatus = "unacked"
	}

	ev := Event{
		ID:        uuid.New().String(),
		Channel:   channel,
		Type:      eventType,
		Timestamp: time.Now().UnixMilli(),
		Author:    author,
		Data:      data,
		Text:      text,
		TaskRef:   taskRef,
		MsgStatus: msgStatus,
	}

	_, err := db.Exec(ctx,
		`INSERT INTO events (id, channel, type, timestamp, author, data, text, task_ref, msg_status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		ev.ID, ev.Channel, ev.Type, ev.Timestamp, ev.Author, ev.Data, ev.Text, ev.TaskRef, ev.MsgStatus,
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
		Type    string          `json:"type"`
		Author  string          `json:"author"`
		Data    json.RawMessage `json:"data,omitempty"`
		Text    string          `json:"text"`
		TaskRef *string         `json:"task_ref,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if !validEventTypes[req.Type] {
		http.Error(w, "invalid event type", http.StatusBadRequest)
		return
	}

	ev, err := publishEventWithRef(r.Context(), channelID, req.Type, req.Author, req.Text, req.Data, req.TaskRef)
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
	statusFilter := r.URL.Query().Get("status")
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
		statusCond := ""
		statusArgs := []interface{}{channelID, cursorTS}
		if statusFilter != "" && statusFilter != "all" {
			statusCond = " AND msg_status = $3"
			statusArgs = append(statusArgs, statusFilter)
		}
		limitArg := len(statusArgs) + 1
		rows, qErr := db.Query(r.Context(),
			fmt.Sprintf(`SELECT id, channel, type, timestamp, author, data, text, task_ref, msg_status, acked_by, acked_at, archived_at
			 FROM events WHERE channel = $1 AND timestamp > $2%s
			 ORDER BY timestamp ASC LIMIT $%d`, statusCond, limitArg),
			append(statusArgs, limit)...,
		)
		if qErr != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		events, err = scanEvents(rows)
	} else {
		statusCond := ""
		statusArgs := []interface{}{channelID}
		if statusFilter != "" && statusFilter != "all" {
			statusCond = " AND msg_status = $2"
			statusArgs = append(statusArgs, statusFilter)
		}
		limitArg := len(statusArgs) + 1
		rows, qErr := db.Query(r.Context(),
			fmt.Sprintf(`SELECT id, channel, type, timestamp, author, data, text, task_ref, msg_status, acked_by, acked_at, archived_at
			 FROM events WHERE channel = $1%s
			 ORDER BY timestamp ASC LIMIT $%d`, statusCond, limitArg),
			append(statusArgs, limit)...,
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
		if err := rows.Scan(&ev.ID, &ev.Channel, &ev.Type, &ev.Timestamp, &ev.Author, &ev.Data, &ev.Text, &ev.TaskRef, &ev.MsgStatus, &ev.AckedBy, &ev.AckedAt, &ev.ArchivedAt); err != nil {
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


// --- Message lifecycle handlers ---

func handleMessageRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/messages/")
	parts := strings.SplitN(path, "/", 2)
	messageID := parts[0]

	if messageID == "" {
		http.NotFound(w, r)
		return
	}

	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	switch parts[1] {
	case "ack":
		if r.Method == http.MethodPost {
			handleAckMessage(w, r, messageID)
			return
		}
	case "archive":
		if r.Method == http.MethodPost {
			handleArchiveMessage(w, r, messageID)
			return
		}
	}

	http.NotFound(w, r)
}

func handleAckMessage(w http.ResponseWriter, r *http.Request, messageID string) {
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	now := time.Now().UnixMilli()

	result, err := db.Exec(ctx,
		`UPDATE events SET msg_status = 'acked', acked_by = $1, acked_at = $2
		 WHERE id = $3 AND msg_status = 'unacked'`,
		req.AgentID, now, messageID,
	)
	if err != nil {
		http.Error(w, "ack failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		http.Error(w, "message not found or already acked", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "acked", "message_id": messageID})
}

func handleArchiveMessage(w http.ResponseWriter, r *http.Request, messageID string) {
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	now := time.Now().UnixMilli()

	result, err := db.Exec(ctx,
		`UPDATE events SET msg_status = 'archived', archived_at = $1
		 WHERE id = $2 AND msg_status IN ('unacked', 'acked', 'captured')`,
		now, messageID,
	)
	if err != nil {
		http.Error(w, "archive failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		http.Error(w, "message not found or already archived", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "archived", "message_id": messageID})
}

// captureMessagesForTask marks all messages linked to a task as 'captured'
// when the task reaches 'done' status.
func captureMessagesForTask(ctx context.Context, taskID string) {
	_, err := db.Exec(ctx,
		`UPDATE events SET msg_status = 'captured'
		 WHERE task_ref = $1 AND msg_status IN ('unacked', 'acked')`,
		taskID,
	)
	if err != nil {
		log.Printf("captureMessagesForTask(%s): %v", taskID[:8], err)
	}
}
