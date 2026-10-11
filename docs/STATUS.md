# Status

One line each. Updated at the end of every milestone.

## Done

- M1 Foundation: `make dev` brings up db, migrate, api, worker, adminer; `make test` and `make lint` pass. CI workflow left as it was (deployment files are off limits).
- M2 Schema and seed: migrations 000002–000006 go up/down/up; seeder makes 59,580 seats (layout 2.8 s) and inserts 0 rows on a second run.
- API contract: `backend/docs/openapi.yaml` (72 operations), lints clean, TypeScript types generate.

## Partly done

- M3 Auth, staff invitations and first-admin bootstrap: in progress.
- M9 Admin web: login, Team (invite) and set-password pages in progress; the rest not started.

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
- staticcheck is not run: its newest release cannot type-check Go 1.27.

## For the owner to do in production (not done by this build)

- Migration 000005 adds NOT NULL columns to `tickets` and `scan_logs`; it fails if production already has rows in those tables.
- The worker (`cmd/worker`: hold sweeper, payment reconciliation) is not in the production image or compose file. Until it is, expired holds are still reclaimed at hold time, but unpaid bookings are not expired and payments are not reconciled.
- New env vars for `/opt/talanta/.env` will be listed here as each milestone adds them.
