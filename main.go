package main

import (
	"embed"
	"flag"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"tempo/internal/app"
	"time"
)

//go:embed all:frontend/dist
var web embed.FS

func main() {
	addr := flag.String("addr", env("TEMPO_ADDR", ":8080"), "listen address")
	data := flag.String("data", env("TEMPO_DATA", "data/tempo.json"), "state file")
	authFile := flag.String("auth-file", env("TEMPO_AUTH_FILE", ""), "credential and session-key file (default: <data>.auth.json)")
	adminUser := flag.String("admin-user", env("TEMPO_ADMIN_USER", "admin"), "bootstrap admin username")
	adminPassword := flag.String("admin-password", env("TEMPO_ADMIN_PASSWORD", ""), "bootstrap admin password (prefer environment variable)")
	adminPasswordFile := flag.String("admin-password-file", env("TEMPO_ADMIN_PASSWORD_FILE", ""), "read bootstrap admin password from a file")
	secureCookie := flag.Bool("secure-cookie", envBool("TEMPO_SECURE_COOKIE", false), "mark session cookie Secure; enable behind HTTPS")
	flag.Parse()
	if *authFile == "" {
		*authFile = *data + ".auth.json"
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if *adminPassword == "" && *adminPasswordFile != "" {
		info, err := os.Stat(*adminPasswordFile)
		if err != nil {
			logger.Error("stat admin password file", "error", err)
			os.Exit(1)
		}
		if info.Mode().Perm()&0o077 != 0 {
			logger.Error("admin password file must not be accessible by group or others", "mode", info.Mode().Perm())
			os.Exit(1)
		}
		body, err := os.ReadFile(*adminPasswordFile)
		if err != nil {
			logger.Error("read admin password file", "error", err)
			os.Exit(1)
		}
		*adminPassword = strings.TrimSpace(string(body))
	}
	store, err := app.NewStore(*data)
	if err != nil {
		logger.Error("open store", "error", err)
		os.Exit(1)
	}
	auth, err := app.NewAuth(*authFile, *adminUser, *adminPassword, *secureCookie)
	if err != nil {
		logger.Error("open auth", "error", err)
		os.Exit(1)
	}
	static, err := fs.Sub(web, "frontend/dist")
	if err != nil {
		logger.Error("open frontend", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: *addr, Handler: app.NewServer(store, static, logger, auth), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	logger.Info("tempo running", "addr", *addr, "data", *data, "auth", *authFile, "secure_cookie", *secureCookie)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
