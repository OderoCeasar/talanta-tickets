# CI/CD and deployment

Production host: `https://talanta.vendorsoko.com` — the admin web app at `/`, the API at `/v1/*`.
The mobile app calls the same host.

## Pipelines (`.github/workflows/`)

| Workflow | Trigger | What it does |
|----------|---------|--------------|
| `ci.yml` | every push / PR | Go fmt + vet + test + build; JS typecheck (shared/admin/mobile) + admin build |
| `deploy-backend.yml` | push to `main` touching `backend/**` (or manual) | build the API image → push to GHCR → SSH to the server → run migrations → restart |
| `deploy-admin.yml` | push to `main` touching `admin/**` or `packages/shared/**` (or manual) | build the admin → rsync `dist/` to the server; live immediately |
| `build-mobile.yml` | manual only ("Run workflow") | start an EAS cloud build; it appears on expo.dev |

## How it sits on the server

The Hetzner server already runs Sherehe (`/opt/sherehe`) and Twende (`/opt/twende`). Sherehe's
stack owns the only Caddy, on ports 80/443. This project plugs into it the way Twende does:

```
Cloudflare (proxied) ──► Caddy (in /opt/sherehe)
                           ├─ /v1/*  ──► talanta-api:8080     (/opt/talanta, this repo's stack)
                           │                └─► talanta-db    (private to this stack)
                           └─ /*     ──► /srv/talanta-admin   (= /opt/sherehe/talanta-admin on disk)
```

- `/opt/talanta` holds this repo's stack: `talanta-api`, a one-shot `migrate`, and its own
  Postgres `talanta-db`. See `deploy/docker-compose.prod.yml`.
- The API joins Caddy's docker network (`sherehe_default`) so Caddy reaches it by name.
- The admin build is rsynced into `/opt/sherehe/talanta-admin`, which Caddy serves off disk.
- The Caddy site block lives in the **event-vendors-platform** repo, not here. That repo's
  deploy overwrites the server's Caddyfile on every backend push, so a hand edit on the server
  is lost. `deploy/talanta.caddy` is the reference copy of the block.

## One-time setup

### 1. GitHub repository and secrets
The repo is `OderoCeasar/talanta-tickets` (the Go module path and `API_IMAGE` assume it).
Add these under Settings → Secrets and variables → Actions. They are the same names and
values the other two projects use:

- `HETZNER_HOST` — the server IP
- `HETZNER_USER` — the deploy SSH user
- `HETZNER_SSH_KEY` — that user's private key
- `HETZNER_SSH_PORT` — optional (default 22)
- `GHCR_PAT` — optional; only if the GHCR image stays private (a PAT with `read:packages`)
- `EXPO_TOKEN` — from https://expo.dev → Account settings → Access tokens

### 2. Server directories and `.env`
As root on the server, replacing `deploy` with the `HETZNER_USER` value:

```bash
mkdir -p /opt/talanta /opt/sherehe/talanta-admin
chown deploy:deploy /opt/talanta /opt/sherehe/talanta-admin
```

Then, as the deploy user, create `/opt/talanta/.env` from `deploy/.env.example`, fill in
`POSTGRES_PASSWORD` (`openssl rand -hex 24`) and the matching `DATABASE_URL`, and
`chmod 600 /opt/talanta/.env`.

### 3. DNS
In Cloudflare, add an `A` record `talanta` → the server IP, **proxied** (orange cloud). The
host uses the vendorsoko Origin certificate, which only Cloudflare trusts.

### 4. Caddy block (pull request to event-vendors-platform)
`git pull` that repo first, then in one pull request:

1. Append the block from `deploy/talanta.caddy` to its `deploy/Caddyfile`.
2. Add this line to the `caddy` service's volumes in its `deploy/docker-compose.prod.yml`:
   ```yaml
   - ./talanta-admin:/srv/talanta-admin:ro
   ```

Step 2 above must be done before this merges. Merging recreates the Caddy container to add
the mount, which interrupts every site on the server for a few seconds.

### 5. First deploy
Push to `main`. `deploy-backend.yml` and `deploy-admin.yml` run. Check:

```bash
curl https://talanta.vendorsoko.com/healthz      # {"status":"ok"}
docker compose -f /opt/talanta/docker-compose.prod.yml ps -a   # api + db up, migrate exited 0
```

### 6. Mobile (EAS)
Once, on your machine:

```bash
cd mobile
npx eas-cli login
npx eas-cli init        # creates the EAS project and writes its projectId into app.json
```

Commit the `app.json` change. After that, run **Build mobile (EAS)** from the Actions tab and
pick a platform and profile. The API URL each profile builds against is set in `mobile/eas.json`.

## Not set up yet

- **Database backups.** `talanta-db` has none. Sherehe's `deploy/postgres/backup.sh` only dumps
  the databases in its own container. Add a nightly `pg_dump` of `talanta-db` before real data
  lives here.
