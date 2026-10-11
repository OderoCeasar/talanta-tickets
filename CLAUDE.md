# Talanta Stadium Ticketing — standing rules

These rules apply to the lead agent and every sub agent, in every session.
The full design is in `docs/ARCHITECTURE.md`. Where this file disagrees with it, this file wins.

## What we are building

A ticketing system for Talanta Stadium, Nairobi: fans pick seats on a map and pay with M-Pesa
in an Expo app; organisers run events from a React admin site; gate staff scan tickets, even
offline. One Go backend and one Postgres database serve all three.

The two parts that must be excellent are seat locking (§6) and the seat map (§5).
Everything else should be plain, correct and boring.

## Stack (fixed)

- Backend: Go, chi, pgx v5, sqlc, golang-migrate, slog, validator v10, golang-jwt v5.
- Database: PostgreSQL 16.
- Tests: testify and testcontainers-go against real Postgres. No mocked database.
- Mobile: Expo, TypeScript, expo-router, TanStack Query, zustand, react-native-svg, Skia,
  gesture-handler, reanimated.
- Admin: React, Vite, TypeScript, TanStack Query.
- No GORM. No new dependency without a line in `docs/DECISIONS.md` saying why.
- Check the current stable version of each tool before pinning it. Pin exact versions.

## Corrections to the architecture document

Apply these. They fix real problems in the design.

1. **`event_seats` needs `block_id`.** §4 indexes `(event_id, block_id, status)` but the
   table has no such column. Add `block_id` (copied from the seat at publish time) so the
   block seat query and availability counts do not need a join.
2. **The best-available query in §6.4 will not run.** Postgres does not allow `FOR UPDATE`
   with `GROUP BY`. Do it in two steps: find runs of neighbouring free seats with a plain
   read, then try to hold the first run using the normal hold statement. If someone else got
   there first, try the next run. Give up after a few tries with a clear answer.
3. **`REFUND_REQUIRED` is missing from the booking statuses.** §8.2 uses it. Add it.
4. **A payment can outlive its hold.** A hold lasts 10 minutes and payment checks run for up
   to 5. When a payment starts, push the hold's expiry out once so at least 6 minutes remain.
   Never extend it a second time.
5. **Selling seats must check the hold.** When a payment succeeds, flip seats to SOLD only
   where `hold_id` matches and status is still HELD. If the count is short, sell nothing,
   mark the booking `REFUND_REQUIRED`, and alert the organiser.
6. **The per-user ticket limit can be raced.** Two hold requests from one user at the same
   moment can both pass the count. Take a Postgres advisory lock on (user, event) inside the
   hold transaction before counting.
7. **The sweeper must close the hold too.** When it frees seats, set the matching `holds`
   row to EXPIRED and any `PENDING_PAYMENT` booking with no live payment to EXPIRED.
8. **Tickets are gate-bound, so scans need a `WRONG_GATE` result.** Add it to `scan_logs`.
9. **Seat changes need a change marker.** The polling endpoint in §7 returns "changed since".
   Add `updated_at` to `event_seats`, set on every status change, and index it per block.

## Rules that never bend

- Money is integer cents in KES. No floats anywhere near money.
- Times are stored as `TIMESTAMPTZ` in UTC and shown in `Africa/Nairobi`.
- A seat is never sold twice. If the choice is between a wrong ledger and a double sale, take
  the wrong ledger and flag it for refund.
- No silent partial bookings. If any requested seat is gone, hold none and return 409 with
  the lost seat IDs.
- Prices are copied onto `booking_items` at booking time and never change afterwards.
- "Payment success + seats SOLD + tickets issued" happens in one database transaction.
- The M-Pesa callback body is never trusted alone. Match it to a known pending payment and
  check the amount.
- Signing keys, Daraja credentials and JWT secrets come from env vars. Nothing secret in git.
  Commit a `.env.example` with fake values.
- The ticket signing private key never leaves the server. Gate devices get the public key.
- Dev-only shortcuts (OTP in the response, mock payment headers) are refused when
  `APP_ENV=production`, and a test proves it.

## How to write the code

- `internal/domain` holds types and state rules with no database or HTTP code.
- Handlers are thin: decode, validate, call a service, encode. Business rules live in
  services. SQL lives in `internal/store`.
- Every query that locks rows is hand-written SQL with a comment saying what it locks and why.
- State changes go through one function per entity that refuses illegal moves
  (for example SOLD back to AVAILABLE).
- Errors: wrap with context, map to HTTP codes in one place, return a stable JSON shape
  `{ "error": { "code", "message", "details" } }`.
- Pass `context.Context` through every call. Set timeouts on outside calls and DB work.
- The OpenAPI file is the contract. Change it first, then the handler, then regenerate the
  clients. Mobile and admin never hand-write API types.
- Mobile: server data lives in TanStack Query. zustand holds only the current selection and
  the countdown. Do not copy server data into zustand.
- Comments explain why, not what. No commented-out code. No TODO without an entry in
  `docs/STATUS.md`.

## Testing

- Anything touching holds, bookings or payments gets an integration test on real Postgres.
- Concurrency tests use many goroutines and real parallel transactions, run with `-race`,
  and are run several times in a row to catch flakes.
- A new test is shown to fail once before it is trusted: break the thing it guards, see red,
  restore it.
- Tests set up their own data and do not depend on the order they run in.

## Reporting

- Say what you ran and what it printed. Give counts, not adjectives.
- If you did not run it, say "not run" and why.
- If part of a task was left out, say so in the first lines of your report, not the last.
- Do not report a bug as open if you fixed it, or as fixed if you only changed the code
  without running the check.

## Useful commands (create these in the Makefile)

`make dev` · `make test` · `make test-integration` · `make lint` · `make migrate-up` ·
`make migrate-down` · `make seed` · `make sqlc` · `make openapi-clients` · `make loadtest`
