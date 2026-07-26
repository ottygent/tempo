package app

import "time"

type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	CreatedAt time.Time `json:"createdAt"`
}

type Project struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Color       string    `json:"color"`
	Status      string    `json:"status"`
	StartDate   string    `json:"startDate"`
	DueDate     string    `json:"dueDate"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Task struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"projectId"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	Status          string    `json:"status"`
	Priority        string    `json:"priority"`
	Assignee        string    `json:"assignee"`
	StartDate       string    `json:"startDate"`
	DueDate         string    `json:"dueDate"`
	EstimateMinutes int       `json:"estimateMinutes"`
	Tags            []string  `json:"tags"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type TimeEntry struct {
	ID              string     `json:"id"`
	TaskID          string     `json:"taskId"`
	StartedAt       time.Time  `json:"startedAt"`
	StoppedAt       *time.Time `json:"stoppedAt,omitempty"`
	DurationSeconds int64      `json:"durationSeconds"`
	Note            string     `json:"note"`
}

type State struct {
	Version     int         `json:"version"`
	Workspaces  []Workspace `json:"workspaces"`
	Projects    []Project   `json:"projects"`
	Tasks       []Task      `json:"tasks"`
	TimeEntries []TimeEntry `json:"timeEntries"`
}
