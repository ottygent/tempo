package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

var errStateNotFound = errors.New("state not found")

type stateBackend interface {
	Load() (State, error)
	Save(State) error
	Check(context.Context) error
	Close(context.Context) error
	Name() string
}

type initialStateLoader func() (State, error)

type Store struct {
	mu      sync.RWMutex
	backend stateBackend
	data    State
}

func newStore(backend stateBackend, loadInitial initialStateLoader) (*Store, error) {
	s := &Store{backend: backend}
	state, err := backend.Load()
	if err != nil {
		if !errors.Is(err, errStateNotFound) {
			return nil, err
		}
		if loadInitial != nil {
			s.data, err = loadInitial()
			if err != nil {
				return nil, err
			}
		} else {
			s.data = seedState()
		}
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	} else {
		s.data = state
	}
	if s.data.Documents == nil {
		s.data.Documents = []Document{}
	}
	return s, nil
}

func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, _ := json.Marshal(s.data)
	var out State
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Store) saveLocked() error {
	return s.backend.Save(s.data)
}

func (s *Store) Check(ctx context.Context) error { return s.backend.Check(ctx) }
func (s *Store) Close(ctx context.Context) error { return s.backend.Close(ctx) }
func (s *Store) BackendName() string             { return s.backend.Name() }

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func clean(value string) string { return strings.TrimSpace(value) }

const (
	maxDocumentTitleLength  = 200
	maxDocumentContentBytes = 512 << 10
)

func validateDocument(title, content string) error {
	if clean(title) == "" {
		return errors.New("document title is required")
	}
	if len([]rune(clean(title))) > maxDocumentTitleLength {
		return errors.New("document title is too long")
	}
	if len(content) > maxDocumentContentBytes {
		return errors.New("document content is too large")
	}
	return nil
}

func (s *Store) AddWorkspace(input Workspace) (Workspace, error) {
	if clean(input.Name) == "" {
		return Workspace{}, errors.New("workspace name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	input.ID = newID("ws")
	input.Name = clean(input.Name)
	input.CreatedAt = time.Now().UTC()
	if input.Color == "" {
		input.Color = "#6c5ce7"
	}
	s.data.Workspaces = append(s.data.Workspaces, input)
	return input, s.saveLocked()
}

func (s *Store) AddProject(input Project) (Project, error) {
	if clean(input.Name) == "" || clean(input.WorkspaceID) == "" {
		return Project{}, errors.New("workspaceId and project name are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, w := range s.data.Workspaces {
		if w.ID == input.WorkspaceID {
			found = true
			break
		}
	}
	if !found {
		return Project{}, errors.New("workspace not found")
	}
	input.ID = newID("prj")
	input.Name = clean(input.Name)
	input.CreatedAt = time.Now().UTC()
	if input.Color == "" {
		input.Color = "#00b894"
	}
	if input.Status == "" {
		input.Status = "active"
	}
	s.data.Projects = append(s.data.Projects, input)
	return input, s.saveLocked()
}

func (s *Store) UpdateProject(id string, patch map[string]any) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Projects {
		if s.data.Projects[i].ID != id {
			continue
		}
		project := &s.data.Projects[i]
		status, ok := patch["status"].(string)
		if !ok || status != "archived" {
			return Project{}, errors.New("project status must be archived")
		}
		project.Status = status
		return *project, s.saveLocked()
	}
	return Project{}, errors.New("project not found")
}

func (s *Store) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	projectIndex := -1
	for i := range s.data.Projects {
		if s.data.Projects[i].ID == id {
			projectIndex = i
			break
		}
	}
	if projectIndex == -1 {
		return errors.New("project not found")
	}

	taskIDs := make(map[string]struct{})
	remainingTasks := s.data.Tasks[:0]
	for _, task := range s.data.Tasks {
		if task.ProjectID == id {
			taskIDs[task.ID] = struct{}{}
			continue
		}
		remainingTasks = append(remainingTasks, task)
	}
	remainingEntries := s.data.TimeEntries[:0]
	for _, entry := range s.data.TimeEntries {
		if _, deleted := taskIDs[entry.TaskID]; deleted {
			continue
		}
		remainingEntries = append(remainingEntries, entry)
	}

	s.data.Projects = append(s.data.Projects[:projectIndex], s.data.Projects[projectIndex+1:]...)
	s.data.Tasks = remainingTasks
	s.data.TimeEntries = remainingEntries
	remainingDocuments := s.data.Documents[:0]
	for _, document := range s.data.Documents {
		if document.ProjectID != id {
			remainingDocuments = append(remainingDocuments, document)
		}
	}
	s.data.Documents = remainingDocuments
	return s.saveLocked()
}

func (s *Store) AddDocument(input Document) (Document, error) {
	if err := validateDocument(input.Title, input.Content); err != nil {
		return Document{}, err
	}
	if clean(input.ProjectID) == "" {
		return Document{}, errors.New("projectId is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, project := range s.data.Projects {
		if project.ID == input.ProjectID && project.Status != "archived" {
			found = true
			break
		}
	}
	if !found {
		return Document{}, errors.New("active project not found")
	}
	now := time.Now().UTC()
	input.ID = newID("doc")
	input.Title = clean(input.Title)
	input.CreatedAt = now
	input.UpdatedAt = now
	s.data.Documents = append(s.data.Documents, input)
	return input, s.saveLocked()
}

func (s *Store) UpdateDocument(id string, patch map[string]any) (Document, error) {
	for key := range patch {
		if key != "title" && key != "content" {
			return Document{}, errors.New("unsupported document field")
		}
	}
	if len(patch) == 0 {
		return Document{}, errors.New("document update is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Documents {
		if s.data.Documents[i].ID != id {
			continue
		}
		document := &s.data.Documents[i]
		title, content := document.Title, document.Content
		if value, ok := patch["title"].(string); ok {
			title = value
		}
		if value, ok := patch["content"].(string); ok {
			content = value
		}
		if err := validateDocument(title, content); err != nil {
			return Document{}, err
		}
		document.Title = clean(title)
		document.Content = content
		document.UpdatedAt = time.Now().UTC()
		return *document, s.saveLocked()
	}
	return Document{}, errors.New("document not found")
}

func (s *Store) DeleteDocument(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Documents {
		if s.data.Documents[i].ID == id {
			s.data.Documents = append(s.data.Documents[:i], s.data.Documents[i+1:]...)
			return s.saveLocked()
		}
	}
	return errors.New("document not found")
}

func validStatus(status string) bool {
	return status == "backlog" || status == "todo" || status == "progress" || status == "review" || status == "done"
}

func (s *Store) AddTask(input Task) (Task, error) {
	if clean(input.Title) == "" || clean(input.ProjectID) == "" {
		return Task{}, errors.New("projectId and task title are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, p := range s.data.Projects {
		if p.ID == input.ProjectID {
			found = true
			break
		}
	}
	if !found {
		return Task{}, errors.New("project not found")
	}
	if input.Status == "" {
		input.Status = "todo"
	}
	if !validStatus(input.Status) {
		return Task{}, errors.New("invalid task status")
	}
	if input.Priority == "" {
		input.Priority = "medium"
	}
	if input.Tags == nil {
		input.Tags = []string{}
	}
	now := time.Now().UTC()
	input.ID = newID("tsk")
	input.Title = clean(input.Title)
	input.CreatedAt = now
	input.UpdatedAt = now
	s.data.Tasks = append(s.data.Tasks, input)
	return input, s.saveLocked()
}

func (s *Store) UpdateTask(id string, patch map[string]any) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID != id {
			continue
		}
		t := &s.data.Tasks[i]
		if v, ok := patch["title"].(string); ok && clean(v) != "" {
			t.Title = clean(v)
		}
		if v, ok := patch["description"].(string); ok {
			t.Description = clean(v)
		}
		if v, ok := patch["status"].(string); ok {
			if !validStatus(v) {
				return Task{}, errors.New("invalid task status")
			}
			t.Status = v
		}
		if v, ok := patch["priority"].(string); ok {
			t.Priority = v
		}
		if v, ok := patch["assignee"].(string); ok {
			t.Assignee = clean(v)
		}
		if v, ok := patch["startDate"].(string); ok {
			t.StartDate = v
		}
		if v, ok := patch["dueDate"].(string); ok {
			t.DueDate = v
		}
		if v, ok := patch["estimateMinutes"].(float64); ok {
			t.EstimateMinutes = int(v)
		}
		if v, ok := patch["tags"].([]any); ok {
			tags := make([]string, 0, len(v))
			for _, raw := range v {
				if tag, yes := raw.(string); yes && clean(tag) != "" {
					tags = append(tags, clean(tag))
				}
			}
			t.Tags = tags
		}
		t.UpdatedAt = time.Now().UTC()
		return *t, s.saveLocked()
	}
	return Task{}, errors.New("task not found")
}

func (s *Store) StartTimer(taskID string) (TimeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, t := range s.data.Tasks {
		if t.ID == taskID {
			found = true
			break
		}
	}
	if !found {
		return TimeEntry{}, errors.New("task not found")
	}
	for _, e := range s.data.TimeEntries {
		if e.StoppedAt == nil {
			return TimeEntry{}, errors.New("a timer is already running")
		}
	}
	e := TimeEntry{ID: newID("time"), TaskID: taskID, StartedAt: time.Now().UTC()}
	s.data.TimeEntries = append(s.data.TimeEntries, e)
	return e, s.saveLocked()
}

func (s *Store) StopTimer() (TimeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.data.TimeEntries) - 1; i >= 0; i-- {
		e := &s.data.TimeEntries[i]
		if e.StoppedAt != nil {
			continue
		}
		now := time.Now().UTC()
		e.StoppedAt = &now
		e.DurationSeconds = int64(now.Sub(e.StartedAt).Seconds())
		return *e, s.saveLocked()
	}
	return TimeEntry{}, errors.New("no timer is running")
}

func seedState() State {
	now := time.Now().UTC()
	day := 24 * time.Hour
	ws := Workspace{ID: "ws_northstar", Name: "Default workspace", Color: "#7c5cff", CreatedAt: now.Add(-30 * day)}
	p1 := Project{ID: "prj_launch", WorkspaceID: ws.ID, Name: "Product launch", Description: "Coordinate the summer release across product, content, and growth.", Color: "#ff7a59", Status: "active", StartDate: now.Add(-14 * day).Format("2006-01-02"), DueDate: now.Add(30 * day).Format("2006-01-02"), CreatedAt: now.Add(-14 * day)}
	p2 := Project{ID: "prj_mobile", WorkspaceID: ws.ID, Name: "Mobile refresh", Description: "A faster, calmer mobile experience.", Color: "#00b894", Status: "active", StartDate: now.Add(-7 * day).Format("2006-01-02"), DueDate: now.Add(45 * day).Format("2006-01-02"), CreatedAt: now.Add(-7 * day)}
	tasks := []Task{
		{ID: "tsk_research", ProjectID: p1.ID, Title: "Synthesize customer interviews", Description: "Turn 12 interviews into a decision-ready insight brief.", Status: "done", Priority: "high", Assignee: "Maya", StartDate: now.Add(-10 * day).Format("2006-01-02"), DueDate: now.Add(-4 * day).Format("2006-01-02"), EstimateMinutes: 240, Tags: []string{"research"}, CreatedAt: now.Add(-12 * day), UpdatedAt: now.Add(-3 * day)},
		{ID: "tsk_copy", ProjectID: p1.ID, Title: "Finalize launch narrative", Description: "Align homepage, email, and sales story.", Status: "progress", Priority: "high", Assignee: "Noah", StartDate: now.Add(-2 * day).Format("2006-01-02"), DueDate: now.Add(4 * day).Format("2006-01-02"), EstimateMinutes: 360, Tags: []string{"content", "launch"}, CreatedAt: now.Add(-8 * day), UpdatedAt: now},
		{ID: "tsk_qa", ProjectID: p1.ID, Title: "Cross-browser release QA", Description: "Desktop and mobile acceptance pass.", Status: "todo", Priority: "medium", Assignee: "Ari", StartDate: now.Add(5 * day).Format("2006-01-02"), DueDate: now.Add(9 * day).Format("2006-01-02"), EstimateMinutes: 300, Tags: []string{"quality"}, CreatedAt: now.Add(-3 * day), UpdatedAt: now},
		{ID: "tsk_metrics", ProjectID: p1.ID, Title: "Wire launch dashboard", Description: "Activation, retention, and revenue signals.", Status: "review", Priority: "medium", Assignee: "Sam", StartDate: now.Add(-1 * day).Format("2006-01-02"), DueDate: now.Add(3 * day).Format("2006-01-02"), EstimateMinutes: 180, Tags: []string{"analytics"}, CreatedAt: now.Add(-5 * day), UpdatedAt: now},
		{ID: "tsk_nav", ProjectID: p2.ID, Title: "Prototype thumb-first navigation", Description: "Validate key paths on compact screens.", Status: "progress", Priority: "high", Assignee: "Maya", StartDate: now.Format("2006-01-02"), DueDate: now.Add(7 * day).Format("2006-01-02"), EstimateMinutes: 420, Tags: []string{"mobile", "ux"}, CreatedAt: now.Add(-2 * day), UpdatedAt: now},
		{ID: "tsk_perf", ProjectID: p2.ID, Title: "Set performance budget", Description: "Define LCP and bundle targets.", Status: "backlog", Priority: "low", Assignee: "Ari", StartDate: "", DueDate: now.Add(15 * day).Format("2006-01-02"), EstimateMinutes: 120, Tags: []string{"performance"}, CreatedAt: now.Add(-1 * day), UpdatedAt: now},
	}
	stopped := now.Add(-2 * time.Hour)
	entries := []TimeEntry{{ID: "time_seed", TaskID: "tsk_copy", StartedAt: stopped.Add(-75 * time.Minute), StoppedAt: &stopped, DurationSeconds: 4500, Note: "Drafted narrative structure"}}
	return State{Version: 1, Workspaces: []Workspace{ws}, Projects: []Project{p1, p2}, Tasks: tasks, TimeEntries: entries, Documents: []Document{}}
}
