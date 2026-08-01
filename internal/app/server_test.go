package app

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

const testPassword = "correct-horse-battery-staple"

func TestStorePersistsTaskAndTimer(t *testing.T) {
	store, backend := newMemoryStore(t)
	state := store.Snapshot()
	task, err := store.AddTask(Task{ProjectID: state.Projects[0].ID, Title: "Ship test", Status: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.StartTimer(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.StopTimer(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newStore(backend, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Snapshot()
	if len(got.Tasks) != len(state.Tasks)+1 || len(got.TimeEntries) != len(state.TimeEntries)+1 {
		t.Fatalf("state did not persist: %+v", got)
	}
}

func TestDocumentLifecycleAndProjectCascade(t *testing.T) {
	store, backend := newMemoryStore(t)
	projectID := store.Snapshot().Projects[0].ID
	document, err := store.AddDocument(Document{ProjectID: projectID, Title: "  Launch brief  ", Content: "# Draft"})
	if err != nil {
		t.Fatal(err)
	}
	if document.Title != "Launch brief" || document.ID == "" || document.CreatedAt.IsZero() || document.UpdatedAt.IsZero() {
		t.Fatalf("invalid document: %+v", document)
	}
	updated, err := store.UpdateDocument(document.ID, map[string]any{"title": "Launch plan", "content": "# Launch\n\nReady."})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Launch plan" || updated.Content != "# Launch\n\nReady." || !updated.UpdatedAt.After(document.UpdatedAt) {
		t.Fatalf("document not updated: %+v", updated)
	}
	if _, err := store.UpdateDocument(document.ID, map[string]any{"title": " "}); err == nil {
		t.Fatal("blank title accepted")
	}
	reloaded, err := newStore(backend, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Snapshot().Documents) != 1 || reloaded.Snapshot().Documents[0].Content != updated.Content {
		t.Fatal("document did not persist")
	}
	if err := reloaded.DeleteProject(projectID); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Snapshot().Documents) != 0 {
		t.Fatal("project deletion did not cascade to documents")
	}
}

func TestProjectArchiveAndDeleteCascade(t *testing.T) {
	store, backend := newMemoryStore(t)
	initial := store.Snapshot()
	project := initial.Projects[0]
	archived, err := store.UpdateProject(project.ID, map[string]any{"status": "archived"})
	if err != nil || archived.Status != "archived" {
		t.Fatalf("archive project: project=%+v err=%v", archived, err)
	}
	if err := store.DeleteProject(project.ID); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	state := store.Snapshot()
	for _, candidate := range state.Projects {
		if candidate.ID == project.ID {
			t.Fatalf("deleted project remains: %+v", candidate)
		}
	}
	deletedTaskIDs := make(map[string]struct{})
	for _, task := range initial.Tasks {
		if task.ProjectID == project.ID {
			deletedTaskIDs[task.ID] = struct{}{}
		}
	}
	for _, task := range state.Tasks {
		if task.ProjectID == project.ID {
			t.Fatalf("deleted project's task remains: %+v", task)
		}
	}
	for _, entry := range state.TimeEntries {
		if _, deleted := deletedTaskIDs[entry.TaskID]; deleted {
			t.Fatalf("deleted project's time entry remains: %+v", entry)
		}
	}
	reloaded, err := newStore(backend, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Snapshot().Projects) != len(initial.Projects)-1 {
		t.Fatal("project deletion did not persist")
	}
}

func TestAuthenticatedAPIWorkflow(t *testing.T) {
	server, authRepository := newTestServer(t)

	unauthorized := request(server, http.MethodGet, "/api/state", "", nil, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected state endpoint: status=%d", unauthorized.Code)
	}

	badLogin := request(server, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"wrong-password"}`, nil, "")
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status=%d body=%s", badLogin.Code, badLogin.Body.String())
	}

	login := request(server, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"`+testPassword+`"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe session cookie: %+v", cookies)
	}
	cookie := cookies[0]
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil || session.CSRFToken == "" {
		t.Fatalf("missing CSRF token: %v %s", err, login.Body.String())
	}

	withoutCSRF := request(server, http.MethodPost, "/api/tasks", `{"projectId":"prj_launch","title":"Denied task","status":"todo"}`, cookie, "")
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("mutation accepted without CSRF: status=%d", withoutCSRF.Code)
	}

	created := request(server, http.MethodPost, "/api/tasks", `{"projectId":"prj_launch","title":"API task","status":"todo"}`, cookie, session.CSRFToken)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	state := request(server, http.MethodGet, "/api/state", "", cookie, "")
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), "API task") || strings.Contains(state.Body.String(), "Denied task") {
		t.Fatalf("unexpected state: %s", state.Body.String())
	}

	createdDocument := request(server, http.MethodPost, "/api/documents", `{"projectId":"prj_launch","title":"API brief","content":"# Draft"}`, cookie, session.CSRFToken)
	if createdDocument.Code != http.StatusCreated {
		t.Fatalf("create document status=%d body=%s", createdDocument.Code, createdDocument.Body.String())
	}
	var document Document
	if err := json.Unmarshal(createdDocument.Body.Bytes(), &document); err != nil || document.ID == "" {
		t.Fatalf("invalid created document: %v %s", err, createdDocument.Body.String())
	}
	updatedDocument := request(server, http.MethodPatch, "/api/documents/"+document.ID, `{"title":"API launch brief","content":"# Ready"}`, cookie, session.CSRFToken)
	if updatedDocument.Code != http.StatusOK || !strings.Contains(updatedDocument.Body.String(), "# Ready") {
		t.Fatalf("update document status=%d body=%s", updatedDocument.Code, updatedDocument.Body.String())
	}
	deletedDocument := request(server, http.MethodDelete, "/api/documents/"+document.ID, "", cookie, session.CSRFToken)
	if deletedDocument.Code != http.StatusOK {
		t.Fatalf("delete document status=%d body=%s", deletedDocument.Code, deletedDocument.Body.String())
	}

	archived := request(server, http.MethodPatch, "/api/projects/prj_mobile", `{"status":"archived"}`, cookie, session.CSRFToken)
	if archived.Code != http.StatusOK || !strings.Contains(archived.Body.String(), `"status":"archived"`) {
		t.Fatalf("archive status=%d body=%s", archived.Code, archived.Body.String())
	}
	deleted := request(server, http.MethodDelete, "/api/projects/prj_mobile", "", cookie, session.CSRFToken)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	afterDelete := request(server, http.MethodGet, "/api/state", "", cookie, "")
	if strings.Contains(afterDelete.Body.String(), "prj_mobile") || strings.Contains(afterDelete.Body.String(), "tsk_nav") {
		t.Fatalf("project cascade failed: %s", afterDelete.Body.String())
	}

	logout := request(server, http.MethodPost, "/api/auth/logout", `{}`, cookie, session.CSRFToken)
	if logout.Code != http.StatusOK || len(logout.Result().Cookies()) != 1 || logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout did not clear cookie: status=%d cookies=%+v", logout.Code, logout.Result().Cookies())
	}

	persisted := authRepository.snapshot()
	if persisted.Revision != 1 || persisted.Credentials.Password.Algorithm != passwordAlgorithmArgon2id {
		t.Fatalf("auth was not persisted securely: revision=%d algorithm=%q", persisted.Revision, persisted.Credentials.Password.Algorithm)
	}
}

func TestSecureCookieAndHSTS(t *testing.T) {
	store, _ := newMemoryStore(t)
	auth := newMemoryAuth(t, &memoryAuthRepository{}, "admin", testPassword, true)
	server := NewServer(store, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)), auth)
	health := request(server, http.MethodGet, "/api/health", "", nil, "")
	if health.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("secure mode did not emit HSTS")
	}
	login := request(server, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"`+testPassword+`"}`, nil, "")
	cookies := login.Result().Cookies()
	if login.Code != http.StatusOK || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("secure cookie missing: status=%d cookies=%+v", login.Code, cookies)
	}
}

func TestSPAHandlerServesWebManifestContentType(t *testing.T) {
	static := fstest.MapFS{
		"index.html":           {Data: []byte("<main>Tempo</main>")},
		"manifest.webmanifest": {Data: []byte(`{"name":"Tempo"}`)},
	}
	response := request(spaHandler(static), http.MethodGet, "/manifest.webmanifest", "", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("manifest status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/manifest+json" {
		t.Fatalf("manifest content type=%q", got)
	}
}

func TestLoginRateLimitAndOriginProtection(t *testing.T) {
	server, _ := newTestServer(t)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		response := request(server, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"incorrect-value"}`, nil, "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d", attempt, response.Code)
		}
	}
	limited := request(server, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"incorrect-value"}`, nil, "")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit status=%d headers=%v", limited.Code, limited.Header())
	}

	originReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"`+testPassword+`"}`))
	originReq.Header.Set("Content-Type", "application/json")
	originReq.Header.Set("Origin", "https://attacker.example")
	originRes := httptest.NewRecorder()
	server.ServeHTTP(originRes, originReq)
	if originRes.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login accepted: %d", originRes.Code)
	}
}

func newTestServer(t *testing.T) (http.Handler, *memoryAuthRepository) {
	t.Helper()
	store, _ := newMemoryStore(t)
	repository := &memoryAuthRepository{}
	auth := newMemoryAuth(t, repository, "admin", testPassword, false)
	return NewServer(store, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)), auth), repository
}

func request(handler http.Handler, method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}
