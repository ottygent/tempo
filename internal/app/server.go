package app

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Server struct {
	store  *Store
	static fs.FS
	logger *slog.Logger
	auth   *Auth
}

func NewServer(store *Store, static fs.FS, logger *slog.Logger, auth *Auth) http.Handler {
	s := &Server{store: store, static: static, logger: logger, auth: auth}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/auth/session", s.authSession)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("POST /api/workspaces", s.createWorkspace)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.updateTask)
	mux.HandleFunc("POST /api/time/start", s.startTimer)
	mux.HandleFunc("POST /api/time/stop", s.stopTimer)
	if static != nil {
		mux.Handle("/", spaHandler(static))
	}
	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if s.auth.secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if isUnsafe(r.Method) && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, errors.New("cross-origin request rejected"))
			return
		}
		if protectedAPI(r.URL.Path) {
			session, ok := s.auth.Session(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
				return
			}
			if isUnsafe(r.Method) && !s.auth.VerifyCSRF(session, r.Header.Get("X-CSRF-Token")) {
				writeError(w, http.StatusForbidden, errors.New("invalid CSRF token"))
				return
			}
		}
		next.ServeHTTP(w, r)
		s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start).String())
	})
}

func protectedAPI(path string) bool {
	return strings.HasPrefix(path, "/api/") && path != "/api/health" && path != "/api/auth/session" && path != "/api/auth/login"
}
func isUnsafe(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host == r.Host
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func readJSON(r *http.Request, value any) error {
	defer r.Body.Close()
	d := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	d.DisallowUnknownFields()
	return d.Decode(value)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
func (s *Server) authSession(w http.ResponseWriter, r *http.Request) {
	session, ok := s.auth.Session(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": session.Username, "csrfToken": s.auth.csrf(session), "expires": session.Expires})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.auth.LoginAllowed(ip) {
		w.Header().Set("Retry-After", "900")
		writeError(w, http.StatusTooManyRequests, errors.New("too many login attempts; try again later"))
		return
	}
	var credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &credentials); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid login request"))
		return
	}
	if !s.auth.CheckCredentials(credentials.Username, credentials.Password) {
		s.auth.RecordFailure(ip)
		writeError(w, http.StatusUnauthorized, errors.New("invalid username or password"))
		return
	}
	s.auth.ClearFailures(ip)
	session, csrf, err := s.auth.Issue(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not create session"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": session.Username, "csrfToken": csrf, "expires": session.Expires})
}
func (s *Server) logout(w http.ResponseWriter, _ *http.Request) {
	s.auth.Clear(w)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}
func (s *Server) state(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot())
}
func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var value Workspace
	if err := readJSON(r, &value); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.store.AddWorkspace(value)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var value Project
	if err := readJSON(r, &value); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.store.AddProject(value)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var value Task
	if err := readJSON(r, &value); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.store.AddTask(value)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	var patch map[string]any
	if err := readJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.store.UpdateTask(r.PathValue("id"), patch)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) startTimer(w http.ResponseWriter, r *http.Request) {
	var value struct {
		TaskID string `json:"taskId"`
	}
	if err := readJSON(r, &value); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.store.StartTimer(value.TaskID)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
func (s *Server) stopTimer(w http.ResponseWriter, _ *http.Request) {
	out, err := s.store.StopTimer()
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func spaHandler(static fs.FS) http.Handler {
	files := http.FileServer(http.FS(static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(static, path); err != nil {
			clone := r.Clone(r.Context())
			clone.URL.Path = "/index.html"
			files.ServeHTTP(w, clone)
			return
		}
		files.ServeHTTP(w, r)
	})
}
