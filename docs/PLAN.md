# Build plan

Source of truth: `docs/ARCHITECTURE.md`, corrected by `CLAUDE.md`. The brief this plan answers
is `docs/KICKOFF_PROMPT.md`. Progress is tracked in `docs/STATUS.md`; choices in
`docs/DECISIONS.md`.

## Starting point

The repo is not empty. Before this plan it already had: a Go API with a health endpoint,
migration `000001` (all §4 tables, with corrections 1 and 3 already applied), a pnpm workspace
with an Expo shell, a React admin shell, `packages/shared`, CI, and a production deploy to
`talanta.vendorsoko.com`. The plan builds on that instead of starting over.

## Conventions every agent follows

These are fixed so that work done in separate folders fits together.

- **Branch:** all work is on `build/full-system`. Nothing is pushed: a push to `main` deploys.
- **Deployment is not ours to change:** `deploy/`, `.github/workflows/`, `backend/Dockerfile`
  and `mobile/eas.json` are never edited. Anything production needs is written up in
  `docs/STATUS.md` for the owner to apply.
- **API prefix:** every route is under `/v1` (the admin site shares the origin). `/healthz`
  is also at the root.
- **HTTP package:** `backend/internal/httpapi` (not `internal/http`, which would shadow
  `net/http`).
- **Migrations:** `000001` may already be applied in production, so it is never edited. New
  schema goes in `000002` and later, each with a working down file.
- **IDs:** `BIGINT` identity everywhere, sent as JSON numbers. Tickets are UUIDs.
- **Money:** integer cents, JSON field names end in `_cents`.
- **Times:** RFC 3339 UTC in JSON. Clients format in `Africa/Nairobi`.
- **Errors:** `{ "error": { "code", "message", "details" } }`, `code` is `SCREAMING_SNAKE`.
- **Colours:** the brand palette in `packages/shared/src/theme.ts` (Emerald Ink `#064E3B`,
  Champagne `#F8E7C9`) is not changed. New UI colours are derived tints or semantic states
  added beside it, never replacements.
- **API clients:** `make openapi-clients` runs `openapi-typescript` over
  `backend/docs/openapi.yaml` into `packages/shared/src/api/schema.d.ts`. Mobile and admin
  call the API through `openapi-fetch` typed by that file. No hand-written request types.
- **Tests:** integration tests carry the build tag `integration` and start Postgres 16 with
  testcontainers-go. `make test` runs unit tests; `make test-integration` runs both.
- **Dev defaults:** `APP_ENV=development` returns the OTP in the response and accepts the
  mock payment header. Both are refused when `APP_ENV=production`.

## Milestones, order and owners

Backend packages share one schema, so backend work runs one agent at a time. Mobile and admin
touch only their own folders and start as soon as the OpenAPI contract exists.

| # | Milestone | Owner | Reviewed by |
|---|---|---|---|
| M1 | Foundation: compose (db, migrate, api, worker, adminer), Makefile, config, logging (CI already exists and is left as is) | lead | `code-reviewer` |
| M2 | Schema corrections, sqlc, seeder (~60,000 seats, two events, one user per role) | `db-architect` | `code-reviewer` |
| M3 | OpenAPI contract for the whole API, error mapping, auth (OTP, password + TOTP, JWT) | `go-api-engineer` | `code-reviewer` |
| M4 | Events, categories, layout endpoints, publish | `go-api-engineer` | `code-reviewer` |
| M5 | Seat map levels 1–3 and the changed-since poll | `go-api-engineer` | `code-reviewer` |
| M6 | Holds, best available, sweeper, limits, concurrency tests | `booking-concurrency-engineer` | `code-reviewer` |
| M7 | Bookings, payments (mock + Daraja), tickets, SMS mock | `payments-engineer`, then `ticketing-gate-engineer` | `code-reviewer` |
| M8 | Mobile fan flow | `mobile-engineer` (starts after M3's contract) | `code-reviewer` |
| M9 | Admin web | `admin-web-engineer` (starts after M3's contract) | `code-reviewer` |
| M10 | Gate mode: manifest, scan upload (backend), offline scanning (mobile) | `ticketing-gate-engineer`, `mobile-engineer` | `code-reviewer` |
| M11 | Load test, end-to-end journeys, README, diagrams | `qa-engineer`, lead | `code-reviewer` |

Each milestone ends with: its "done when" lines run by the lead, a review, blocking findings
fixed, a line in `docs/STATUS.md`, and a commit.

## How the sub agents are run

The briefs in `.claude/agents/` were added in this session, so they are not registered as
agent types yet. Each task is given to a general agent that is told to read its brief and
`CLAUDE.md` first. The effect is the same; from the next session they can be called by name.

## Questions the documents did not answer

None block the work. Each was decided and recorded in `docs/DECISIONS.md`:

1. Whether `000001` can be rewritten (no: treated as applied).
2. How gate groups are named (stand code + tier, for example `N-LOWER`).
3. What the booking fee is (zero by default, set by `BOOKING_FEE_BPS`).
4. Which linter `make lint` runs (`gofmt` and `go vet`; staticcheck does not support Go 1.27 yet).
5. Which tool runs the load test (decided in M11 from what is installed).
