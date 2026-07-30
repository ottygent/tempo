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

const updatedTestPassword = "updated-horse-battery-staple"

type authTestResponse struct {
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username"`
	Email         string `json:"email"`
	CSRFToken     string `json:"csrfToken"`
	Expires       int64  `json:"expires"`
}

func TestAuthSettingsUpdatePersistsAndRotatesSession(t *testing.T) {
	server, authPath := newTestServer(t)
	initial := loginForSettings(t, server, "admin", testPassword)
	oldCookie := responseCookie(t, initial)
	oldSession := decodeAuthResponse(t, initial)

	update := request(
		server,
		http.MethodPatch,
		"/api/auth/settings",
		`{"username":"  tempo-admin  ","email":"admin@example.com","currentPassword":"`+testPassword+`","newPassword":"`+updatedTestPassword+`"}`,
		oldCookie,
		oldSession.CSRFToken,
	)
	if update.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", update.Code, update.Body.String())
	}
	updated := decodeAuthResponse(t, update)
	if !updated.Authenticated || updated.Username != "tempo-admin" || updated.Email != "admin@example.com" || updated.Expires == 0 {
		t.Fatalf("unexpected settings response: %+v", updated)
	}
	if updated.CSRFToken == "" || updated.CSRFToken == oldSession.CSRFToken {
		t.Fatalf("settings did not return a fresh CSRF token: old=%q new=%q", oldSession.CSRFToken, updated.CSRFToken)
	}
	newCookie := responseCookie(t, update)
	if newCookie.Value == oldCookie.Value || !newCookie.HttpOnly || newCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("settings did not return a fresh secure session cookie: %+v", newCookie)
	}

	oldSessionRequest := request(server, http.MethodGet, "/api/state", "", oldCookie, "")
	if oldSessionRequest.Code != http.StatusUnauthorized {
		t.Fatalf("old session remained valid after credential change: status=%d", oldSessionRequest.Code)
	}
	newSessionRequest := request(server, http.MethodGet, "/api/state", "", newCookie, "")
	if newSessionRequest.Code != http.StatusOK {
		t.Fatalf("fresh session rejected: status=%d body=%s", newSessionRequest.Code, newSessionRequest.Body.String())
	}
	csrfProof := request(
		server,
		http.MethodPatch,
		"/api/auth/settings",
		`{"currentPassword":"`+updatedTestPassword+`","newPassword":""}`,
		newCookie,
		updated.CSRFToken,
	)
	if csrfProof.Code != http.StatusOK {
		t.Fatalf("fresh CSRF token rejected: status=%d body=%s", csrfProof.Code, csrfProof.Body.String())
	}

	oldCredentials := request(server, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"`+testPassword+`"}`, nil, "")
	if oldCredentials.Code != http.StatusUnauthorized {
		t.Fatalf("old credentials remained valid: status=%d", oldCredentials.Code)
	}
	newCredentials := loginForSettings(t, server, "tempo-admin", updatedTestPassword)
	newLogin := decodeAuthResponse(t, newCredentials)
	if newLogin.Email != "admin@example.com" {
		t.Fatalf("login omitted persisted email: %+v", newLogin)
	}

	reloaded, err := NewAuth(authPath, "ignored", "", false)
	if err != nil {
		t.Fatalf("reload auth: %v", err)
	}
	if !reloaded.CheckCredentials("tempo-admin", updatedTestPassword) {
		t.Fatal("updated credentials did not survive NewAuth reload")
	}
	if reloaded.CheckCredentials("admin", testPassword) {
		t.Fatal("reloaded auth accepted old credentials")
	}
	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("stat auth file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("updated auth file permissions=%v", info.Mode().Perm())
	}
	var persisted authFile
	body, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if err := json.Unmarshal(body, &persisted); err != nil {
		t.Fatalf("parse auth file: %v", err)
	}
	if persisted.Username != "tempo-admin" || persisted.Email != "admin@example.com" {
		t.Fatalf("settings not persisted: %+v", persisted)
	}
}

func TestAuthSettingsRequiresSessionCSRFAndValidInput(t *testing.T) {
	server, _ := newTestServer(t)
	login := loginForSettings(t, server, "admin", testPassword)
	cookie := responseCookie(t, login)
	session := decodeAuthResponse(t, login)

	unauthenticated := request(
		server,
		http.MethodPatch,
		"/api/auth/settings",
		`{"currentPassword":"`+testPassword+`"}`,
		nil,
		"",
	)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("settings accepted unauthenticated request: status=%d", unauthenticated.Code)
	}
	withoutCSRF := request(
		server,
		http.MethodPatch,
		"/api/auth/settings",
		`{"currentPassword":"`+testPassword+`"}`,
		cookie,
		"",
	)
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("settings accepted request without CSRF: status=%d", withoutCSRF.Code)
	}

	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "malformed JSON", body: `{`, want: http.StatusBadRequest},
		{name: "trailing JSON", body: `{"currentPassword":"` + testPassword + `"} {}`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"currentPassword":"` + testPassword + `","unexpected":true}`, want: http.StatusBadRequest},
		{name: "missing current password", body: `{"email":"admin@example.com"}`, want: http.StatusUnprocessableEntity},
		{name: "empty current password", body: `{"currentPassword":""}`, want: http.StatusUnprocessableEntity},
		{name: "empty username", body: `{"username":"  ","currentPassword":"` + testPassword + `"}`, want: http.StatusUnprocessableEntity},
		{name: "long username", body: `{"username":"` + strings.Repeat("a", maxUsernameLength+1) + `","currentPassword":"` + testPassword + `"}`, want: http.StatusUnprocessableEntity},
		{name: "invalid email", body: `{"email":"not-an-email","currentPassword":"` + testPassword + `"}`, want: http.StatusUnprocessableEntity},
		{name: "long email", body: `{"email":"` + strings.Repeat("a", maxEmailLength+1) + `","currentPassword":"` + testPassword + `"}`, want: http.StatusUnprocessableEntity},
		{name: "short new password", body: `{"currentPassword":"` + testPassword + `","newPassword":"too-short"}`, want: http.StatusUnprocessableEntity},
		{name: "short Unicode password", body: `{"currentPassword":"` + testPassword + `","newPassword":` + quotedJSON(strings.Repeat("🙂", 11)) + `}`, want: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := request(server, http.MethodPatch, "/api/auth/settings", test.body, cookie, session.CSRFToken)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestNewAuthCountsPasswordCharacters(t *testing.T) {
	_, err := NewAuth(filepath.Join(t.TempDir(), "auth.json"), "admin", strings.Repeat("🙂", 11), false)
	if err == nil || !strings.Contains(err.Error(), "at least 12 characters") {
		t.Fatalf("short Unicode bootstrap password accepted: %v", err)
	}
}

func TestAuthSettingsWrongPasswordDoesNotChangeState(t *testing.T) {
	server, authPath := newTestServer(t)
	login := loginForSettings(t, server, "admin", testPassword)
	cookie := responseCookie(t, login)
	session := decodeAuthResponse(t, login)
	before, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}

	response := request(
		server,
		http.MethodPatch,
		"/api/auth/settings",
		`{"username":"intruder","email":"intruder@example.com","currentPassword":"wrong-password","newPassword":"`+updatedTestPassword+`"}`,
		cookie,
		session.CSRFToken,
	)
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong current password status=%d body=%s", response.Code, response.Body.String())
	}
	after, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("wrong current password changed the auth file")
	}
	if request(server, http.MethodGet, "/api/state", "", cookie, "").Code != http.StatusOK {
		t.Fatal("wrong current password invalidated the existing session")
	}
	if loginForSettings(t, server, "admin", testPassword).Code != http.StatusOK {
		t.Fatal("wrong current password changed in-memory credentials")
	}
}

func TestAuthSettingsPersistenceFailureLeavesMemoryAndFileUnchanged(t *testing.T) {
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
	server := NewServer(store, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)), auth)
	login := loginForSettings(t, server, "admin", testPassword)
	cookie := responseCookie(t, login)
	session := decodeAuthResponse(t, login)
	before, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}

	auth.path = filepath.Join(authPath, "cannot-create")
	response := request(
		server,
		http.MethodPatch,
		"/api/auth/settings",
		`{"username":"should-not-stick","currentPassword":"`+testPassword+`","newPassword":"`+updatedTestPassword+`"}`,
		cookie,
		session.CSRFToken,
	)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("persistence failure status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Body.String() != "{\"error\":\"could not update account settings\"}\n" {
		t.Fatalf("persistence failure leaked internal details: %s", response.Body.String())
	}
	auth.path = authPath

	after, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed persistence changed the auth file")
	}
	if !auth.CheckCredentials("admin", testPassword) || auth.CheckCredentials("should-not-stick", updatedTestPassword) {
		t.Fatal("failed persistence changed in-memory credentials")
	}
	if request(server, http.MethodGet, "/api/state", "", cookie, "").Code != http.StatusOK {
		t.Fatal("failed persistence invalidated the existing session")
	}
}

func TestNewAuthLoadsLegacyFileWithoutEmail(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if _, err := NewAuth(authPath, "admin", testPassword, false); err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), `"email"`) {
		t.Fatalf("test fixture unexpectedly contains email: %s", legacy)
	}

	auth, err := NewAuth(authPath, "ignored", "", false)
	if err != nil {
		t.Fatalf("legacy auth file did not load: %v", err)
	}
	store, err := NewStore(filepath.Join(dir, "tempo.json"))
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)), auth)
	login := loginForSettings(t, server, "admin", testPassword)
	response := decodeAuthResponse(t, login)
	if response.Email != "" {
		t.Fatalf("legacy auth file returned unexpected email: %+v", response)
	}
	session := request(server, http.MethodGet, "/api/auth/session", "", responseCookie(t, login), "")
	if session.Code != http.StatusOK || decodeAuthResponse(t, session).Email != "" {
		t.Fatalf("legacy session response is incompatible: status=%d body=%s", session.Code, session.Body.String())
	}
}

func loginForSettings(t *testing.T, server http.Handler, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	response := request(
		server,
		http.MethodPost,
		"/api/auth/login",
		`{"username":`+quotedJSON(username)+`,"password":`+quotedJSON(password)+`}`,
		nil,
		"",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	return response
}

func quotedJSON(value string) string {
	body, _ := json.Marshal(value)
	return string(body)
}

func responseCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count=%d cookies=%+v", len(cookies), cookies)
	}
	return cookies[0]
}

func decodeAuthResponse(t *testing.T, response *httptest.ResponseRecorder) authTestResponse {
	t.Helper()
	var value authTestResponse
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode auth response: %v body=%s", err, response.Body.String())
	}
	return value
}
