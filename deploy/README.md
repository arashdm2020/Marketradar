# MarketRadar production deployment

This deployment targets Ubuntu 24.04 with Docker/Compose and the existing host-networked Traefik instance. It does **not** enable Treasury sweeping. The Go service, Next.js service, and Traefik all use the host network, while the application listeners remain bound to `127.0.0.1`; no application port is publicly published by Docker.

## Architecture and routes

- `marketradar-go`: one process running `marketradar bot`, Telegram long polling, analysis workers, payment monitor, Go user portal, and authenticated Go APIs on `127.0.0.1:8088`.
- `marketradar-admin`: Next.js standalone server on `127.0.0.1:3000`; server-side proxy uses `GO_ADMIN_URL=http://127.0.0.1:8088`.
- `admin-radar.foxwd.tech`: Next.js administration interface.
- `api-radar.foxwd.tech`: authenticated Go `/api` and `/auth` routes only.
- `radar.foxwd.tech`: the existing Go user portal at `/app/`; `/` redirects to `/app/`. This is a real registration, subscription, invoice, and analysis UI in `internal/webapp/user.html`, not an invented service.

Docker-provider address discovery is intentionally not used for these host-networked containers. `traefik/marketradar.yml` defines explicit loopback upstream URLs through Traefik's file provider.

## Prepare the VPS (do not commit secrets)

```bash
sudo install -d -m 0750 /opt/marketradar/app /opt/marketradar/secrets /opt/marketradar/backups
git clone https://github.com/arashdm2020/Marketradar.git /opt/marketradar/app
sudo cp /opt/marketradar/app/deploy/marketradar.env.example /opt/marketradar/secrets/marketradar.env
sudo chmod 600 /opt/marketradar/secrets/marketradar.env
sudoedit /opt/marketradar/secrets/marketradar.env
```

Set the provider credentials and a persistent `AI_CONFIG_MASTER_KEY`. Keep these deployment locks unchanged initially:

```dotenv
TREASURY_MAINNET_ENABLED=false
TREASURY_TESTNET_ENABLED=false
TREASURY_SWEEP_ENABLED=false
WEB_BSC_TEST_INVOICES_ENABLED=false
```

`SQLITE_PATH` and `ADMIN_LISTEN_ADDR` are enforced by Compose. Do not put private keys, the populated environment file, or database backups inside the Git checkout.

## Traefik routing

Confirm the existing Traefik static configuration has a watched file-provider directory, for example:

```yaml
providers:
  docker: {}
  file:
    directory: /etc/traefik/dynamic
    watch: true
```

Copy `deploy/traefik/marketradar.yml` into that already-mounted directory. If the configured ACME resolver is not named `letsencrypt`, change only the four `certResolver` values to the existing resolver's actual name. Validate before reloading Traefik:

```bash
docker compose -f /path/to/traefik/compose.yaml config --quiet
```

Enabling the file provider or mounting its directory is the only possible one-time Traefik integration change when no watched directory already exists. Do not add Docker labels that depend on host-network service discovery.

## Validate and build

From `/opt/marketradar/app`:

```bash
export MARKETRADAR_ENV_FILE=/opt/marketradar/secrets/marketradar.env
docker compose -f deploy/compose.yaml config --quiet
docker compose -f deploy/compose.yaml build --pull
```

The builds require outbound access to Go modules, npm, and Debian registries. Runtime requires outbound HTTPS access to Telegram, Twelve Data, NVIDIA, and the configured BSC RPC. No inbound application port other than Traefik's existing 80/443 is required.

## Transfer the existing SQLite database safely

SQLite runs in WAL mode. Never copy only `tradebot.db` while MarketRadar is running.

Recommended cutover from the Windows machine:

1. Stop the existing polling process with `Stop-MarketRadar.cmd`. Confirm `marketradar.exe` is no longer running. This prevents two Telegram `getUpdates` consumers.
2. If `sqlite3` is installed, create a consistent online-format backup while the app is stopped:

   ```powershell
   sqlite3.exe .\data\tradebot.db ".timeout 10000" ".backup '.\tradebot-production.db'"
   sqlite3.exe .\tradebot-production.db "PRAGMA integrity_check;"
   ```

3. If `sqlite3` is unavailable, with the process stopped copy `tradebot.db`, `tradebot.db-wal`, and `tradebot.db-shm` together. Do not omit WAL/SHM. Prefer installing SQLite and using `.backup`.
4. Transfer the backup through an authenticated administrative channel to `/opt/marketradar/backups/`; do not put it in Git.
5. Before first start, restore it into the named volume:

   ```bash
   docker volume create marketradar-data
   docker run --rm \
     -v marketradar-data:/data \
     -v /opt/marketradar/backups:/backup:ro \
     alpine:3.21 sh -c 'cp /backup/tradebot-production.db /data/tradebot.db && chown 10001:10001 /data/tradebot.db && chmod 600 /data/tradebot.db'
   ```

6. Start exactly one Go replica only after the Windows bot is stopped.

## Start, inspect, and stop

```bash
export MARKETRADAR_ENV_FILE=/opt/marketradar/secrets/marketradar.env
docker compose -f deploy/compose.yaml up -d --build
docker compose -f deploy/compose.yaml ps
docker compose -f deploy/compose.yaml logs -f --tail=200 marketradar-go marketradar-admin
docker compose -f deploy/compose.yaml stop
docker compose -f deploy/compose.yaml down        # preserves the named data volume
```

Never run `docker compose up --scale marketradar-go=2`; Telegram long polling and SQLite require a single Go instance in this deployment.

## Health checks

```bash
docker inspect --format '{{json .State.Health}}' marketradar-marketradar-go-1
docker inspect --format '{{json .State.Health}}' marketradar-marketradar-admin-1
curl -fsS https://admin-radar.foxwd.tech/login >/dev/null
curl -fsS https://radar.foxwd.tech/app/ >/dev/null
```

The container health checks are local-only and do not call paid AI inference. A healthy container does not prove that external providers are reachable; inspect sanitized application logs for provider connectivity.

## Recurring backup

For a simple consistent backup, briefly stop only the Go service, archive the entire volume, then restart it:

```bash
docker compose -f deploy/compose.yaml stop marketradar-go
docker run --rm -v marketradar-data:/data:ro -v /opt/marketradar/backups:/backup alpine:3.21 \
  sh -c 'tar -C /data -czf /backup/marketradar-$(date -u +%Y%m%dT%H%M%SZ).tgz .'
docker compose -f deploy/compose.yaml start marketradar-go
```

Keep backup permissions restricted and test restoration separately. A stopped-volume archive includes any WAL/SHM files consistently.

## Rollback

```bash
docker compose -f deploy/compose.yaml down
git fetch origin
git checkout <previous-tested-commit>
export MARKETRADAR_ENV_FILE=/opt/marketradar/secrets/marketradar.env
docker compose -f deploy/compose.yaml build
docker compose -f deploy/compose.yaml up -d
```

If a database migration is not backward-compatible, stop the stack and restore the matching pre-upgrade volume backup before starting the previous image. Never roll back by deleting the persistent volume.
