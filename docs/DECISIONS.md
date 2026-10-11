# Decisions

One line each: what was chosen and why. Newest at the bottom.

- **Keep `/v1` prefix on every route.** The admin site is served from the same origin and its
  client routes would collide with API paths; production Caddy already proxies `/v1/*`.
- **HTTP package is `internal/httpapi`, not `internal/http`.** A package named `http` shadows
  `net/http` in every file that imports both.
- **Migration `000001` is never edited.** The deploy workflow applies migrations on every push
  to `main`, so it may already be applied in production. Corrections go in later migrations.
- **Statuses are checked text, not Postgres enums.** `000001` already uses `CHECK` constraints,
  and adding a value to a check is a plain, reversible migration.
- **Gate group is `<stand code>-<tier>`, for example `W-LOWER`.** It follows directly from the
  stand a seat is in, which is what correction 8 binds a ticket to.
- **Booking fee is zero unless `BOOKING_FEE_BPS` is set.** The architecture has a `fee_cents`
  column but names no fee; basis points keep the sum in integers.
- **`make lint` runs `gofmt` and `go vet` only.** staticcheck v0.8.1 (the newest release)
  cannot type-check the Go 1.27 standard library ("method must have no type parameters" in
  `math/rand/v2`), and golangci-lint embeds the same checker. Add staticcheck back when it
  supports 1.27.
- **API clients come from `openapi-typescript` + `openapi-fetch`, generated into
  `packages/shared`.** Types only, no generated runtime, and one copy serves both apps.
- **The worker is a second binary (`cmd/worker`), built for local use by
  `backend/Dockerfile.dev`.** The sweeper and reconciliation never share a process with request
  handling. The production `Dockerfile`, `deploy/` and `.github/workflows/` are owned by the
  project owner and are not changed by this build; running the worker in production is listed
  in `docs/STATUS.md` as a step for them.
- **Deployment files are off limits.** `deploy/`, `.github/workflows/`, `backend/Dockerfile`
  and `mobile/eas.json` are already wired to production, and a push to `main` deploys. This
  build does not edit them and does not push.
- **Staff log in with email and password; TOTP is optional per user.** The owner asked for an
  email + password login page (2026-10-11). This overrides the "password + TOTP" line in
  `CLAUDE.md` and §11: the TOTP step runs only for users who have switched it on.
- **Staff accounts are created by invitation only.** An admin enters an email and a role; the
  invited person opens a one-time link and sets their own password. Only the hash of the
  invite token is stored, and it expires after 72 hours.
- **The invite link is shown to the inviting admin as well as emailed.** No mail provider is
  set up, so mail goes through an interface with a logging mock (SMTP when its env vars are
  set); the admin can always copy the link and send it themselves.
- **The first admin comes from env, not from an invitation.** In development the seeder
  creates it. In production the API creates it at startup from `BOOTSTRAP_ADMIN_EMAIL` and
  `BOOTSTRAP_ADMIN_PASSWORD` when no admin exists, so no pipeline change is needed.
- **`golang.org/x/crypto` v0.58.0 for argon2id.** The standard library has no argon2; this is
  the Go team's own implementation. Used only by `internal/auth/password.go`.
- **`github.com/stretchr/testify` v1.12.1, `github.com/testcontainers/testcontainers-go` and its
  `modules/postgres` v0.44.0.** The test stack named in `CLAUDE.md`, pinned. Integration tests
  start their own `postgres:16-alpine` through `internal/testdb`.
- **`github.com/golang-migrate/migrate/v4` v4.20.1 as a library, in tests only.** `internal/testdb`
  applies the real migration files (up, down, up) from Go, so tests need no `migrate` binary.
  The API does not import it; `make migrate-up` still uses the CLI.
- **sqlc v1.31.1, pgx/v5 mode, generated into `internal/store`.** Required timestamps are
  `time.Time`, nullable columns are pointers, JSONB is `json.RawMessage`.
- **`updated_at` is set by a trigger using `clock_timestamp()`.** A trigger so no hand-written
  statement can forget it; the wall clock rather than `now()` because `now()` is the
  transaction's start, which a late-committing transaction would carry past a poller. A poll
  must still ask from a few seconds before the last time it saw.
- **A seat with no category is published as `BLOCKED`, and a check forbids any other status
  while `category_id` is null.** The database, not each query, guarantees an unpriced seat is
  never sold.
- **`event_seats (event_id, block_id, category_id, status)` replaces the `(event_id, block_id,
  status)` index from `000001`.** It serves the block seat list by prefix and the availability
  counts as an index-only scan; every index on this table is rewritten on each status change,
  so the narrower one was dropped rather than kept beside it.
- **The seeder checks seat foreign keys once per load instead of once per row, when the role may
  set `session_replication_role`.** That cut the layout load from 16.5 s to 2.4 s on the dev machine; the
  parents are created in the same transaction so nothing can remove them meanwhile, and one
  query re-checks every reference before commit. Publish does not do this: its parent rows are
  already committed.
- **Venue template assignments are stored by block/row id and category name.** Ids are stable
  within a venue; categories are per event and do not exist until the template is applied.
- **An unset `APP_ENV` means production.** Review found that defaulting to development left
  every dev shortcut switched on wherever the variable was forgotten. Dev shortcuts now need an
  explicit `development` or `test`.
- **The seeder creates demo events and demo logins only when `APP_ENV` is `development` or
  `test` and the database host is local.** Two conditions, so neither a forgotten variable nor a
  production `DATABASE_URL` in a developer's shell can put public passwords on a real database.
- **Local migrations run with the `migrate/migrate:v4.17.1` image; tests use the v4.20.1
  library.** The image tag matches what the production compose file already runs, which is not
  ours to change.
- **Admin routing is `react-router-dom` 7.18.4.** The site needs protected routes, a fixed
  public `/invite/:token` path and "go back to the page you asked for" after sign-in; this is
  the standard router for a Vite React app and needs no framework.
- **Admin server data is `@tanstack/react-query` 5.104.1.** Already the fixed choice in
  `CLAUDE.md` for admin and mobile; it gives every screen its loading, error and retry states.
- **The two-step setup QR code is drawn with `qrcode-generator` 2.0.4.** An authenticator app
  needs a QR to scan; this library has no dependencies and only computes the modules, which
  the admin draws as SVG in the brand ink.
- **Shared-package tests run on `vitest` 3.2.7, not 5.x.** vitest 5 needs vite 6.4 or newer and
  the admin is on vite 5; a second vite in the hoisted `node_modules` made
  `@vitejs/plugin-react` resolve the wrong one. Move to vitest 5 when the admin's vite is raised.
- **The admin keeps the access token in memory and the refresh token in `sessionStorage`.**
  No long-lived token in `localStorage`: closing the tab ends the session, and each tab
  rotates its own refresh token so two tabs cannot trip the server's reuse check.
- **`ADMIN_API_PROXY_TARGET` can repoint the admin dev proxy.** Unset, `/v1` still goes to
  `http://localhost:8080`; it exists so the site can be run against a stub when 8080 is busy.
- **`github.com/go-playground/validator/v10` v10.30.5.** The validator named in `CLAUDE.md`.
  Used in one place, `httpapi.DecodeJSON`, which turns its errors into `VALIDATION_FAILED`
  with JSON field names.
- **`github.com/golang-jwt/jwt/v5` v5.3.1.** The JWT library named in `CLAUDE.md`. HS256 only:
  the parser is built with `WithValidMethods`, so `alg: none` and other algorithms are refused.
- **`github.com/getkin/kin-openapi` v0.149.0, in tests only.** `internal/httpapi/contracttest`
  loads `backend/docs/openapi.yaml` (it reads OpenAPI 3.1) and checks real answers against
  it. The API binary does not import it.
- **No CORS or rate-limit dependency.** Both are small enough to own: `httpapi.cors` and
  `internal/ratelimit` (fixed window, in memory, per process).
- **`APP_ENV` unset means production.** Dev shortcuts are on only when it is exactly
  `development` or `test` (`config.Base.AllowDevShortcuts`); nothing asks "is it not
  production". A server that lost its env file fails closed.
- **Forwarding headers are believed only from `TRUSTED_PROXY_CIDRS`.** chi's `RealIP` was
  removed: it trusts `X-Forwarded-For`, `X-Real-IP` and `True-Client-IP` from anyone, which
  lets a caller pick their own rate-limit bucket. `httpapi.ClientIPResolver` is the one
  function that reads them.
- **Per-IP limits are loose, per-phone and per-email limits are tight.** Kenyan mobile networks
  put thousands of phones behind one address; a tight per-IP limit would lock out a stadium
  of fans. SMS cost is capped per phone (one code a minute, five an hour).
- **Access tokens carry `sid` (the refresh-token family) beside `sub`, `role`, `iat`, `exp`,
  `jti`.** "Change password signs out my other sessions" needs to know which session is
  calling. The MFA token is signed with a key derived from `JWT_SECRET`, so it can never
  pass as an access token.
- **TOTP replay and the MFA token's attempt count are kept in memory.** A used code is refused
  for the rest of its window and an MFA token dies after five tries or one success, per
  process. A database column would make it survive restarts and work across instances; the
  password step stands in front either way.
- **Pool-wide `statement_timeout` and `idle_in_transaction_session_timeout` (15 s each) for the
  API.** Set through `store.OpenWithOptions`; `store.SetLocalStatementTimeout` raises the
  limit inside one transaction (publish). `store.Open` (seeder, worker) sets none.
- **`/v1/admin` and `/v1/gate` are sub-routers with a catch-all route.** Their auth and role
  middleware then runs for every path under the prefix, existing or not, so a fan gets 403
  rather than a 404 that maps the admin API.
