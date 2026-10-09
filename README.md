# Talanta Tickets

Mobile-first ticket booking with interactive seat selection for Talanta Stadium, Nairobi.
The design and 12-week build plan are in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

| Path | What |
|------|------|
| `backend/` | Go API (chi, pgx), migrations |
| `mobile/` | Fan and gate-staff app (Expo, expo-router) |
| `admin/` | Organiser console (React, Vite) |
| `packages/shared/` | Types and API client used by both apps |
| `deploy/` | Production compose file and Caddy block |

## Run it locally

Needs Docker, Node 22+, pnpm 9 and Go 1.27.

```bash
make dev        # Postgres + migrations + API on http://localhost:8080, Adminer on :8081
make install    # JS dependencies
make admin      # admin web on http://localhost:5173 (proxies /v1 to the API)
make mobile     # Expo dev server
```

To reach the API from a phone, put your machine's LAN address in `mobile/.env.local`:

```
EXPO_PUBLIC_API_URL=http://192.168.x.x:8080
```

`make help` lists the other targets.

## Deploying

Pushing to `main` deploys the backend and the admin web app; mobile builds are started by hand
from the Actions tab. Setup steps are in [docs/deployment.md](docs/deployment.md).
