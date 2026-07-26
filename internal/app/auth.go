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
	username string
	salt     []byte
	hash     []byte
	secret   []byte
	rounds   int
	secure   bool
	mu       sync.Mutex
	attempts map[string]loginAttempts
}

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
		if len(password) < 12 {
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
	return &Auth{username: config.Username, salt: salt, hash: hash, secret: secret, rounds: config.PBKDFIterations, secure: secure, attempts: make(map[string]loginAttempts)}, nil
}

func saveAuthFile(path string, config authFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}
	body, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("write auth file: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	session := Session{Username: a.username, Expires: time.Now().Add(sessionTTL).Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes)}
	body, err := json.Marshal(session)
	if err != nil {
		return Session{}, "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	value := payload + "." + a.sign(payload)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/", MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode})
	return session, a.csrf(session), nil
}

func (a *Auth) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode})
}

func (a *Auth) Session(r *http.Request) (Session, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(a.sign(parts[0]))) != 1 {
		return Session{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Session{}, false
	}
	var session Session
	if json.Unmarshal(body, &session) != nil || session.Username != a.username || session.Expires <= time.Now().Unix() || session.Nonce == "" {
		return Session{}, false
	}
	return session, true
}

func (a *Auth) VerifyCSRF(session Session, token string) bool {
	expected := a.csrf(session)
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (a *Auth) csrf(session Session) string {
	return a.sign("csrf|" + session.Username + "|" + session.Nonce + "|" + fmt.Sprint(session.Expires))
}

func (a *Auth) sign(value string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (a *Auth) LoginAllowed(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.attempts[ip]
	if !ok || time.Since(entry.Since) > loginWindow {
		delete(a.attempts, ip)
		return true
	}
	return entry.Count < maxAttempts
}

func (a *Auth) RecordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.attempts[ip]
	if entry.Since.IsZero() || time.Since(entry.Since) > loginWindow {
		entry = loginAttempts{Since: time.Now()}
	}
	entry.Count++
	a.attempts[ip] = entry
}

func (a *Auth) ClearFailures(ip string) {
	a.mu.Lock()
	delete(a.attempts, ip)
	a.mu.Unlock()
}
