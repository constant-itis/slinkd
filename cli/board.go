package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
