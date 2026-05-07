package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Task struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Status       string          `json:"status"`
	Priority     int             `json:"priority"`
	Assignee     *string         `json:"assignee"`
	ParentTaskID *string         `json:"parent_task_id,omitempty"`
	CreatedBy    string          `json:"created_by"`
	Result       string          `json:"result"`
	ClaimedAt    *int64          `json:"claimed_at,omitempty"`
	StartedAt    *int64          `json:"started_at,omitempty"`
	CompletedAt  *int64          `json:"completed_at,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	CreatedAt    int64           `json:"created_at"`
	UpdatedAt    int64           `json:"updated_at"`
	Subtasks     []Task          `json:"subtasks,omitempty"`
}

// Valid status transitions: from -> []to
// Flow: todo → claimed → in_progress → review → done
//                                        ↓
//                                     qc-fail (needs human review)
var validTransitions = map[string][]string{
	"backlog":     {"todo", "cancelled"},
	"todo":        {"claimed", "cancelled"},
	"claimed":     {"in_progress", "todo", "cancelled"},
	"in_progress": {"review", "blocked", "cancelled"},
	"review":      {"done", "qc-fail", "in_progress", "claimed", "cancelled"},
	"qc-fail":     {"todo", "in_progress", "cancelled"},
	"blocked":     {"in_progress", "todo", "cancelled"},
	"cancelled":   {"backlog", "todo"},
}

var validStatuses = map[string]bool{
	"backlog": true, "todo": true, "claimed": true,
	"in_progress": true, "review": true, "qc-fail": true,
	"blocked": true, "done": true, "cancelled": true,
}

// emitTaskEvent publishes a task_update event to the project's channel.
func emitTaskEvent(ctx context.Context, task Task, action, actor string) error {
	var channel string
	err := db.QueryRow(ctx, `SELECT channel FROM projects WHERE id = $1`, task.ProjectID).Scan(&channel)
	if err != nil {
		return fmt.Errorf("lookup project channel: %w", err)
	}

	data, _ := json.Marshal(map[string]interface{}{
		"task_id":    task.ID,
		"action":     action,
		"title":      task.Title,
		"status":     task.Status,
		"assignee":   task.Assignee,
		"priority":   task.Priority,
		"project_id": task.ProjectID,
	})

	text := fmt.Sprintf("Task %s: %s", action, task.Title)
	if task.Assignee != nil && *task.Assignee != "" {
		text += fmt.Sprintf(" [%s]", *task.Assignee)
	}

	_, err = publishEvent(ctx, channel, "task_update", actor, text, data)
	return err
}

func scanTask(row interface{ Scan(dest ...any) error }) (Task, error) {
	var t Task
	err := row.Scan(
		&t.ID, &t.ProjectID, &t.Title, &t.Description, &t.Status,
		&t.Priority, &t.Assignee, &t.ParentTaskID, &t.CreatedBy, &t.Result,
		&t.ClaimedAt, &t.StartedAt, &t.CompletedAt, &t.Metadata,
		&t.CreatedAt, &t.UpdatedAt,
	)
	return t, err
}

const taskColumns = `id, project_id, title, description, status, priority, assignee, parent_task_id, created_by, result, claimed_at, started_at, completed_at, metadata, created_at, updated_at`

// --- Top-level task routes: /tasks and /tasks/{id}... ---

func handleTasksTopLevel(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// Cross-project task list with filters
		projectID := r.URL.Query().Get("project")
		handleListTasks(w, r, projectID)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func handleTaskRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/tasks/")
	parts := strings.SplitN(path, "/", 2)
	taskID := parts[0]

	if taskID == "" {
		http.NotFound(w, r)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			handleGetTask(w, r, taskID)
		case http.MethodPatch:
			handleUpdateTask(w, r, taskID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	if parts[1] == "transition" && r.Method == http.MethodPost {
		handleTaskTransition(w, r, taskID)
		return
	}

	if parts[1] == "cancel" && r.Method == http.MethodPatch {
		handleCancelTask(w, r, taskID)
		return
	}

	http.NotFound(w, r)
}

// --- Handlers ---

func handleCreateTask(w http.ResponseWriter, r *http.Request, projectID string) {
	var req struct {
		Title        string          `json:"title"`
		Description  string          `json:"description"`
		Priority     int             `json:"priority"`
		Assignee     *string         `json:"assignee"`
		ParentTaskID *string         `json:"parent_task_id"`
		CreatedBy    string          `json:"created_by"`
		Status       string          `json:"status"`
		Metadata     json.RawMessage `json:"metadata,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	if req.Status == "" {
		req.Status = "backlog"
	}
	if !validStatuses[req.Status] {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	now := time.Now().UnixMilli()
	id := uuid.New().String()

	t, err := scanTask(db.QueryRow(ctx,
		`INSERT INTO tasks (id, project_id, title, description, status, priority, assignee, parent_task_id, created_by, metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
		 RETURNING `+taskColumns,
		id, projectID, req.Title, req.Description, req.Status, req.Priority,
		req.Assignee, req.ParentTaskID, req.CreatedBy, req.Metadata, now,
	))
	if err != nil {
		http.Error(w, "failed to create task: "+err.Error(), http.StatusInternalServerError)
		return
	}

	actor := req.CreatedBy
	if actor == "" {
		actor = "unknown"
	}
	_ = emitTaskEvent(ctx, t, "created", actor)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(t)
}

func handleListTasks(w http.ResponseWriter, r *http.Request, projectID string) {
	ctx := r.Context()
	query := r.URL.Query()

	// Build dynamic WHERE clause
	conditions := []string{}
	args := []interface{}{}
	argN := 1

	if projectID != "" {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", argN))
		args = append(args, projectID)
		argN++
	}

	if statuses := query.Get("status"); statuses != "" {
		statusList := strings.Split(statuses, ",")
		placeholders := make([]string, len(statusList))
		for i, s := range statusList {
			placeholders[i] = fmt.Sprintf("$%d", argN)
			args = append(args, strings.TrimSpace(s))
			argN++
		}
		conditions = append(conditions, fmt.Sprintf("status IN (%s)", strings.Join(placeholders, ",")))
	}

	if assignee := query.Get("assignee"); assignee != "" {
		conditions = append(conditions, fmt.Sprintf("assignee = $%d", argN))
		args = append(args, assignee)
		argN++
	}

	// Only top-level tasks by default (no subtasks in list view)
	if query.Get("include_subtasks") != "true" {
		conditions = append(conditions, "parent_task_id IS NULL")
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	limit := 50
	if l := query.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	sql := fmt.Sprintf(
		`SELECT %s FROM tasks %s ORDER BY priority DESC, created_at DESC LIMIT $%d`,
		taskColumns, where, argN,
	)
	args = append(args, limit)

	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		http.Error(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	tasks := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			http.Error(w, "scan error", http.StatusInternalServerError)
			return
		}
		tasks = append(tasks, t)
	}

	// Get total count for the same filters (without limit)
	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM tasks %s`, where)
	countArgs := args[:len(args)-1] // exclude the limit arg
	var count int
	_ = db.QueryRow(ctx, countSQL, countArgs...).Scan(&count)

	resp := struct {
		Tasks []Task `json:"tasks"`
		Count int    `json:"count"`
	}{Tasks: tasks, Count: count}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleGetTask(w http.ResponseWriter, r *http.Request, taskID string) {
	ctx := r.Context()

	t, err := scanTask(db.QueryRow(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE id = $1`, taskID))
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	// Fetch subtasks
	rows, err := db.Query(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE parent_task_id = $1 ORDER BY priority DESC, created_at`, taskID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			sub, err := scanTask(rows)
			if err != nil {
				break
			}
			t.Subtasks = append(t.Subtasks, sub)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

func handleUpdateTask(w http.ResponseWriter, r *http.Request, taskID string) {
	var req struct {
		Title       *string         `json:"title"`
		Description *string         `json:"description"`
		Priority    *int            `json:"priority"`
		Assignee    *string         `json:"assignee"`
		Metadata    json.RawMessage `json:"metadata,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	now := time.Now().UnixMilli()

	// Build dynamic SET clause
	sets := []string{"updated_at = $1"}
	args := []interface{}{now}
	argN := 2

	if req.Title != nil {
		sets = append(sets, fmt.Sprintf("title = $%d", argN))
		args = append(args, *req.Title)
		argN++
	}
	if req.Description != nil {
		sets = append(sets, fmt.Sprintf("description = $%d", argN))
		args = append(args, *req.Description)
		argN++
	}
	if req.Priority != nil {
		sets = append(sets, fmt.Sprintf("priority = $%d", argN))
		args = append(args, *req.Priority)
		argN++
	}
	if req.Assignee != nil {
		sets = append(sets, fmt.Sprintf("assignee = $%d", argN))
		args = append(args, *req.Assignee)
		argN++
	}
	if req.Metadata != nil {
		sets = append(sets, fmt.Sprintf("metadata = $%d", argN))
		args = append(args, req.Metadata)
		argN++
	}

	sql := fmt.Sprintf(
		`UPDATE tasks SET %s WHERE id = $%d RETURNING %s`,
		strings.Join(sets, ", "), argN, taskColumns,
	)
	args = append(args, taskID)

	t, err := scanTask(db.QueryRow(ctx, sql, args...))
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	_ = emitTaskEvent(ctx, t, "updated", "")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

func handleTaskTransition(w http.ResponseWriter, r *http.Request, taskID string) {
	var req struct {
		Status  string `json:"status"`
		AgentID string `json:"agent_id"`
		Result  string `json:"result"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if !validStatuses[req.Status] {
		http.Error(w, "invalid target status", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	now := time.Now().UnixMilli()

	// Look up current task to validate transition
	var currentStatus string
	err := db.QueryRow(ctx, `SELECT status FROM tasks WHERE id = $1`, taskID).Scan(&currentStatus)
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	// Validate transition
	allowed := validTransitions[currentStatus]
	transitionOK := false
	for _, s := range allowed {
		if s == req.Status {
			transitionOK = true
			break
		}
	}
	if !transitionOK {
		http.Error(w, fmt.Sprintf("invalid transition: %s -> %s", currentStatus, req.Status), http.StatusConflict)
		return
	}

	// Build the atomic update based on target status
	var t Task

	switch req.Status {
	case "claimed":
		if req.AgentID == "" {
			http.Error(w, "agent_id is required for claiming", http.StatusBadRequest)
			return
		}
		// Auto-register the agent if it doesn't exist yet (ephemeral agents).
		// Refreshes last_seen on every claim — supports the watchdog pattern
		// without requiring an explicit POST /agents/{id} per claim.
		_, regErr := db.Exec(ctx,
			`INSERT INTO agents (id, name, last_seen, metadata)
			 VALUES ($1, $1, $2, '{"auto_registered": true}')
			 ON CONFLICT (id) DO UPDATE SET last_seen = $2`,
			req.AgentID, now,
		)
		if regErr != nil {
			log.Printf("agent auto-register failed: agent=%s err=%v", req.AgentID, regErr)
			http.Error(w, fmt.Sprintf("agent register failed: %v", regErr), http.StatusInternalServerError)
			return
		}
		// Atomic claim: WHERE status IN ('todo', 'review') ensures only one agent wins
		// 'review' allows QC agents to claim tasks for review
		t, err = scanTask(db.QueryRow(ctx,
			`UPDATE tasks SET status = 'claimed', assignee = $1, claimed_at = $2, updated_at = $2
			 WHERE id = $3 AND status IN ('todo', 'review')
			 RETURNING `+taskColumns,
			req.AgentID, now, taskID,
		))

	case "in_progress":
		t, err = scanTask(db.QueryRow(ctx,
			`UPDATE tasks SET status = 'in_progress', started_at = COALESCE(started_at, $1), updated_at = $1
			 WHERE id = $2 AND status IN ('claimed', 'blocked')
			 RETURNING `+taskColumns,
			now, taskID,
		))

	case "review":
		// Worker finished — move to review for QC
		t, err = scanTask(db.QueryRow(ctx,
			`UPDATE tasks SET status = 'review', result = $1, updated_at = $2
			 WHERE id = $3 AND status = 'in_progress'
			 RETURNING `+taskColumns,
			req.Result, now, taskID,
		))

	case "done":
		// Only QC can mark done (from review)
		t, err = scanTask(db.QueryRow(ctx,
			`UPDATE tasks SET status = 'done', completed_at = $1, updated_at = $1
			 WHERE id = $2 AND status = 'review'
			 RETURNING `+taskColumns,
			now, taskID,
		))

	case "qc-fail":
		// QC rejected — goes to review queue for human
		t, err = scanTask(db.QueryRow(ctx,
			`UPDATE tasks SET status = 'qc-fail', result = $1, updated_at = $2
			 WHERE id = $3 AND status = 'review'
			 RETURNING `+taskColumns,
			req.Result, now, taskID,
		))

	case "cancelled":
		t, err = scanTask(db.QueryRow(ctx,
			`UPDATE tasks SET status = 'cancelled', result = $1, completed_at = $2, updated_at = $2
			 WHERE id = $3 AND status NOT IN ('done', 'cancelled')
			 RETURNING `+taskColumns,
			req.Result, now, taskID,
		))

	case "todo":
		// Unclaim, promote from backlog, or re-open from qc-fail
		t, err = scanTask(db.QueryRow(ctx,
			`UPDATE tasks SET status = 'todo', assignee = NULL, claimed_at = NULL, updated_at = $1
			 WHERE id = $2 AND status IN ('backlog', 'claimed', 'qc-fail', 'blocked')
			 RETURNING `+taskColumns,
			now, taskID,
		))

	case "blocked":
		t, err = scanTask(db.QueryRow(ctx,
			`UPDATE tasks SET status = 'blocked', updated_at = $1
			 WHERE id = $2 AND status = 'in_progress'
			 RETURNING `+taskColumns,
			now, taskID,
		))

	case "backlog":
		// Reopen from cancelled
		t, err = scanTask(db.QueryRow(ctx,
			`UPDATE tasks SET status = 'backlog', assignee = NULL, claimed_at = NULL, started_at = NULL, completed_at = NULL, result = '', updated_at = $1
			 WHERE id = $2 AND status = 'cancelled'
			 RETURNING `+taskColumns,
			now, taskID,
		))
	}

	if err != nil {
		log.Printf("transition update failed: task=%s %s->%s err=%v", taskID[:8], currentStatus, req.Status, err)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, fmt.Sprintf("transition failed: task changed state during update (current=%s target=%s)", currentStatus, req.Status), http.StatusConflict)
		} else {
			http.Error(w, fmt.Sprintf("transition failed: %v", err), http.StatusInternalServerError)
		}
		return
	}

	action := fmt.Sprintf("%s->%s", currentStatus, req.Status)
	actor := req.AgentID
	if actor == "" {
		actor = "unknown"
	}
	_ = emitTaskEvent(ctx, t, action, actor)

	// Auto-capture linked messages when task is done
	if req.Status == "done" {
		captureMessagesForTask(ctx, taskID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

func handleCancelTask(w http.ResponseWriter, r *http.Request, taskID string) {
	var req struct {
		AgentID string `json:"agent_id"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	now := time.Now().UnixMilli()

	t, err := scanTask(db.QueryRow(ctx,
		`UPDATE tasks SET status = 'cancelled', result = $1, completed_at = $2, updated_at = $2
		 WHERE id = $3 AND status NOT IN ('done', 'cancelled')
		 RETURNING `+taskColumns,
		req.Reason, now, taskID,
	))
	if err != nil {
		http.Error(w, "cancel failed (task may already be done or cancelled)", http.StatusConflict)
		return
	}

	actor := req.AgentID
	if actor == "" {
		actor = "unknown"
	}
	_ = emitTaskEvent(ctx, t, "force-cancel", actor)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}
