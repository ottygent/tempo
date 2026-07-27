# Tempo production deployment

The production host uses:

- `/opt/tempo/tempo` — static application binary
- MongoDB `tempo.app_state` — production application state
- `/var/lib/tempo/tempo.json` — one-time migration source and rollback backup
- `/var/lib/tempo/auth.json` — mode-0600 hashed credential and signing key
- `/etc/tempo/tempo.env` — mode-0600 runtime flags and MongoDB URI
- `tempo.service` — unprivileged, systemd-hardened process bound to `127.0.0.1:8085`
- Apache — public reverse proxy for `tasks.shuvrojit.com`

The bootstrap password is used only to create `auth.json`; remove the plaintext bootstrap file immediately afterward. Enable `TEMPO_SECURE_COOKIE=true` only after HTTPS is active.

## DNS

Create an A record:

```text
tasks.shuvrojit.com.  A  <server IPv4>
```

## Installation sketch

```bash
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin tempo
sudo install -d -o root -g root -m 0755 /opt/tempo /etc/tempo
sudo install -d -o tempo -g tempo -m 0700 /var/lib/tempo
sudo install -o root -g root -m 0755 bin/tempo /opt/tempo/tempo
sudo install -o root -g root -m 0644 deploy/tempo.service /etc/systemd/system/tempo.service
sudo install -o root -g root -m 0644 deploy/tasks.shuvrojit.com.conf /etc/apache2/sites-available/tasks.shuvrojit.com.conf
```

Create `/etc/tempo/tempo.env` with mode `0600`:

```text
TEMPO_SECURE_COOKIE=false
TEMPO_MONGO_URI=mongodb://<user>:***@127.0.0.1:27017/tempo?authSource=tempo
TEMPO_MONGO_DATABASE=tempo
TEMPO_MONGO_COLLECTION=app_state
```

MongoDB must bind to `127.0.0.1`, have authorization enabled, and grant the application user only `readWrite` on the `tempo` database. On the first Mongo-backed start, Tempo imports `/var/lib/tempo/tempo.json` only if `_id: "tempo"` is absent. The systemd unit requires and starts after `mongod.service`.

After DNS resolves, run `deploy/complete-tls.sh` (or install it as a scheduled watcher). It verifies the A record points to the expected server, issues the Apache certificate, enables redirect mode, switches `TEMPO_SECURE_COOKIE=true`, restarts Tempo, and verifies the HTTPS health endpoint.

The current host keeps a bounded scheduler watcher for 24 hours; it is silent until DNS is ready and reports once after successful HTTPS activation.
