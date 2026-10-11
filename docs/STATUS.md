# Status

One line each. Updated at the end of every milestone.

## Done

- M1 Foundation: `make dev` brings up db, migrate, api, worker, adminer; `make test` and `make lint` pass. CI workflow left as it was (deployment files are off limits).
- M2 Schema and seed: migrations 000002–000007 go up/down/up; seeder makes 59,580 seats (layout 2.8 s) and inserts 0 rows on a second run.
- API contract: `backend/docs/openapi.yaml` (72 operations), lints clean, TypeScript types generate.

- M3 Auth: phone OTP, email + password with optional two-step, rotating refresh tokens with reuse detection, role checks, staff invitations, first admin from env. Integration suite passes; the invite journey was run in a browser against the real API.
- Admin web, first slice (on `main`): login, Team (invite, resend, revoke), set-password page, account page.

## Partly done

- M9 Admin web: events, venue layout, pricing and sales pages are placeholders.
- M8 Mobile fan flow: in progress.

## Not started

- M4 Events and inventory
- M5 Seat map API
- M6 Holds and locking
- M7 Bookings, payments, tickets
- M8 Mobile fan flow
- M9 Admin web
- M10 Gate mode
- M11 Hardening

## Known bugs and limits

- Publishing 59,568 seats takes 26–37 s on the dev laptop (four foreign-key checks per row on `postgres:16-alpine`); M4 must run publish outside the 30 s request timeout.
- No real SMS provider: in production the OTP is not delivered, so fans cannot sign in there until one is added (M7 adds the interface; a provider account is the owner's).
- Rate limits, TOTP replay protection and MFA-token single use are held in memory per process: a restart resets them, and a second API instance would not share them.
- No "forgot password" flow for staff, and no way to disable a staff user or change a role.
- Ten wrong passwords in 15 minutes for one email also holds back the real owner of that account.
- staticcheck is not run: its newest release cannot type-check Go 1.27.

## For the owner to do in production (not done by this build)

- Before merging `backend/auth-invitations`, add to `/opt/talanta/.env` (the API refuses to start in production without the first one):
  - `JWT_SECRET` — required, at least 32 random bytes (`openssl rand -base64 48`).
  - `APP_ENV=production` — unset already means production, but say it explicitly.
  - `ADMIN_BASE_URL=https://talanta.vendorsoko.com` — otherwise invite links point at localhost.
  - `BOOTSTRAP_ADMIN_EMAIL` and `BOOTSTRAP_ADMIN_PASSWORD` (10+ characters) — creates the first admin at startup when none exists; remove the password from the file afterwards.
  - `TRUSTED_PROXY_CIDRS` — the docker network range Caddy connects from; without it every caller shares one per-IP rate limit bucket.
  - Optional: `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM` to email invitations. Without them the admin copies the link.
  - The full list with defaults is in `backend/.env.example`.
- Before merging the schema commit, check production has no rows that migration 000002 rejects, or the migrate step fails and leaves the schema marked dirty: `SELECT count(*) FROM users WHERE phone_e164 !~ '^\+[1-9][0-9]{7,14}$';` and `SELECT lower(email), count(*) FROM users WHERE email IS NOT NULL GROUP BY 1 HAVING count(*) > 1;` must both return nothing.
- The venue layout exists only after the seeder runs. Against production it creates the venue and the template only: no demo events and no demo users.
- Migration 000005 adds NOT NULL columns to `tickets` and `scan_logs`; it fails if production already has rows in those tables.
- The worker (`cmd/worker`: hold sweeper, payment reconciliation) is not in the production image or compose file. Until it is, expired holds are still reclaimed at hold time, but unpaid bookings are not expired and payments are not reconciled.
- New env vars for `/opt/talanta/.env` will be listed here as each milestone adds them.
