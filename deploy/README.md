# Tempo production deployment

The production host uses:

- `/opt/tempo/tempo` — static application binary
- MongoDB `tempo.app_state` — production application state
- MongoDB `tempo.auth` — admin identity, password verifier, and session-signing key
- `/etc/tempo/tempo.env` — mode-0600 runtime flags and MongoDB URI
- `tempo.service` — unprivileged, systemd-hardened process bound to `127.0.0.1:8085`
- Apache — public reverse proxy for `tasks.shuvrojit.com`

`TEMPO_MONGO_URI` is mandatory. The bootstrap password is needed only when the MongoDB authentication collection is empty; remove its plaintext file immediately after the first successful start. Later starts need no bootstrap password. Enable `TEMPO_SECURE_COOKIE=true` only after HTTPS is active.

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
TEMPO_MONGO_AUTH_COLLECTION=auth
```

MongoDB must bind to `127.0.0.1`, have authorization enabled, and grant the application user only `readWrite` on the `tempo` database. The systemd unit requires and starts after `mongod.service`.

Keep `TEMPO_MONGO_COLLECTION` and `TEMPO_MONGO_AUTH_COLLECTION` distinct. Authentication updates use majority acknowledgement and primary read-back before the running account changes.

For a new installation, create a password of at least 12 characters in a mode-`0600` file owned by the `tempo` user, then temporarily add its path to `/etc/tempo/tempo.env`:

```text
TEMPO_ADMIN_PASSWORD_FILE=/var/lib/tempo/bootstrap-password
```

Start Tempo and verify that login succeeds. The resulting account is stored in `tempo.auth` with a salted Argon2id password hash. Remove the bootstrap file and the `TEMPO_ADMIN_PASSWORD_FILE` line afterward; neither is needed on subsequent starts.

For a one-time migration from an older JSON-based installation, temporarily add the applicable explicit import paths:

```text
TEMPO_LEGACY_STATE_FILE=/var/lib/tempo/tempo.json
TEMPO_LEGACY_AUTH_FILE=/var/lib/tempo/auth.json
```

Tempo imports each file only when the corresponding MongoDB document (`_id: "tempo"`) is absent. Existing MongoDB records always win, and normal startup does not consult JSON. After verifying the state and login, remove these environment entries and the obsolete JSON files. `TEMPO_DATA` and `TEMPO_AUTH_FILE` are deprecated aliases and should not be used for new migrations.

Back up both `tempo.app_state` and `tempo.auth` with authenticated `mongodump`, and restore them from the same backup point. Run only one Tempo process for this database and collection pair: application mutations are serialized within one process, but multiple instances can overwrite the singleton state document.

After DNS resolves, run `deploy/complete-tls.sh` (or install it as a scheduled watcher). It verifies the A record points to the expected server, issues the Apache certificate, enables redirect mode, switches `TEMPO_SECURE_COOKIE=true`, restarts Tempo, and verifies the HTTPS health endpoint.

The current host keeps a bounded scheduler watcher for 24 hours; it is silent until DNS is ready and reports once after successful HTTPS activation.
