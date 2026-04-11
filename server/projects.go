package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Channel     string `json:"channel"`
	Description string `json:"description"`
	CreatedAt   int64  `json:"created_at"`
}

type AgentProject struct {
	AgentID   string `json:"agent_id"`
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
	JoinedAt  int64  `json:"joined_at"`
}

func handleProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		handleCreateProject(w, r)
	case http.MethodGet:
		handleListProjects(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleProjectRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/projects/")
	parts := strings.SplitN(path, "/", 2)
	projectID := parts[0]

	if projectID == "" {
		http.NotFound(w, r)
		return
	}

	if len(parts) == 1 {
		if r.Method == http.MethodGet {
			handleGetProject(w, r, projectID)
		} else {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	switch parts[1] {
	case "agents":
		switch r.Method {
		case http.MethodGet:
			handleListProjectAgents(w, r, projectID)
		case http.MethodPost:
			handleAddProjectAgent(w, r, projectID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "tasks":
		switch r.Method {
		case http.MethodGet:
			handleListTasks(w, r, projectID)
		case http.MethodPost:
			handleCreateTask(w, r, projectID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

func handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Channel     string `json:"channel"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	req.Name = strings.TrimSpace(req.Name)
	req.Channel = strings.TrimSpace(req.Channel)
	if req.ID == "" || req.Name == "" {
		http.Error(w, "id and name are required", http.StatusBadRequest)
		return
	}
	if req.Channel == "" {
		req.Channel = req.ID
	}

	ctx := r.Context()

	// Auto-create channel if it doesn't exist
	_, err := db.Exec(ctx,
		`INSERT INTO channels (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
		req.Channel, req.Name)
	if err != nil {
		http.Error(w, "failed to create channel: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var p Project
	err = db.QueryRow(ctx,
		`INSERT INTO projects (id, name, channel, description) VALUES ($1, $2, $3, $4)
		 RETURNING id, name, channel, description, created_at`,
		req.ID, req.Name, req.Channel, req.Description,
	).Scan(&p.ID, &p.Name, &p.Channel, &p.Description, &p.CreatedAt)
	if err != nil {
		http.Error(w, "failed to create project: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(p)
}

func handleListProjects(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(r.Context(),
		`SELECT id, name, channel, description, created_at FROM projects ORDER BY created_at`)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	projects := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Channel, &p.Description, &p.CreatedAt); err != nil {
			http.Error(w, "scan error", http.StatusInternalServerError)
			return
		}
		projects = append(projects, p)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(projects)
}

func handleGetProject(w http.ResponseWriter, r *http.Request, id string) {
	var p Project
	err := db.QueryRow(r.Context(),
		`SELECT id, name, channel, description, created_at FROM projects WHERE id = $1`, id,
	).Scan(&p.ID, &p.Name, &p.Channel, &p.Description, &p.CreatedAt)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func handleListProjectAgents(w http.ResponseWriter, r *http.Request, projectID string) {
	rows, err := db.Query(r.Context(),
		`SELECT ap.agent_id, ap.project_id, ap.role, ap.joined_at
		 FROM agent_projects ap WHERE ap.project_id = $1 ORDER BY ap.joined_at`, projectID)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	members := []AgentProject{}
	for rows.Next() {
		var ap AgentProject
		if err := rows.Scan(&ap.AgentID, &ap.ProjectID, &ap.Role, &ap.JoinedAt); err != nil {
			http.Error(w, "scan error", http.StatusInternalServerError)
			return
		}
		members = append(members, ap)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(members)
}

func handleAddProjectAgent(w http.ResponseWriter, r *http.Request, projectID string) {
	var req struct {
		AgentID string `json:"agent_id"`
		Role    string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.AgentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}
	if req.Role == "" {
		req.Role = "member"
	}

	now := time.Now().UnixMilli()
	var ap AgentProject
	err := db.QueryRow(r.Context(),
		`INSERT INTO agent_projects (agent_id, project_id, role, joined_at) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (agent_id, project_id) DO UPDATE SET role = EXCLUDED.role
		 RETURNING agent_id, project_id, role, joined_at`,
		req.AgentID, projectID, req.Role, now,
	).Scan(&ap.AgentID, &ap.ProjectID, &ap.Role, &ap.JoinedAt)
	if err != nil {
		http.Error(w, "failed to add agent: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(ap)
}
