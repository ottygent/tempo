package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	sessionCookie = "tempo_session"
	sessionTTL    = 8 * time.Hour
	loginWindow   = 15 * time.Minute
	maxAttempts   = 5

	passwordAlgorithmPBKDF2   = "pbkdf2-sha256"
	passwordAlgorithmArgon2id = "argon2id"
	pbkdfRounds               = 210_000
	maxPBKDFRounds            = 2_000_000
	argon2Time                = 3
	argon2Memory              = 64 * 1024
	argon2Threads             = 2
	passwordHashLength        = 32
)

type passwordDigest struct {
	Algorithm       string
	Salt            []byte
	Hash            []byte
	PBKDFIterations int
	Argon2Time      uint32
	Argon2Memory    uint32
	Argon2Threads   uint8
}

type authCredentials struct {
	Username      string
	Email         string
	Password      passwordDigest
	SessionSecret []byte
}

type authRecord struct {
	Credentials authCredentials
	Revision    int64
}

type authRepository interface {
	LoadAuth() (authRecord, error)
	InitializeAuth(authCredentials) (authRecord, error)
	UpdateAuth(expectedRevision int64, next authCredentials) (authRecord, error)
	Name() string
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
	repository    authRepository
	credentials   authCredentials
	revision      int64
	secure        bool
	credentialsMu sync.RWMutex
	passwordMu    sync.Mutex
	attemptsMu    sync.Mutex
	attempts      map[string]loginAttempts
}

var (
	ErrAuthBootstrapRequired  = errors.New("MongoDB auth bootstrap is required")
	errAuthNotFound           = errors.New("auth record not found")
	errAuthConflict           = errors.New("auth record changed concurrently")
	errInvalidCurrentPassword = errors.New("invalid current password")
)

func NewAuthForStore(store *Store, legacyPath, username, password string, secure bool) (*Auth, error) {
	repository, ok := store.backend.(authRepository)
	if !ok {
		return nil, errors.New("MongoDB auth storage is required")
	}
	return newAuth(repository, legacyPath, username, password, secure)
}

func newAuth(repository authRepository, legacyPath, username, password string, secure bool) (*Auth, error) {
	record, err := repository.LoadAuth()
	if errors.Is(err, errAuthNotFound) {
		credentials, imported, loadErr := credentialsForInitialization(legacyPath, username, password)
		if loadErr != nil {
			return nil, loadErr
		}
		if imported {
			// Rotating the signing key makes any retained legacy JSON copy unable
			// to forge sessions after its password verifier has been migrated.
			credentials.SessionSecret, loadErr = randomBytes(32)
			if loadErr != nil {
				return nil, loadErr
			}
		}
		record, err = repository.InitializeAuth(credentials)
	}
	if err != nil {
		return nil, fmt.Errorf("load MongoDB auth: %w", err)
	}
	if err := validateAuthRecord(record); err != nil {
		return nil, fmt.Errorf("invalid MongoDB auth: %w", err)
	}
	return &Auth{
		repository:  repository,
		credentials: cloneAuthCredentials(record.Credentials),
		revision:    record.Revision,
		secure:      secure,
		attempts:    make(map[string]loginAttempts),
	}, nil
}

func credentialsForInitialization(legacyPath, username, password string) (authCredentials, bool, error) {
	if legacyPath != "" {
		credentials, err := loadLegacyAuthFile(legacyPath)
		if err != nil {
			return authCredentials{}, false, fmt.Errorf("load legacy auth: %w", err)
		}
		if err := validateAuthCredentials(credentials); err != nil {
			return authCredentials{}, false, fmt.Errorf("validate legacy auth: %w", err)
		}
		return credentials, true, nil
	}
	credentials, err := newAuthCredentials(username, password)
	return credentials, false, err
}

func newAuthCredentials(username, password string) (authCredentials, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		username = "admin"
	}
	if utf8.RuneCountInString(password) < 12 {
		return authCredentials{}, fmt.Errorf("%w: TEMPO_ADMIN_PASSWORD must contain at least 12 characters", ErrAuthBootstrapRequired)
	}
	digest, err := newPasswordDigest(password)
	if err != nil {
		return authCredentials{}, err
	}
	secret, err := randomBytes(32)
	if err != nil {
		return authCredentials{}, err
	}
	credentials := authCredentials{Username: username, Password: digest, SessionSecret: secret}
	if err := validateAuthCredentials(credentials); err != nil {
		return authCredentials{}, err
	}
	return credentials, nil
}

func newPasswordDigest(password string) (passwordDigest, error) {
	salt, err := randomBytes(16)
	if err != nil {
		return passwordDigest{}, err
	}
	return passwordDigest{
		Algorithm:     passwordAlgorithmArgon2id,
		Salt:          salt,
		Hash:          argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, passwordHashLength),
		Argon2Time:    argon2Time,
		Argon2Memory:  argon2Memory,
		Argon2Threads: argon2Threads,
	}, nil
}

func validateAuthRecord(record authRecord) error {
	if record.Revision < 1 {
		return errors.New("invalid auth revision")
	}
	return validateAuthCredentials(record.Credentials)
}

func validateAuthCredentials(credentials authCredentials) error {
	if credentials.Username == "" || strings.TrimSpace(credentials.Username) != credentials.Username || !utf8.ValidString(credentials.Username) || utf8.RuneCountInString(credentials.Username) > 100 {
		return errors.New("invalid auth username")
	}
	if !utf8.ValidString(credentials.Email) || utf8.RuneCountInString(credentials.Email) > 254 {
		return errors.New("invalid auth email")
	}
	if len(credentials.Password.Salt) != 16 || len(credentials.Password.Hash) != passwordHashLength {
		return errors.New("invalid password verifier")
	}
	switch credentials.Password.Algorithm {
	case passwordAlgorithmPBKDF2:
		if credentials.Password.PBKDFIterations < 100_000 || credentials.Password.PBKDFIterations > maxPBKDFRounds {
			return errors.New("unsafe PBKDF2 iteration count")
		}
	case passwordAlgorithmArgon2id:
		if credentials.Password.Argon2Time < 1 || credentials.Password.Argon2Time > 10 ||
			credentials.Password.Argon2Memory < 32*1024 || credentials.Password.Argon2Memory > 256*1024 ||
			credentials.Password.Argon2Threads < 1 || credentials.Password.Argon2Threads > 16 {
			return errors.New("unsafe Argon2id parameters")
		}
	default:
		return errors.New("unsupported password algorithm")
	}
	if len(credentials.SessionSecret) < 32 || len(credentials.SessionSecret) > 64 {
		return errors.New("invalid session secret")
	}
	return nil
}

func cloneAuthCredentials(credentials authCredentials) authCredentials {
	credentials.Password.Salt = append([]byte(nil), credentials.Password.Salt...)
	credentials.Password.Hash = append([]byte(nil), credentials.Password.Hash...)
	credentials.SessionSecret = append([]byte(nil), credentials.SessionSecret...)
	return credentials
}

func sameAuthCredentials(left, right authCredentials) bool {
	return left.Username == right.Username && left.Email == right.Email &&
		left.Password.Algorithm == right.Password.Algorithm &&
		left.Password.PBKDFIterations == right.Password.PBKDFIterations &&
		left.Password.Argon2Time == right.Password.Argon2Time &&
		left.Password.Argon2Memory == right.Password.Argon2Memory &&
		left.Password.Argon2Threads == right.Password.Argon2Threads &&
		bytes.Equal(left.Password.Salt, right.Password.Salt) &&
		bytes.Equal(left.Password.Hash, right.Password.Hash) &&
		bytes.Equal(left.SessionSecret, right.SessionSecret)
}

func derivePassword(password string, digest passwordDigest) []byte {
	switch digest.Algorithm {
	case passwordAlgorithmArgon2id:
		return argon2.IDKey([]byte(password), digest.Salt, digest.Argon2Time, digest.Argon2Memory, digest.Argon2Threads, uint32(len(digest.Hash)))
	case passwordAlgorithmPBKDF2:
		return derivePBKDF2(password, digest.Salt, digest.PBKDFIterations)
	default:
		return make([]byte, len(digest.Hash))
	}
}

func derivePBKDF2(password string, salt []byte, rounds int) []byte {
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

func (a *Auth) StorageName() string { return a.repository.Name() }

func (a *Auth) CheckCredentials(username, password string) bool {
	a.credentialsMu.RLock()
	defer a.credentialsMu.RUnlock()
	a.passwordMu.Lock()
	defer a.passwordMu.Unlock()
	return checkCredentials(a.credentials, username, password)
}

func checkCredentials(credentials authCredentials, username, password string) bool {
	candidate := derivePassword(password, credentials.Password)
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(credentials.Username))
	hashOK := subtle.ConstantTimeCompare(candidate, credentials.Password.Hash)
	return userOK&hashOK == 1
}

func (a *Auth) Issue(w http.ResponseWriter) (Session, string, error) {
	nonceBytes, err := randomBytes(18)
	if err != nil {
		return Session{}, "", err
	}
	a.credentialsMu.RLock()
	defer a.credentialsMu.RUnlock()
	session, csrf, cookieValue, err := makeSession(a.credentials.Username, a.credentials.SessionSecret, nonceBytes)
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
	a.passwordMu.Lock()
	defer a.passwordMu.Unlock()
	if !checkCredentials(a.credentials, username, password) {
		return Session{}, "", "", false, nil
	}
	session, csrf, cookieValue, err := makeSession(a.credentials.Username, a.credentials.SessionSecret, nonceBytes)
	if err != nil {
		return Session{}, "", "", false, err
	}
	a.setSessionCookie(w, cookieValue)
	return session, a.credentials.Email, csrf, true, nil
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
	if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(signWithSecret(a.credentials.SessionSecret, parts[0]))) != 1 ||
		session.Username != a.credentials.Username || session.Expires <= time.Now().Unix() || session.Nonce == "" {
		return Session{}, "", "", false
	}
	return session, a.credentials.Email, csrfWithSecret(a.credentials.SessionSecret, session), true
}

func (a *Auth) VerifyCSRF(session Session, token string) bool {
	a.credentialsMu.RLock()
	defer a.credentialsMu.RUnlock()
	expected := csrfWithSecret(a.credentials.SessionSecret, session)
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (a *Auth) csrf(session Session) string {
	a.credentialsMu.RLock()
	defer a.credentialsMu.RUnlock()
	return csrfWithSecret(a.credentials.SessionSecret, session)
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

	a.passwordMu.Lock()
	currentHash := derivePassword(currentPassword, a.credentials.Password)
	a.passwordMu.Unlock()
	if subtle.ConstantTimeCompare(currentHash, a.credentials.Password.Hash) != 1 {
		return Session{}, "", "", errInvalidCurrentPassword
	}

	next := cloneAuthCredentials(a.credentials)
	if username != nil {
		next.Username = *username
	}
	if email != nil {
		next.Email = *email
	}
	passwordChanged := newPassword != nil && *newPassword != ""
	if passwordChanged {
		var err error
		next.Password, err = newPasswordDigest(*newPassword)
		if err != nil {
			return Session{}, "", "", err
		}
	}
	if passwordChanged || next.Username != a.credentials.Username {
		var err error
		next.SessionSecret, err = randomBytes(32)
		if err != nil {
			return Session{}, "", "", err
		}
	}
	if err := validateAuthCredentials(next); err != nil {
		return Session{}, "", "", err
	}
	nonceBytes, err := randomBytes(18)
	if err != nil {
		return Session{}, "", "", err
	}
	session, csrf, cookieValue, err := makeSession(next.Username, next.SessionSecret, nonceBytes)
	if err != nil {
		return Session{}, "", "", err
	}

	persisted, err := a.repository.UpdateAuth(a.revision, next)
	if err != nil {
		return Session{}, "", "", err
	}
	if err := validateAuthRecord(persisted); err != nil || !sameAuthCredentials(persisted.Credentials, next) {
		return Session{}, "", "", errors.New("MongoDB returned an inconsistent auth record")
	}

	a.credentials = cloneAuthCredentials(persisted.Credentials)
	a.revision = persisted.Revision
	a.setSessionCookie(w, cookieValue)
	return session, next.Email, csrf, nil
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
