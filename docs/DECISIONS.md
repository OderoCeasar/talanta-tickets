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
