#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  exec sudo -n "$0" "$@"
fi

DOMAIN="tasks.shuvrojit.com"
EXPECTED_IP="34.171.177.20"
STATE_FILE="/var/lib/tempo/.tls-ready"
LOG_FILE="/var/log/tempo-tls-setup.log"

exec 9>/run/tempo-tls-watch.lock
flock -n 9 || exit 0
sudo test -f "$STATE_FILE" && exit 0

DNS_JSON="$(curl -fsS --max-time 20 "https://dns.google/resolve?name=${DOMAIN}&type=A" 2>/dev/null || true)"
[[ -n "$DNS_JSON" ]] || exit 0
if ! DNS_JSON="$DNS_JSON" EXPECTED_IP="$EXPECTED_IP" python3 - <<'PY'
import json, os
try:
    data = json.loads(os.environ["DNS_JSON"])
except Exception:
    raise SystemExit(1)
answers = [row.get("data") for row in data.get("Answer", []) if row.get("type") == 1]
raise SystemExit(0 if os.environ["EXPECTED_IP"] in answers else 1)
PY
then
  exit 0
fi

if ! sudo certbot --apache -d "$DOMAIN" --non-interactive --agree-tos --redirect --register-unsafely-without-email >"$LOG_FILE" 2>&1; then
  echo "Tempo TLS setup attempted after DNS became ready but Certbot failed. Inspect $LOG_FILE."
  exit 1
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
if [[ ! -f /etc/tempo/tempo.env ]]; then
  echo "Missing /etc/tempo/tempo.env; refusing to erase the MongoDB configuration."
  exit 1
fi
if ! awk '
  /^[[:space:]]*TEMPO_SECURE_COOKIE=/ {
    if (!updated) print "TEMPO_SECURE_COOKIE=true"
    updated = 1
    next
  }
  { print }
  END { if (!updated) print "TEMPO_SECURE_COOKIE=true" }
' /etc/tempo/tempo.env >"$tmp"; then
  echo "Could not update /etc/tempo/tempo.env."
  exit 1
fi
sudo install -o root -g root -m 0600 "$tmp" /etc/tempo/tempo.env
sudo apache2ctl configtest >>"$LOG_FILE" 2>&1
sudo systemctl reload apache2
sudo systemctl restart tempo

for _ in $(seq 1 30); do
  if curl -fsS --max-time 15 "https://${DOMAIN}/api/health" >/dev/null 2>&1; then
    sudo touch "$STATE_FILE"
    echo "Tempo deployment completed: https://${DOMAIN} is live with HTTPS, redirect, Secure cookies, HSTS, Apache, and systemd supervision."
    exit 0
  fi
  sleep 2
done

echo "Tempo certificate was issued, but the HTTPS health check did not become ready. Inspect $LOG_FILE."
exit 1
