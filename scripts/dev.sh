#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
FRONTEND_DIR="$ROOT_DIR/frontend"
DEV_BINARY="$ROOT_DIR/bin/tempo-dev"
NEXT_BINARY="$ROOT_DIR/bin/.tempo-dev.next"
PREVIOUS_BINARY="$ROOT_DIR/bin/.tempo-dev.previous"
ENV_FILE="${TEMPO_ENV_FILE:-$ROOT_DIR/.env}"
POLL_INTERVAL="${TEMPO_DEV_POLL_INTERVAL:-0.75}"

BACKEND_PID=""
VITE_PID=""
BOOTSTRAP_DIR=""
BOOTSTRAP_PASSWORD_FILE=""

log() {
  printf '[tempo-dev] %s\n' "$*"
}

fail() {
  printf '[tempo-dev] error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

stop_process() {
  local pid="${1:-}" attempt
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    for attempt in {1..30}; do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.1
    done
    if kill -0 "$pid" 2>/dev/null; then
      kill -KILL "$pid" 2>/dev/null || true
    fi
    wait "$pid" 2>/dev/null || true
  fi
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  stop_process "$VITE_PID"
  stop_process "$BACKEND_PID"
  clear_bootstrap_credentials
  log "Development servers stopped."
  exit "$status"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

load_environment() {
  if [[ "${TEMPO_LOAD_ENV:-true}" == "false" || ! -f "$ENV_FILE" ]]; then
    return
  fi

  log "Loading environment from $ENV_FILE"
  local mode
  mode="$(stat -c '%a' "$ENV_FILE" 2>/dev/null || true)"
  if [[ "$mode" =~ ^[0-7]{3,4}$ && "${mode: -2}" != "00" ]]; then
    log "Warning: $ENV_FILE is readable by group or others; consider: chmod 600 $ENV_FILE"
  fi
  set +u
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
  set -u
}

clear_bootstrap_credentials() {
  unset TEMPO_ADMIN_PASSWORD TEMPO_ADMIN_PASSWORD_FILE
  if [[ -n "$BOOTSTRAP_PASSWORD_FILE" && "$BOOTSTRAP_PASSWORD_FILE" == /tmp/tempo-dev-auth.*/password ]]; then
    rm -f -- "$BOOTSTRAP_PASSWORD_FILE"
  fi
  if [[ -n "$BOOTSTRAP_DIR" && "$BOOTSTRAP_DIR" == /tmp/tempo-dev-auth.* ]]; then
    rmdir -- "$BOOTSTRAP_DIR" 2>/dev/null || true
  fi
  BOOTSTRAP_PASSWORD_FILE=""
  BOOTSTRAP_DIR=""
}

store_bootstrap_password() {
  local password="$1"
  if [[ -z "$BOOTSTRAP_DIR" ]]; then
    BOOTSTRAP_DIR="$(mktemp -d /tmp/tempo-dev-auth.XXXXXX)"
    BOOTSTRAP_PASSWORD_FILE="$BOOTSTRAP_DIR/password"
  fi
  (umask 077; printf '%s\n' "$password" >"$BOOTSTRAP_PASSWORD_FILE")
  chmod 0600 "$BOOTSTRAP_PASSWORD_FILE"
  unset TEMPO_ADMIN_PASSWORD
  export TEMPO_ADMIN_PASSWORD_FILE="$BOOTSTRAP_PASSWORD_FILE"
}

stage_inline_bootstrap_password() {
  if [[ -n "${TEMPO_ADMIN_PASSWORD:-}" ]]; then
    store_bootstrap_password "$TEMPO_ADMIN_PASSWORD"
  fi
}

prompt_bootstrap_password() {
  local password confirmation
  [[ -t 0 ]] || fail "MongoDB has no Tempo auth record; set TEMPO_ADMIN_PASSWORD or TEMPO_ADMIN_PASSWORD_FILE once"

  read -r -s -p "First-run Tempo admin password (at least 12 characters): " password
  printf '\n'
  ((${#password} >= 12)) || fail "admin password must contain at least 12 characters"
  read -r -s -p "Confirm admin password: " confirmation
  printf '\n'
  [[ "$password" == "$confirmation" ]] || fail "passwords do not match"
  store_bootstrap_password "$password"
  unset password confirmation
}

install_frontend_dependencies() {
  if [[ -x "$FRONTEND_DIR/node_modules/.bin/vite" ]]; then
    return
  fi

  log "Installing frontend dependencies..."
  (
    cd "$FRONTEND_DIR"
    pnpm install --frozen-lockfile
  )
}

build_frontend() {
  log "Building the frontend..."
  (
    cd "$FRONTEND_DIR"
    pnpm build
  )
}

build_backend() {
  log "Building the Go server..."
  if (
    cd "$ROOT_DIR"
    CGO_ENABLED=0 go build -trimpath -o "$NEXT_BINARY" .
  ); then
    return 0
  fi

  log "Go build failed; the current server will keep running."
  return 1
}

activate_backend() {
  mv -f -- "$NEXT_BINARY" "$DEV_BINARY"
}

start_backend() {
  log "Starting the Go API at http://127.0.0.1:8080..."
  (
    cd "$ROOT_DIR"
    exec "$DEV_BINARY"
  ) &
  BACKEND_PID=$!
}

wait_for_backend() {
  local health attempt status

  for attempt in {1..48}; do
    if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
      status=0
      wait "$BACKEND_PID" || status=$?
      return "$status"
    fi
    health="$(curl -fsS --max-time 1 http://127.0.0.1:8080/api/health 2>/dev/null || true)"
    if [[ "$health" == *"\"status\":\"ok\""* && "$health" == *'"storage":"mongodb"'* ]]; then
      log "Go API is healthy (storage: mongodb)."
      return 0
    fi
    sleep 0.25
  done

  return 1
}

start_frontend() {
  log "Starting Vite at http://127.0.0.1:5173..."
  (
    cd "$FRONTEND_DIR"
    unset TEMPO_ADMIN_PASSWORD TEMPO_ADMIN_PASSWORD_FILE TEMPO_MONGO_URI
    exec ./node_modules/.bin/vite --host 127.0.0.1 --port 5173 --strictPort
  ) &
  VITE_PID=$!
}

backend_snapshot() {
  {
    find "$ROOT_DIR" -maxdepth 1 -type f \
      \( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' \) \
      -printf '%p:%T@:%s\n'
    find "$ROOT_DIR/internal" -type f -name '*.go' -printf '%p:%T@:%s\n'
  } | LC_ALL=C sort
}

watch_backend() {
  local previous current
  previous="$(backend_snapshot)"

  while true; do
    sleep "$POLL_INTERVAL"

    if ! kill -0 "$VITE_PID" 2>/dev/null; then
      wait "$VITE_PID" || true
      fail "Vite stopped unexpectedly"
    fi
    if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
      wait "$BACKEND_PID" || true
      fail "Go server stopped unexpectedly"
    fi

    current="$(backend_snapshot)"
    if [[ "$current" == "$previous" ]]; then
      continue
    fi
    previous="$current"

    log "Backend change detected. Rebuilding..."
    if build_backend; then
      cp -p -- "$DEV_BINARY" "$PREVIOUS_BINARY"
      stop_process "$BACKEND_PID"
      activate_backend
      start_backend
      if wait_for_backend; then
        rm -f -- "$PREVIOUS_BINARY"
        previous="$(backend_snapshot)"
        log "Go server reloaded."
      else
        stop_process "$BACKEND_PID"
        mv -f -- "$PREVIOUS_BINARY" "$DEV_BINARY"
        start_backend
        wait_for_backend || fail "reloaded Go server failed and the previous server could not be restored"
        previous="$(backend_snapshot)"
        log "Reload failed health checks; restored the previous Go server."
      fi
    fi
  done
}

main() {
  local status
  require_command go
  require_command pnpm
  require_command find
  require_command curl

  cd "$ROOT_DIR"
  load_environment

  [[ -n "${TEMPO_MONGO_URI:-}" ]] || fail "TEMPO_MONGO_URI is required; add it to .env"
  stage_inline_bootstrap_password

  # Vite's development proxy targets this fixed local address.
  export TEMPO_ADDR="127.0.0.1:8080"

  mkdir -p "$ROOT_DIR/bin"
  install_frontend_dependencies
  build_frontend
  build_backend
  activate_backend

  log "Persistence: MongoDB (${TEMPO_MONGO_DATABASE:-tempo}; state=${TEMPO_MONGO_COLLECTION:-app_state}; auth=${TEMPO_MONGO_AUTH_COLLECTION:-auth})"

  start_backend
  if wait_for_backend; then
    :
  else
    status=$?
    if [[ "$status" -eq 3 && -z "${TEMPO_ADMIN_PASSWORD:-}" && -z "${TEMPO_ADMIN_PASSWORD_FILE:-}" ]]; then
      prompt_bootstrap_password
      start_backend
      wait_for_backend || fail "Go server did not become healthy after MongoDB auth bootstrap"
    else
      fail "Go server stopped during startup (exit status $status)"
    fi
  fi
  clear_bootstrap_credentials
  start_frontend
  log "Open http://127.0.0.1:5173 (frontend changes use HMR; Go changes restart the API)."
  watch_backend
}

main "$@"
