package app

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPassword = "correct-horse-battery-staple"

func TestStorePersistsTaskAndTimer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tempo.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
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
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Snapshot()
	if len(got.Tasks) != len(state.Tasks)+1 || len(got.TimeEntries) != len(state.TimeEntries)+1 {
		t.Fatalf("state did not persist: %+v", got)
	}
}

func TestAuthenticatedAPIWorkflow(t *testing.T) {
	server, authPath := newTestServer(t)

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

	logout := request(server, http.MethodPost, "/api/auth/logout", `{}`, cookie, session.CSRFToken)
	if logout.Code != http.StatusOK || len(logout.Result().Cookies()) != 1 || logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout did not clear cookie: status=%d cookies=%+v", logout.Code, logout.Result().Cookies())
	}

	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("stat auth file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth file permissions=%v", info.Mode().Perm())
	}
}

func TestSecureCookieAndHSTS(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "tempo.json"))
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth(filepath.Join(dir, "auth.json"), "admin", testPassword, true)
	if err != nil {
		t.Fatal(err)
	}
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

func newTestServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "tempo.json"))
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(dir, "auth.json")
	auth, err := NewAuth(authPath, "admin", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(store, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)), auth), authPath
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
