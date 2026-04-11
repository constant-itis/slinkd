package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ANSI colors
const (
	reset   = "\033[0m"
	bold    = "\033[1m"
	dim     = "\033[2m"
	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	blue    = "\033[34m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
	white   = "\033[37m"
	gray    = "\033[90m"
)

type TaskResponse struct {
	Tasks []Task `json:"tasks"`
	Count int    `json:"count"`
}

type Task struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"project_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	Priority    int     `json:"priority"`
	Assignee    *string `json:"assignee"`
	CreatedBy   string  `json:"created_by"`
	Result      string  `json:"result"`
	ClaimedAt   *int64  `json:"claimed_at"`
	StartedAt   *int64  `json:"started_at"`
	CompletedAt *int64  `json:"completed_at"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
	Subtasks    []Task  `json:"subtasks,omitempty"`
}

type AgentResponse struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	LastSeen int64  `json:"last_seen"`
}

type ProjectResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Channel string `json:"channel"`
}

func statusColor(status string) string {
	switch status {
	case "backlog":
		return gray
	case "todo":
		return white
	case "claimed":
		return yellow
	case "in_progress":
		return cyan
	case "blocked":
		return red
	case "done":
		return green
	case "cancelled":
		return gray
	default:
		return white
	}
}

func statusIcon(status string) string {
	switch status {
	case "backlog":
		return "○"
	case "todo":
		return "◉"
	case "claimed":
		return "◎"
	case "in_progress":
		return "▶"
	case "blocked":
		return "✖"
	case "done":
		return "✔"
	case "cancelled":
		return "—"
	default:
		return "?"
	}
}

func priorityStr(p int) string {
	if p == 0 {
		return ""
	}
	return fmt.Sprintf(" %s!%d%s", yellow, p, reset)
}

func timeAgo(ms int64) string {
	if ms == 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func apiGet(path string, params map[string]string) ([]byte, error) {
	u := host + path
	if len(params) > 0 {
		v := url.Values{}
		for k, val := range params {
			v.Set(k, val)
		}
		u += "?" + v.Encode()
	}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func fetchTasks(project, status string) []Task {
	params := map[string]string{"limit": "100"}
	if status != "" {
		params["status"] = status
	}

	var path string
	if project != "" {
		path = "/projects/" + url.PathEscape(project) + "/tasks"
	} else {
		path = "/tasks"
		if project != "" {
			params["project"] = project
		}
	}

	data, err := apiGet(path, params)
	if err != nil {
		return nil
	}
	var resp TaskResponse
	json.Unmarshal(data, &resp)
	return resp.Tasks
}

func fetchAgents() []AgentResponse {
	data, err := apiGet("/agents", nil)
	if err != nil {
		return nil
	}
	var agents []AgentResponse
	json.Unmarshal(data, &agents)
	return agents
}

func fetchProjects() []ProjectResponse {
	data, err := apiGet("/projects", nil)
	if err != nil {
		return nil
	}
	var projects []ProjectResponse
	json.Unmarshal(data, &projects)
	return projects
}

// cmdBoard shows the kanban board
func cmdBoard(args []string) {
	project := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--project=") {
			project = strings.TrimPrefix(arg, "--project=")
		}
	}

	tasks := fetchTasks(project, "")
	agents := fetchAgents()

	// Group by status
	columns := []string{"backlog", "todo", "claimed", "in_progress", "blocked", "done"}
	grouped := map[string][]Task{}
	for _, t := range tasks {
		grouped[t.Status] = append(grouped[t.Status], t)
	}

	// Header
	title := "slinkd"
	if project != "" {
		title += " / " + project
	}
	fmt.Printf("\n%s%s %s%s\n", bold, title, dim+gray+time.Now().Format("15:04:05")+reset, reset)
	fmt.Println(strings.Repeat("─", 80))

	// Columns
	for _, col := range columns {
		tasks := grouped[col]
		if len(tasks) == 0 && (col == "backlog" || col == "done" || col == "cancelled") {
			continue // hide empty terminal columns
		}

		color := statusColor(col)
		icon := statusIcon(col)
		label := strings.ToUpper(col)
		if col == "in_progress" {
			label = "IN PROGRESS"
		}

		fmt.Printf("\n%s%s %s%s %s(%d)%s\n", color, icon, bold, label, dim, len(tasks), reset)

		if len(tasks) == 0 {
			fmt.Printf("  %s(empty)%s\n", dim, reset)
			continue
		}

		for _, t := range tasks {
			assignee := ""
			if t.Assignee != nil && *t.Assignee != "" {
				assignee = fmt.Sprintf(" %s→%s%s", dim, *t.Assignee, reset)
			}
			prio := priorityStr(t.Priority)

			age := ""
			switch col {
			case "in_progress":
				if t.StartedAt != nil {
					age = fmt.Sprintf(" %s%s%s", dim, timeAgo(*t.StartedAt), reset)
				}
			case "claimed":
				if t.ClaimedAt != nil {
					age = fmt.Sprintf(" %s%s%s", dim, timeAgo(*t.ClaimedAt), reset)
				}
			case "done":
				if t.CompletedAt != nil {
					age = fmt.Sprintf(" %s%s%s", dim, timeAgo(*t.CompletedAt), reset)
				}
			default:
				age = fmt.Sprintf(" %s%s%s", dim, timeAgo(t.CreatedAt), reset)
			}

			id := gray + t.ID[:8] + reset
			title := truncate(t.Title, 45)

			if t.ProjectID != project && project == "" {
				title = fmt.Sprintf("%s%s:%s%s", dim, t.ProjectID, reset, title)
			}

			fmt.Printf("  %s %s%s%s%s %s\n", id, title, prio, assignee, age, "")
		}
	}

	// Agent status bar
	if len(agents) > 0 {
		fmt.Println(strings.Repeat("─", 80))
		parts := []string{}
		for _, a := range agents {
			color := green
			if a.Status == "busy" {
				color = yellow
			}
			age := timeAgo(a.LastSeen)
			parts = append(parts, fmt.Sprintf("%s%s%s %s(%s, %s ago)%s", color, a.Name, reset, dim, a.Status, age, reset))
		}
		fmt.Printf("agents: %s\n", strings.Join(parts, "  "))
	}

	fmt.Println()
}

// cmdProjects shows all projects with task counts
func cmdProjects() {
	projects := fetchProjects()
	agents := fetchAgents()

	if len(projects) == 0 {
		fmt.Println("no projects")
		return
	}

	fmt.Printf("\n%s%s%s\n", bold, "projects", reset)
	fmt.Println(strings.Repeat("─", 80))

	for _, p := range projects {
		// Fetch task counts per status for this project
		allTasks := fetchTasks(p.ID, "")
		counts := map[string]int{}
		for _, t := range allTasks {
			counts[t.Status]++
		}

		total := len(allTasks)
		active := counts["todo"] + counts["claimed"] + counts["in_progress"]
		blocked := counts["blocked"]
		done := counts["done"]

		// Project header
		fmt.Printf("\n  %s%s%s %s%s%s", bold, p.Name, reset, dim, p.ID, reset)
		if p.Channel != p.ID {
			fmt.Printf(" %s→ #%s%s", dim, p.Channel, reset)
		}
		fmt.Println()

		// Task summary bar
		parts := []string{}
		if active > 0 {
			parts = append(parts, fmt.Sprintf("%s%d active%s", cyan, active, reset))
		}
		if blocked > 0 {
			parts = append(parts, fmt.Sprintf("%s%d blocked%s", red, blocked, reset))
		}
		if done > 0 {
			parts = append(parts, fmt.Sprintf("%s%d done%s", green, done, reset))
		}
		if counts["backlog"] > 0 {
			parts = append(parts, fmt.Sprintf("%s%d backlog%s", gray, counts["backlog"], reset))
		}

		if total == 0 {
			fmt.Printf("    %sno tasks%s\n", dim, reset)
		} else {
			fmt.Printf("    %s (%d total)\n", strings.Join(parts, "  "), total)
		}

		// Agents on this project
		data, err := apiGet("/projects/"+url.PathEscape(p.ID)+"/agents", nil)
		if err == nil {
			var members []struct {
				AgentID string `json:"agent_id"`
				Role    string `json:"role"`
			}
			json.Unmarshal(data, &members)
			if len(members) > 0 {
				names := []string{}
				for _, m := range members {
					// Find agent status
					status := ""
					for _, a := range agents {
						if a.ID == m.AgentID {
							color := green
							if a.Status == "busy" {
								color = yellow
							}
							status = fmt.Sprintf("%s%s%s", color, a.Status, reset)
							break
						}
					}
					names = append(names, fmt.Sprintf("%s %s(%s)%s", m.AgentID, dim, status, reset))
				}
				fmt.Printf("    agents: %s\n", strings.Join(names, ", "))
			}
		}
	}

	fmt.Println()
}

// cmdAgents shows all registered agents with status and project membership
func cmdAgents() {
	agents := fetchAgents()
	projects := fetchProjects()

	if len(agents) == 0 {
		fmt.Println("no agents registered")
		return
	}

	// Build agent -> projects map
	agentProjects := map[string][]string{}
	for _, p := range projects {
		data, err := apiGet("/projects/"+url.PathEscape(p.ID)+"/agents", nil)
		if err != nil {
			continue
		}
		var members []struct {
			AgentID string `json:"agent_id"`
			Role    string `json:"role"`
		}
		json.Unmarshal(data, &members)
		for _, m := range members {
			agentProjects[m.AgentID] = append(agentProjects[m.AgentID], p.ID)
		}
	}

	// Build agent -> active tasks map
	allTasks := fetchTasks("", "claimed,in_progress")
	agentTasks := map[string][]Task{}
	for _, t := range allTasks {
		if t.Assignee != nil && *t.Assignee != "" {
			agentTasks[*t.Assignee] = append(agentTasks[*t.Assignee], t)
		}
	}

	fmt.Printf("\n%s%s%s\n", bold, "agents", reset)
	fmt.Println(strings.Repeat("─", 80))

	for _, a := range agents {
		color := green
		statusLabel := "idle"
		switch a.Status {
		case "busy":
			color = yellow
			statusLabel = "busy"
		case "offline":
			color = red
			statusLabel = "offline"
		default:
			// Check if last_seen is stale (>10 min)
			if time.Since(time.UnixMilli(a.LastSeen)) > 10*time.Minute {
				color = gray
				statusLabel = a.Status + " " + dim + "(" + timeAgo(a.LastSeen) + " ago)"
			}
		}

		fmt.Printf("\n  %s●%s %s%s%s  %s%s%s\n", color, reset, bold, a.Name, reset, color, statusLabel, reset)

		// Projects
		if projs, ok := agentProjects[a.ID]; ok && len(projs) > 0 {
			fmt.Printf("    projects: %s\n", strings.Join(projs, ", "))
		} else {
			fmt.Printf("    projects: %snone%s\n", dim, reset)
		}

		// Active tasks
		if tasks, ok := agentTasks[a.ID]; ok && len(tasks) > 0 {
			for _, t := range tasks {
				icon := statusIcon(t.Status)
				sColor := statusColor(t.Status)
				age := ""
				if t.StartedAt != nil {
					age = fmt.Sprintf(" %s%s%s", dim, timeAgo(*t.StartedAt), reset)
				} else if t.ClaimedAt != nil {
					age = fmt.Sprintf(" %s%s%s", dim, timeAgo(*t.ClaimedAt), reset)
				}
				fmt.Printf("    %s%s%s %s%s%s%s\n", sColor, icon, reset, t.Title, age, " ", gray+t.ID[:8]+reset)
			}
		} else {
			fmt.Printf("    %sno active tasks%s\n", dim, reset)
		}
	}

	fmt.Println()
}

// cmdHistory shows completed tasks with results and timing
func cmdHistory(args []string) {
	project := ""
	limit := "20"
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--project="):
			project = strings.TrimPrefix(arg, "--project=")
		case strings.HasPrefix(arg, "--limit="):
			limit = strings.TrimPrefix(arg, "--limit=")
		}
	}

	tasks := fetchTasks(project, "done")
	if len(tasks) == 0 {
		fmt.Println("no completed tasks")
		return
	}

	// Respect limit
	maxItems := 20
	if n, err := strconv.Atoi(limit); err == nil && n > 0 {
		maxItems = n
	}
	if len(tasks) > maxItems {
		tasks = tasks[:maxItems]
	}

	title := "history"
	if project != "" {
		title += " / " + project
	}
	fmt.Printf("\n%s%s%s %s(%d completed)%s\n", bold, title, reset, dim, len(tasks), reset)
	fmt.Println(strings.Repeat("─", 80))

	for _, t := range tasks {
		assignee := "unassigned"
		if t.Assignee != nil && *t.Assignee != "" {
			assignee = *t.Assignee
		}

		// Duration: created → completed
		duration := ""
		if t.CompletedAt != nil {
			d := time.UnixMilli(*t.CompletedAt).Sub(time.UnixMilli(t.CreatedAt))
			if d < time.Minute {
				duration = fmt.Sprintf("%ds", int(d.Seconds()))
			} else if d < time.Hour {
				duration = fmt.Sprintf("%dm", int(d.Minutes()))
			} else {
				duration = fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
			}
		}

		completedStr := ""
		if t.CompletedAt != nil {
			completedStr = time.UnixMilli(*t.CompletedAt).Format("Jan 02 15:04")
		}

		proj := ""
		if project == "" {
			proj = fmt.Sprintf("%s%s:%s ", dim, t.ProjectID, reset)
		}

		fmt.Printf("\n  %s✔%s %s%s%s%s\n", green, reset, proj, bold, t.Title, reset)
		fmt.Printf("    %s%s → %s · %s · %s%s\n", dim, assignee, completedStr, duration, t.ID[:8], reset)
		if t.Result != "" {
			result := t.Result
			if len(result) > 120 {
				result = result[:117] + "..."
			}
			fmt.Printf("    %s%s%s\n", gray, result, reset)
		}
	}
	fmt.Println()
}

// cmdTaskDetail shows full task detail with event timeline
func cmdTaskDetail(taskID string) {
	// Try full ID first
	data, err := apiGet("/tasks/"+url.PathEscape(taskID), nil)

	var t Task
	if err != nil || json.Unmarshal(data, &t) != nil || t.ID == "" {
		// Try partial ID match across all statuses
		allTasks := fetchTasks("", "")
		matched := []Task{}
		for _, task := range allTasks {
			if strings.HasPrefix(task.ID, taskID) {
				matched = append(matched, task)
			}
		}
		if len(matched) == 0 {
			fatal("task not found: " + taskID)
		}
		if len(matched) > 1 {
			fmt.Printf("ambiguous ID %s, matches:\n", taskID)
			for _, task := range matched {
				fmt.Printf("  %s  %s\n", task.ID, task.Title)
			}
			return
		}
		data, err = apiGet("/tasks/"+url.PathEscape(matched[0].ID), nil)
		if err != nil {
			fatal("task not found")
		}
		taskID = matched[0].ID
		json.Unmarshal(data, &t)
	}

	color := statusColor(t.Status)
	icon := statusIcon(t.Status)
	assignee := "unassigned"
	if t.Assignee != nil && *t.Assignee != "" {
		assignee = *t.Assignee
	}

	fmt.Printf("\n%s%s%s %s%s%s\n", color, icon, reset, bold, t.Title, reset)
	fmt.Println(strings.Repeat("─", 80))
	fmt.Printf("  ID:         %s\n", t.ID)
	fmt.Printf("  Project:    %s\n", t.ProjectID)
	fmt.Printf("  Status:     %s%s%s\n", color, t.Status, reset)
	fmt.Printf("  Priority:   %d\n", t.Priority)
	fmt.Printf("  Assignee:   %s\n", assignee)
	fmt.Printf("  Created by: %s\n", t.CreatedBy)
	if t.Description != "" {
		fmt.Printf("  Description: %s\n", t.Description)
	}
	if t.Result != "" {
		fmt.Printf("  Result:     %s%s%s\n", green, t.Result, reset)
	}

	// Timestamps
	fmt.Printf("\n  %sTimeline:%s\n", bold, reset)
	fmt.Printf("    created    %s\n", time.UnixMilli(t.CreatedAt).Format("Jan 02 15:04:05"))
	if t.ClaimedAt != nil {
		d := time.UnixMilli(*t.ClaimedAt).Sub(time.UnixMilli(t.CreatedAt))
		fmt.Printf("    claimed    %s  %s+%s%s\n", time.UnixMilli(*t.ClaimedAt).Format("Jan 02 15:04:05"), dim, fmtDuration(d), reset)
	}
	if t.StartedAt != nil {
		base := t.CreatedAt
		if t.ClaimedAt != nil {
			base = *t.ClaimedAt
		}
		d := time.UnixMilli(*t.StartedAt).Sub(time.UnixMilli(base))
		fmt.Printf("    started    %s  %s+%s%s\n", time.UnixMilli(*t.StartedAt).Format("Jan 02 15:04:05"), dim, fmtDuration(d), reset)
	}
	if t.CompletedAt != nil {
		base := t.CreatedAt
		if t.StartedAt != nil {
			base = *t.StartedAt
		}
		d := time.UnixMilli(*t.CompletedAt).Sub(time.UnixMilli(base))
		fmt.Printf("    completed  %s  %s+%s%s\n", time.UnixMilli(*t.CompletedAt).Format("Jan 02 15:04:05"), dim, fmtDuration(d), reset)
	}

	// Fetch event timeline from the project channel
	var projectChannel string
	pData, pErr := apiGet("/projects/"+url.PathEscape(t.ProjectID), nil)
	if pErr == nil {
		var proj ProjectResponse
		json.Unmarshal(pData, &proj)
		projectChannel = proj.Channel
	}

	if projectChannel != "" {
		evData, evErr := apiGet("/channels/"+url.PathEscape(projectChannel)+"/events", map[string]string{"limit": "200"})
		if evErr == nil {
			var evResp struct {
				Events []struct {
					Type      string `json:"type"`
					Text      string `json:"text"`
					Author    string `json:"author"`
					Timestamp int64  `json:"timestamp"`
					Data      struct {
						TaskID string `json:"task_id"`
						Action string `json:"action"`
					} `json:"data"`
				} `json:"events"`
			}
			json.Unmarshal(evData, &evResp)

			events := []struct {
				Action    string
				Author    string
				Timestamp int64
			}{}
			for _, ev := range evResp.Events {
				if ev.Type == "task_update" && ev.Data.TaskID == t.ID {
					events = append(events, struct {
						Action    string
						Author    string
						Timestamp int64
					}{ev.Data.Action, ev.Author, ev.Timestamp})
				}
			}

			if len(events) > 0 {
				fmt.Printf("\n  %sEvents:%s\n", bold, reset)
				for _, ev := range events {
					ts := time.UnixMilli(ev.Timestamp).Format("15:04:05")
					fmt.Printf("    %s[%s]%s %s %s(%s)%s\n", dim, ts, reset, ev.Action, dim, ev.Author, reset)
				}
			}
		}
	}

	// Subtasks
	if len(t.Subtasks) > 0 {
		fmt.Printf("\n  %sSubtasks (%d):%s\n", bold, len(t.Subtasks), reset)
		for _, s := range t.Subtasks {
			sColor := statusColor(s.Status)
			sIcon := statusIcon(s.Status)
			fmt.Printf("    %s%s%s %s %s%s%s\n", sColor, sIcon, reset, s.Title, gray, s.ID[:8], reset)
		}
	}

	fmt.Println()
}

func fmtDuration(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// cmdTasks shows a filtered task list
func cmdTasks(args []string) {
	project := ""
	status := ""
	assignee := ""

	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--project="):
			project = strings.TrimPrefix(arg, "--project=")
		case strings.HasPrefix(arg, "--status="):
			status = strings.TrimPrefix(arg, "--status=")
		case strings.HasPrefix(arg, "--assignee="):
			assignee = strings.TrimPrefix(arg, "--assignee=")
		}
	}

	params := map[string]string{"limit": "50"}
	if status != "" {
		params["status"] = status
	}
	if assignee != "" {
		params["assignee"] = assignee
	}

	var path string
	if project != "" {
		path = "/projects/" + url.PathEscape(project) + "/tasks"
	} else {
		path = "/tasks"
	}

	data, err := apiGet(path, params)
	if err != nil {
		fatal(fmt.Sprintf("request failed: %v", err))
	}
	var resp TaskResponse
	json.Unmarshal(data, &resp)

	if len(resp.Tasks) == 0 {
		fmt.Println("no tasks")
		return
	}

	fmt.Printf("%s%d tasks%s\n\n", dim, resp.Count, reset)

	for _, t := range resp.Tasks {
		color := statusColor(t.Status)
		icon := statusIcon(t.Status)
		assignee := ""
		if t.Assignee != nil && *t.Assignee != "" {
			assignee = fmt.Sprintf(" → %s", *t.Assignee)
		}
		prio := priorityStr(t.Priority)
		proj := ""
		if project == "" {
			proj = fmt.Sprintf("%s%s:%s ", dim, t.ProjectID, reset)
		}

		fmt.Printf("  %s%s%s %s%s%s%s%s %s%s%s\n",
			color, icon, reset,
			proj,
			t.Title, prio, assignee,
			"",
			gray, t.ID[:8], reset)
	}
	fmt.Println()
}
