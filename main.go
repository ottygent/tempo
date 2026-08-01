package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
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
	mongoURI := flag.String("mongo-uri", env("TEMPO_MONGO_URI", ""), "MongoDB connection URI (required)")
	mongoDatabase := flag.String("mongo-database", env("TEMPO_MONGO_DATABASE", "tempo"), "MongoDB database")
	mongoCollection := flag.String("mongo-collection", env("TEMPO_MONGO_COLLECTION", "app_state"), "MongoDB state collection")
	mongoAuthCollection := flag.String("mongo-auth-collection", env("TEMPO_MONGO_AUTH_COLLECTION", "auth"), "MongoDB authentication collection")
	legacyState := flag.String("legacy-state-file", env("TEMPO_LEGACY_STATE_FILE", env("TEMPO_DATA", "")), "one-time legacy state JSON import source")
	legacyAuth := flag.String("legacy-auth-file", env("TEMPO_LEGACY_AUTH_FILE", env("TEMPO_AUTH_FILE", "")), "one-time legacy auth JSON import source")
	flag.StringVar(legacyState, "data", *legacyState, "deprecated alias for -legacy-state-file")
	flag.StringVar(legacyAuth, "auth-file", *legacyAuth, "deprecated alias for -legacy-auth-file")
	adminUser := flag.String("admin-user", env("TEMPO_ADMIN_USER", "admin"), "bootstrap admin username")
	adminPassword := flag.String("admin-password", env("TEMPO_ADMIN_PASSWORD", ""), "bootstrap admin password (prefer environment variable)")
	adminPasswordFile := flag.String("admin-password-file", env("TEMPO_ADMIN_PASSWORD_FILE", ""), "read bootstrap admin password from a file")
	secureCookie := flag.Bool("secure-cookie", envBool("TEMPO_SECURE_COOKIE", false), "mark session cookie Secure; enable behind HTTPS")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if strings.TrimSpace(*mongoURI) == "" {
		logger.Error("TEMPO_MONGO_URI is required; JSON runtime persistence has been removed")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	store, err := app.NewMongoStore(ctx, *mongoURI, *mongoDatabase, *mongoCollection, *mongoAuthCollection, *legacyState)
	cancel()
	if err != nil {
		logger.Error("open store", "error", err)
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := store.Close(ctx); closeErr != nil {
			logger.Error("close store", "backend", store.BackendName(), "error", closeErr)
		}
	}()
	auth, err := app.NewAuthForStore(store, *legacyAuth, *adminUser, *adminPassword, *secureCookie)
	if errors.Is(err, app.ErrAuthBootstrapRequired) && *adminPassword == "" && *adminPasswordFile != "" {
		*adminPassword, err = readAdminPasswordFile(*adminPasswordFile)
		if err == nil {
			auth, err = app.NewAuthForStore(store, *legacyAuth, *adminUser, *adminPassword, *secureCookie)
		}
	}
	_ = os.Unsetenv("TEMPO_ADMIN_PASSWORD")
	_ = os.Unsetenv("TEMPO_ADMIN_PASSWORD_FILE")
	*adminPassword = ""
	if err != nil {
		logger.Error("open auth", "error", err)
		if errors.Is(err, app.ErrAuthBootstrapRequired) {
			os.Exit(3)
		}
		os.Exit(1)
	}
	static, err := fs.Sub(web, "frontend/dist")
	if err != nil {
		logger.Error("open frontend", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: *addr, Handler: app.NewServer(store, static, logger, auth), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	logger.Info("tempo running", "addr", *addr, "storage", store.BackendName(), "auth_storage", auth.StorageName(), "secure_cookie", *secureCookie)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func readAdminPasswordFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("stat admin password file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("admin password file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("admin password file must not be accessible by group or others (mode %v)", info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read admin password file: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
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
