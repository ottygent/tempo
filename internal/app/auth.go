package app

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	sessionCookie = "tempo_session"
	sessionTTL    = 8 * time.Hour
	loginWindow   = 15 * time.Minute
	maxAttempts   = 5
	pbkdfRounds   = 210_000
)

type authFile struct {
	Username        string `json:"username"`
	Email           string `json:"email,omitempty"`
	Salt            string `json:"salt"`
	PasswordHash    string `json:"passwordHash"`
	SessionSecret   string `json:"sessionSecret"`
	PBKDFIterations int    `json:"pbkdfIterations"`
}

type loginAttempts struct {
	Count int
	Since time.Time
}

type Session struct {
	Username string `json:"username"`
	Expires  int64  `json:"expires"`
	Nonce    string `json:"nonce"`
}

type Auth struct {
	path          string
	username      string
	email         string
	salt          []byte
	hash          []byte
	secret        []byte
	rounds        int
	secure        bool
	credentialsMu sync.RWMutex
	attemptsMu    sync.Mutex
	attempts      map[string]loginAttempts
}

var errInvalidCurrentPassword = errors.New("invalid current password")

func NewAuth(path, username, password string, secure bool) (*Auth, error) {
	var config authFile
	body, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(body, &config); err != nil {
			return nil, fmt.Errorf("parse auth file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read auth file: %w", err)
	} else {
		username = strings.TrimSpace(username)
		if username == "" {
			username = "admin"
		}
		if utf8.RuneCountInString(password) < 12 {
			return nil, errors.New("TEMPO_ADMIN_PASSWORD must contain at least 12 characters on first run")
		}
		salt, err := randomBytes(16)
		if err != nil {
			return nil, err
		}
		secret, err := randomBytes(32)
		if err != nil {
			return nil, err
		}
		config = authFile{Username: username, Salt: hex.EncodeToString(salt), PasswordHash: hex.EncodeToString(derivePassword(password, salt, pbkdfRounds)), SessionSecret: hex.EncodeToString(secret), PBKDFIterations: pbkdfRounds}
		if err := saveAuthFile(path, config); err != nil {
			return nil, err
		}
	}
	if config.PBKDFIterations < 100_000 {
		return nil, errors.New("auth file uses an unsafe password iteration count")
	}
	salt, err := hex.DecodeString(config.Salt)
	if err != nil {
		return nil, errors.New("invalid auth salt")
	}
	hash, err := hex.DecodeString(config.PasswordHash)
	if err != nil {
		return nil, errors.New("invalid password hash")
	}
	secret, err := hex.DecodeString(config.SessionSecret)
	if err != nil || len(secret) < 32 {
		return nil, errors.New("invalid session secret")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure auth file: %w", err)
	}
	return &Auth{
		path:     path,
		username: config.Username,
		email:    config.Email,
		salt:     salt,
		hash:     hash,
		secret:   secret,
		rounds:   config.PBKDFIterations,
		secure:   secure,
		attempts: make(map[string]loginAttempts),
	}, nil
}

func saveAuthFile(path string, config authFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}
	body, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary auth file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure temporary auth file: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write auth file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close auth file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace auth file: %w", err)
	}
	return nil
}

func derivePassword(password string, salt []byte, rounds int) []byte {
	// PBKDF2-HMAC-SHA256 implemented with the standard library to keep Tempo dependency-free.
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write(salt)
	mac.Write([]byte{0, 0, 0, 1})
	u := mac.Sum(nil)
	out := append([]byte(nil), u...)
	for i := 1; i < rounds; i++ {
		mac.Reset()
		mac.Write(u)
		u = mac.Sum(nil)
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return out
}

func randomBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return nil, fmt.Errorf("secure random: %w", err)
	}
	return value, nil
}

func (a *Auth) CheckCredentials(username, password string) bool {
	a.credentialsMu.RLock()
	defer a.credentialsMu.RUnlock()
	return a.checkCredentialsLocked(username, password)
}

func (a *Auth) checkCredentialsLocked(username, password string) bool {
	candidate := derivePassword(password, a.salt, a.rounds)
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(a.username))
	hashOK := subtle.ConstantTimeCompare(candidate, a.hash)
	return userOK&hashOK == 1
}

func (a *Auth) Issue(w http.ResponseWriter) (Session, string, error) {
	nonceBytes, err := randomBytes(18)
	if err != nil {
		return Session{}, "", err
	}
	a.credentialsMu.RLock()
	defer a.credentialsMu.RUnlock()
	session, csrf, cookieValue, err := makeSession(a.username, a.secret, nonceBytes)
	if err != nil {
		return Session{}, "", err
	}
	a.setSessionCookie(w, cookieValue)
	return session, csrf, nil
}

func (a *Auth) AuthenticateAndIssue(w http.ResponseWriter, username, password string) (Session, string, string, bool, error) {
	nonceBytes, err := randomBytes(18)
	if err != nil {
		return Session{}, "", "", false, err
	}
	a.credentialsMu.RLock()
	defer a.credentialsMu.RUnlock()
	if !a.checkCredentialsLocked(username, password) {
		return Session{}, "", "", false, nil
	}
	session, csrf, cookieValue, err := makeSession(a.username, a.secret, nonceBytes)
	if err != nil {
		return Session{}, "", "", false, err
	}
	a.setSessionCookie(w, cookieValue)
	return session, a.email, csrf, true, nil
}

func makeSession(username string, secret, nonceBytes []byte) (Session, string, string, error) {
	session := Session{Username: username, Expires: time.Now().Add(sessionTTL).Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes)}
	body, err := json.Marshal(session)
	if err != nil {
		return Session{}, "", "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	value := payload + "." + signWithSecret(secret, payload)
	return session, csrfWithSecret(secret, session), value, nil
}

func (a *Auth) setSessionCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/", MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode})
}

func (a *Auth) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode})
}

func (a *Auth) Session(r *http.Request) (Session, bool) {
	session, _, _, ok := a.SessionDetails(r)
	return session, ok
}

func (a *Auth) SessionDetails(r *http.Request) (Session, string, string, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, "", "", false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return Session{}, "", "", false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Session{}, "", "", false
	}
	var session Session
	if json.Unmarshal(body, &session) != nil {
		return Session{}, "", "", false
	}
	a.credentialsMu.RLock()
	defer a.credentialsMu.RUnlock()
	if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(signWithSecret(a.secret, parts[0]))) != 1 ||
		session.Username != a.username ||
		session.Expires <= time.Now().Unix() ||
		session.Nonce == "" {
		return Session{}, "", "", false
	}
	return session, a.email, csrfWithSecret(a.secret, session), true
}

func (a *Auth) VerifyCSRF(session Session, token string) bool {
	a.credentialsMu.RLock()
	defer a.credentialsMu.RUnlock()
	expected := csrfWithSecret(a.secret, session)
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (a *Auth) csrf(session Session) string {
	a.credentialsMu.RLock()
	defer a.credentialsMu.RUnlock()
	return csrfWithSecret(a.secret, session)
}

func csrfWithSecret(secret []byte, session Session) string {
	return signWithSecret(secret, "csrf|"+session.Username+"|"+session.Nonce+"|"+fmt.Sprint(session.Expires))
}

func signWithSecret(secret []byte, value string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Auth) UpdateSettingsAndIssue(w http.ResponseWriter, username, email *string, currentPassword string, newPassword *string) (Session, string, string, error) {
	a.credentialsMu.Lock()
	defer a.credentialsMu.Unlock()

	currentHash := derivePassword(currentPassword, a.salt, a.rounds)
	if subtle.ConstantTimeCompare(currentHash, a.hash) != 1 {
		return Session{}, "", "", errInvalidCurrentPassword
	}

	nextUsername := a.username
	if username != nil {
		nextUsername = *username
	}
	nextEmail := a.email
	if email != nil {
		nextEmail = *email
	}
	nextSalt := append([]byte(nil), a.salt...)
	nextHash := append([]byte(nil), a.hash...)
	nextSecret := append([]byte(nil), a.secret...)

	passwordChanged := newPassword != nil && *newPassword != ""
	if passwordChanged {
		var err error
		nextSalt, err = randomBytes(16)
		if err != nil {
			return Session{}, "", "", err
		}
		nextHash = derivePassword(*newPassword, nextSalt, a.rounds)
	}
	if passwordChanged || nextUsername != a.username {
		var err error
		nextSecret, err = randomBytes(32)
		if err != nil {
			return Session{}, "", "", err
		}
	}
	nonceBytes, err := randomBytes(18)
	if err != nil {
		return Session{}, "", "", err
	}
	session, csrf, cookieValue, err := makeSession(nextUsername, nextSecret, nonceBytes)
	if err != nil {
		return Session{}, "", "", err
	}

	config := authFile{
		Username:        nextUsername,
		Email:           nextEmail,
		Salt:            hex.EncodeToString(nextSalt),
		PasswordHash:    hex.EncodeToString(nextHash),
		SessionSecret:   hex.EncodeToString(nextSecret),
		PBKDFIterations: a.rounds,
	}
	if err := saveAuthFile(a.path, config); err != nil {
		return Session{}, "", "", err
	}

	a.username = nextUsername
	a.email = nextEmail
	a.salt = nextSalt
	a.hash = nextHash
	a.secret = nextSecret
	a.setSessionCookie(w, cookieValue)
	return session, nextEmail, csrf, nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (a *Auth) LoginAllowed(ip string) bool {
	a.attemptsMu.Lock()
	defer a.attemptsMu.Unlock()
	entry, ok := a.attempts[ip]
	if !ok || time.Since(entry.Since) > loginWindow {
		delete(a.attempts, ip)
		return true
	}
	return entry.Count < maxAttempts
}

func (a *Auth) RecordFailure(ip string) {
	a.attemptsMu.Lock()
	defer a.attemptsMu.Unlock()
	entry := a.attempts[ip]
	if entry.Since.IsZero() || time.Since(entry.Since) > loginWindow {
		entry = loginAttempts{Since: time.Now()}
	}
	entry.Count++
	a.attempts[ip] = entry
}

func (a *Auth) ClearFailures(ip string) {
	a.attemptsMu.Lock()
	delete(a.attempts, ip)
	a.attemptsMu.Unlock()
}
