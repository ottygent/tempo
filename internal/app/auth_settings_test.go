package app

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	server, repository := newTestServer(t)
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

	if response := request(server, http.MethodGet, "/api/state", "", oldCookie, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("old session remained valid after credential change: status=%d", response.Code)
	}
	if response := request(server, http.MethodGet, "/api/state", "", newCookie, ""); response.Code != http.StatusOK {
		t.Fatalf("fresh session rejected: status=%d body=%s", response.Code, response.Body.String())
	}
	oldCredentials := request(server, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"`+testPassword+`"}`, nil, "")
	if oldCredentials.Code != http.StatusUnauthorized {
		t.Fatalf("old credentials remained valid: status=%d", oldCredentials.Code)
	}
	newCredentials := loginForSettings(t, server, "tempo-admin", updatedTestPassword)
	if login := decodeAuthResponse(t, newCredentials); login.Email != "admin@example.com" {
		t.Fatalf("login omitted persisted email: %+v", login)
	}

	reloaded, err := newAuth(repository, "", "ignored", "", false)
	if err != nil {
		t.Fatalf("reload Mongo auth: %v", err)
	}
	if !reloaded.CheckCredentials("tempo-admin", updatedTestPassword) || reloaded.CheckCredentials("admin", testPassword) {
		t.Fatal("updated credentials did not survive Mongo auth reload")
	}
	persisted := repository.snapshot()
	if persisted.Credentials.Username != "tempo-admin" || persisted.Credentials.Email != "admin@example.com" || persisted.Credentials.Password.Algorithm != passwordAlgorithmArgon2id {
		t.Fatalf("settings not persisted securely: username=%q email=%q algorithm=%q", persisted.Credentials.Username, persisted.Credentials.Email, persisted.Credentials.Password.Algorithm)
	}
}

func TestAuthSettingsRequiresSessionCSRFAndValidInput(t *testing.T) {
	server, _ := newTestServer(t)
	login := loginForSettings(t, server, "admin", testPassword)
	cookie := responseCookie(t, login)
	session := decodeAuthResponse(t, login)

	unauthenticated := request(server, http.MethodPatch, "/api/auth/settings", `{"currentPassword":"`+testPassword+`"}`, nil, "")
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("settings accepted unauthenticated request: status=%d", unauthenticated.Code)
	}
	withoutCSRF := request(server, http.MethodPatch, "/api/auth/settings", `{"currentPassword":"`+testPassword+`"}`, cookie, "")
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
	_, err := newAuth(&memoryAuthRepository{}, "", "admin", strings.Repeat("🙂", 11), false)
	if err == nil || !strings.Contains(err.Error(), "at least 12 characters") {
		t.Fatalf("short Unicode bootstrap password accepted: %v", err)
	}
}

func TestAuthSettingsWrongPasswordDoesNotChangeState(t *testing.T) {
	server, repository := newTestServer(t)
	login := loginForSettings(t, server, "admin", testPassword)
	cookie := responseCookie(t, login)
	session := decodeAuthResponse(t, login)
	before := repository.snapshot()

	response := request(server, http.MethodPatch, "/api/auth/settings", `{"username":"intruder","email":"intruder@example.com","currentPassword":"wrong-password","newPassword":"`+updatedTestPassword+`"}`, cookie, session.CSRFToken)
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong current password status=%d body=%s", response.Code, response.Body.String())
	}
	after := repository.snapshot()
	if after.Revision != before.Revision || !sameAuthCredentials(after.Credentials, before.Credentials) {
		t.Fatal("wrong current password changed Mongo auth")
	}
	if request(server, http.MethodGet, "/api/state", "", cookie, "").Code != http.StatusOK {
		t.Fatal("wrong current password invalidated the existing session")
	}
	if loginForSettings(t, server, "admin", testPassword).Code != http.StatusOK {
		t.Fatal("wrong current password changed in-memory credentials")
	}
}

func TestAuthSettingsPersistenceFailureLeavesMemoryUnchanged(t *testing.T) {
	store, _ := newMemoryStore(t)
	repository := &memoryAuthRepository{}
	auth := newMemoryAuth(t, repository, "admin", testPassword, false)
	server := NewServer(store, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)), auth)
	login := loginForSettings(t, server, "admin", testPassword)
	cookie := responseCookie(t, login)
	session := decodeAuthResponse(t, login)
	before := repository.snapshot()
	repository.updateErr = errors.New("injected persistence failure")

	response := request(server, http.MethodPatch, "/api/auth/settings", `{"username":"should-not-stick","currentPassword":"`+testPassword+`","newPassword":"`+updatedTestPassword+`"}`, cookie, session.CSRFToken)
	if response.Code != http.StatusInternalServerError || response.Body.String() != "{\"error\":\"could not update account settings\"}\n" {
		t.Fatalf("persistence failure response=%d body=%s", response.Code, response.Body.String())
	}
	after := repository.snapshot()
	if after.Revision != before.Revision || !sameAuthCredentials(after.Credentials, before.Credentials) {
		t.Fatal("failed persistence changed Mongo auth")
	}
	if !auth.CheckCredentials("admin", testPassword) || auth.CheckCredentials("should-not-stick", updatedTestPassword) {
		t.Fatal("failed persistence changed in-memory credentials")
	}
	if request(server, http.MethodGet, "/api/state", "", cookie, "").Code != http.StatusOK {
		t.Fatal("failed persistence invalidated the existing session")
	}
}

func TestEmailOnlyUpdateKeepsExistingSessionsValid(t *testing.T) {
	server, repository := newTestServer(t)
	login := loginForSettings(t, server, "admin", testPassword)
	cookie := responseCookie(t, login)
	session := decodeAuthResponse(t, login)
	before := repository.snapshot()

	response := request(server, http.MethodPatch, "/api/auth/settings", `{"email":"owner@example.com","currentPassword":"`+testPassword+`"}`, cookie, session.CSRFToken)
	if response.Code != http.StatusOK {
		t.Fatalf("email update status=%d body=%s", response.Code, response.Body.String())
	}
	after := repository.snapshot()
	if !bytes.Equal(before.Credentials.SessionSecret, after.Credentials.SessionSecret) {
		t.Fatal("email-only update rotated the session secret")
	}
	if request(server, http.MethodGet, "/api/state", "", cookie, "").Code != http.StatusOK {
		t.Fatal("email-only update invalidated an existing session")
	}
}

func TestLegacyAuthImportsOnceAndRotatesSessionSecret(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "auth.json")
	salt := bytes.Repeat([]byte{0x23}, 16)
	legacySecret := bytes.Repeat([]byte{0x45}, 32)
	legacy := legacyAuthFile{
		Username:        "admin",
		Salt:            hex.EncodeToString(salt),
		PasswordHash:    hex.EncodeToString(derivePBKDF2(testPassword, salt, pbkdfRounds)),
		SessionSecret:   hex.EncodeToString(legacySecret),
		PBKDFIterations: pbkdfRounds,
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &memoryAuthRepository{}
	auth, err := newAuth(repository, legacyPath, "ignored", "", false)
	if err != nil {
		t.Fatalf("import legacy auth: %v", err)
	}
	if !auth.CheckCredentials("admin", testPassword) {
		t.Fatal("legacy password verifier was not preserved")
	}
	persisted := repository.snapshot()
	if persisted.Credentials.Email != "" || persisted.Credentials.Password.Algorithm != passwordAlgorithmPBKDF2 {
		t.Fatalf("legacy auth was not represented correctly: email=%q algorithm=%q", persisted.Credentials.Email, persisted.Credentials.Password.Algorithm)
	}
	if bytes.Equal(persisted.Credentials.SessionSecret, legacySecret) {
		t.Fatal("legacy session secret was not rotated during migration")
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy source was removed before functional verification: %v", err)
	}
}

func TestExistingMongoAuthNeverReadsLegacyJSON(t *testing.T) {
	repository := &memoryAuthRepository{}
	original := newMemoryAuth(t, repository, "admin", testPassword, false)
	legacyPath := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(legacyPath, []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newAuth(repository, legacyPath, "ignored", updatedTestPassword, false)
	if err != nil {
		t.Fatalf("existing Mongo auth consulted stale legacy JSON: %v", err)
	}
	if !original.CheckCredentials("admin", testPassword) || !reloaded.CheckCredentials("admin", testPassword) {
		t.Fatal("existing Mongo credentials did not win")
	}
}

func TestConfiguredMissingLegacyAuthFailsClosed(t *testing.T) {
	repository := &memoryAuthRepository{}
	legacyPath := filepath.Join(t.TempDir(), "missing-auth.json")
	_, err := newAuth(repository, legacyPath, "admin", testPassword, false)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing explicit legacy auth source did not fail closed: %v", err)
	}
	if repository.snapshot().Revision != 0 {
		t.Fatal("missing legacy auth source was replaced with bootstrap credentials")
	}
}

func TestMalformedMongoAuthFailsClosed(t *testing.T) {
	repository := &memoryAuthRepository{record: &authRecord{
		Revision: 1,
		Credentials: authCredentials{
			Username:      "admin",
			Password:      passwordDigest{Algorithm: "unknown", Salt: make([]byte, 16), Hash: make([]byte, passwordHashLength)},
			SessionSecret: make([]byte, 32),
		},
	}}
	_, err := newAuth(repository, "", "admin", updatedTestPassword, false)
	if err == nil || !strings.Contains(err.Error(), "invalid MongoDB auth") {
		t.Fatalf("malformed Mongo auth did not fail closed: %v", err)
	}
	if repository.snapshot().Credentials.Password.Algorithm != "unknown" {
		t.Fatal("malformed Mongo auth was overwritten by bootstrap credentials")
	}
}

func loginForSettings(t *testing.T, server http.Handler, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	response := request(server, http.MethodPost, "/api/auth/login", `{"username":`+quotedJSON(username)+`,"password":`+quotedJSON(password)+`}`, nil, "")
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
