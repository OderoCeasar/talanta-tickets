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
  of fans. The per-phone limit (one code a minute, five an hour) protects one number; it does
  not cap SMS cost, because an attacker uses a new number each time. Cost is bounded by the
  country allow-list and the global hourly and daily caps (see below).
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
- **Mobile server data is `@tanstack/react-query` 5.104.1.** The fixed choice in `CLAUDE.md`, and the
  same version the admin uses so the hoisted workspace holds one copy.
- **Mobile local state is `zustand` 5.0.15.** The fixed choice in `CLAUDE.md`; it holds only the
  tapped seat IDs and the hold expiry.
- **`react-native-svg` 15.15.4 and `@shopify/react-native-skia` 2.6.2 draw the seat map.** The fixed
  choices in `CLAUDE.md` (§5.2: SVG up to about 1,200 seats, Skia above); versions are the ones
  `expo install` picks for SDK 57, and both ship in Expo Go.
- **`expo-secure-store` 57.0.4 holds the mobile tokens.** Access and refresh tokens go in the
  Android Keystore / iOS Keychain, never in plain storage.
- **`@react-native-async-storage/async-storage` 2.2.0 holds the offline copy of the tickets.** Also
  the signed-in user and the pointer used to resume a checkout. Nothing secret. An explicit
  cache of one answer was simpler to reason about than persisting the whole query cache.
- **`expo-brightness` 57.0.2 raises screen brightness on the ticket screen.** A dim screen is the
  usual reason a QR will not scan; the system level is restored on leaving.
- **`expo-crypto` 57.0.3 makes the checkout idempotency key.** `randomUUID()` from the platform's
  secure random source; Hermes has no `crypto.randomUUID`.
- **`qrcode-generator` 2.0.4 draws the ticket QR in the app.** Same library and version the admin
  uses; it only computes the modules, which the app draws with react-native-svg.
- **Mobile unit tests run on `vitest` 3.2.7.** The tested code is pure TypeScript (no React Native
  imports), so the runner the shared package already uses is enough and `jest-expo` is not needed.
- **Mobile shows Nairobi time with a fixed UTC+3 offset, not `Intl`.** Kenya has no daylight
  saving, and `Intl` time-zone support differs between Hermes builds; the fixed offset gives
  the same text on every phone and is unit-tested.
- **The mobile app creates the booking when checkout opens, not when Pay is tapped.** `Hold` has
  no fee, so only a booking can tell the fan the total before they are asked to pay it. The
  idempotency key is made once per hold and "Pay" resends it, so nothing is created twice.
- **Mobile `EXPO_PUBLIC_API_URL` is the origin without `/v1`.** That is what `eas.json` already sets
  for preview and production; the app appends the prefix.
- **TOTP codes at sign-in are counted per user, five per 15 minutes, in the rate limiter.**
  The per-token count was no limit: every good password minted a new token. Every code tried
  is counted before it is checked (so parallel guesses share the five) and a finished sign-in
  clears the count; once it is reached even the right code answers 429 until the window ends.
- **A good password does not clear the per-email login count when TOTP is on.** Only the TOTP
  step does, otherwise a known password is an unlimited supply of password steps.
- **`CLIENT_IP_HEADER` is empty by default.** Caddy passes `CF-Connecting-IP` through from
  whoever reaches the origin, so believing it let a caller choose their rate-limit bucket.
  The right-to-left `X-Forwarded-For` walk is the default; the header is opt-in for origins
  only the CDN can reach.
- **Production refuses an empty `TRUSTED_PROXY_CIDRS`; `none` means "exposed directly".**
  Empty behind a proxy puts every caller in one bucket without any sign of it, so the operator
  has to say which of the two set-ups this is.
- **At most `ARGON2_MAX_CONCURRENT` (4) argon2 runs at once, behind one semaphore in
  `auth.passwordHasher`.** Login needs no account, so uncapped it is 19 MiB and a core per
  request for anyone. Waiters queue on the request context and get 429 when it ends; there is
  no 503 code in the contract.
- **Sign-in SMS go only to `OTP_ALLOWED_COUNTRY_CODES` (default 254), with global caps of
  2,000 an hour and 10,000 a day.** International SMS is what pumping fraud bills; the caps
  bound the worst day, and reaching one is logged at error as the alert. The caps are counted
  last so a request refused for its own phone's limit does not use them up.
- **SMTP requires TLS (STARTTLS, or implicit on 465) except to loopback or with
  `SMTP_ALLOW_PLAINTEXT=true`.** "STARTTLS when offered" is downgraded by anyone in the path;
  a failed send does not fail the invitation.
- **The mock SMS and mailer never log a body in production, at any level.** Debug logging is
  switched on exactly when someone is investigating, which is when codes must not leak.
- **`ADMIN_BASE_URL` has no default in production and must be https.** The invite link is a
  one-time credential; a localhost or http link is a silent failure or a leak.
- **`auth.Cleanup` runs hourly in the worker as `auth-cleanup`.** It deletes sign-in codes
  older than 24 hours and refresh tokens expired more than 7 days; it is a plain function, not
  a `Service` method, because the worker has no JWT secret.
- **TOTP enrolment still does not ask for the password.** Asking would change the contract
  and the admin UI; it is listed in `docs/STATUS.md` as a known limit instead.
- **A published event's ON_SALE/PUBLISHED status is derived on every read, not flipped by a job.**
  Publish stores `ON_SALE` when now is inside `[sales_open_at, sales_close_at)` (no open time
  means open) and `PUBLISHED` otherwise; after that `domain.EffectiveEventStatus` works the
  status out from the stored value and the window, so nothing depends on a worker waking up at
  the right second. The hold service should ask the same function.
- **Publish and everything that edits what publish copies lock the event row `FOR UPDATE`.**
  Paint, category create/update/delete, event edit and cancel take the lock and then check the
  status, so `event_seats.category_id` can never differ from the painting. Publish also holds
  the venue row `FOR SHARE`; deleting or regenerating a block takes it `FOR NO KEY UPDATE` with
  a 3-second `lock_timeout` and answers 409 `BLOCK_HAS_INVENTORY` if a publish is in flight.
- **Publish has its own request timeout: the HTTP write timeout less five seconds (55 s by
  default).** It took 30.6 s and 44.2 s in two runs for 59,568 seats on the dev laptop, over both the 30 s request
  timeout and the 15 s statement timeout. `httpapi.routeTimeout` swaps the deadline for that
  one route and the transaction raises its own `statement_timeout`. No new env var.
- **The seat map marker is the database clock read before the select, and a poll looks back 20
  seconds before it.** `updated_at` is stamped at write time, so a transaction can commit a row
  older than a marker already handed out; 20 s is above the 15 s statement and
  idle-in-transaction limits that bound such a transaction. The server owns the overlap, the
  client only echoes the marker. Markers older than 5 minutes get the full block.
- **A category edit touches the event row, and a poll answers "full" when the event changed
  since the marker.** That is how a new price or colour reaches an open block without a
  per-category change stamp.
- **Availability counts are cached in process for 3 seconds per event, dropped on publish and
  cancel.** One aggregate (block × category × status) serves level 1, level 2, event and
  category counts and the sales dashboard; reading it fresh scans about 60,000 rows. Level 3
  is never cached.
- **A category price change needs `confirm: true` once the event has left DRAFT**, not only
  once a seat is held or sold: fans may already be looking at the old price. (Review rule; the
  contract text was changed to match.)
- **Seats in numbered rows are labelled `<row>-<seat>` (`12-14`).** `1214` could be row 1, 12
  or 121. Lettered rows keep `C14`.
- **Cancelling an event moves unpaid bookings to CANCELLED and paid ones to REFUND_REQUIRED, and
  frees held seats.** A payment that lands after the cancel then finds no held seat to sell
  (correction 5) and is refunded by the ordinary path. Refunds are queued with the payment's
  amount; `refunds.payment_id` is unique, so a payment is never queued twice.
- **Sales day buckets are Africa/Nairobi days.** The organiser's "today" starts at Nairobi
  midnight (21:00 UTC), not at 00:00 UTC.
- **Public reads are rate limited at 12,000 a minute per IP.** Loose for the same reason as the
  other per-IP limits: thousands of phones share an address, and each polls every 5 seconds.
- **No new Go dependency for M4/M5.**
- **Lock order, fixed for the whole system (M6).** Any transaction that takes more than one of
  these takes them in this order: (0) the event's *gate*, an advisory lock on the event id;
  (1) the event row; (2) the (user, event) advisory lock; (3) hold rows, in id order;
  (4) `event_seats` rows, in id order, and the hold's booking and payments. A hold's seats,
  booking and payments are only touched by a transaction that already has that hold's row
  lock, so their order among themselves does not matter. Holding seats, release, the payment
  extension and the sweeper take the event *shared*; publish, cancel and every edit take it
  exclusive (`LockEvent`). The comments on each statement are in `internal/store/holds.go`.
- **A hold takes the event row `FOR SHARE`, not `FOR KEY SHARE`.** Both conflict with the
  `FOR UPDATE` that cancel and publish take, so either would stop a cancel interleaving with a
  hold. But a plain `UPDATE events` (status, sales window) takes `FOR NO KEY UPDATE`, which
  `FOR KEY SHARE` lets through and `FOR SHARE` does not. With `FOR SHARE` no writer of the
  event row can run during a hold, whether or not it remembered to lock first. The status and
  the sales window are checked under that lock, by the database clock.
- **The event has a gate (an advisory lock) in front of its row lock.** Measured before it
  existed: a cancel issued during a 1,000-request hold storm got the row only when the storm
  was over (3.3 s, 4.1 s and 8.0 s in three runs, with every one of the 500 seats held first).
  Postgres lets a new `FOR SHARE` join the ones already on a row even while a `FOR UPDATE` is
  waiting, so a steady stream of holds starves the organiser. Advisory locks queue in arrival
  order. Holds take the gate shared and `LockEvent` takes it exclusive, both before the row
  lock, so a writer waits only for the holds already in flight (0.7 to 1.2 s in eleven runs of
  the same test under `-race`, the cancel's own work included, with 118 to 258 holds getting
  in before it and the rest refused).
- **The hold statement is two statements of one transaction: lock, then write.** §6.2 locks
  and writes in one statement. Here `SELECT … ORDER BY id FOR UPDATE` picks and locks the free
  seats, and the `UPDATE` of those same rows is the last statement before commit. A seat's
  `updated_at` is stamped when the row is written and the seat map's poll looks back 20
  seconds, so nothing that might wait may come between that write and the commit. The same
  rule holds for release, the sweeper and selling: the `event_seats` write is last.
- **A lapsed hold is closed by whoever takes its last seat.** The hold statement reclaims seats
  of a hold whose time has run out (no sweeper needed). The old hold rows are locked *before*
  the seats (lock order), and one that has no seat left afterwards becomes `EXPIRED` with its
  unpaid booking. One that still has seats stays `ACTIVE` in the table until they go too or
  the sweeper closes it; every read reports it as `EXPIRED` (`domain.EffectiveHoldStatus`).
- **The per-user ticket limit counts seats, once each:** seats held by the user's live holds,
  seats of `PAID` bookings, and seats of a `PENDING_PAYMENT` booking that has a payment still
  in flight (so a hold that lapses while M-Pesa is deciding keeps counting). An unpaid booking
  with no live payment and no live hold does not count: its seats are back on sale.
- **Best available reads without locking and lets the hold statement decide (correction 2).**
  "Best" is: the block nearest the middle of its stand (lower tier first on a tie), in it the
  row nearest the pitch, in it the run nearest the middle of the row. Blocks are read a few
  at a time, best first (1, then 3, then 11 …), and the search stops when it has enough runs,
  so the usual request reads one block. The category's blocks come from the event's painting,
  which is frozen at publish, not from a count over the inventory. No new index was needed
  (no migration 000008): the plans are in the M6 report and asserted by
  `TestSeededVenue_HoldQueriesDoNotScanTheInventoryTable`.
- **Best available tries six runs, and spreads out after a lost race.** The first try takes
  the best run. Each later try picks at random among a growing window of the best runs (8,
  32, 128, 512, 2,048): if every loser went for the next best run, a crowd would collide
  again and again with one winner a round. Then it answers `NO_ADJACENT_SEATS`.
- **Best available hands out STANDARD seats.** A run may include `RESTRICTED_VIEW` seats only
  when the category has no run of standard seats at all. `ACCESSIBLE` and `COMPANION` seats
  are never handed out by best available; they can be picked by hand on the map. A run never
  crosses a gap: a seat that is taken, blocked or switched off, a seat of an excluded type,
  the end of a row, or an aisle (the edge of a block).
- **A hold is extended for a payment only when it needs it, and then only once.** With six
  minutes or more left nothing changes and the one extension is kept for a later attempt.
  Otherwise the expiry moves to now + 6 minutes on the hold and on its seats and
  `holds.extended_at` is set. A second call changes nothing and says so.
- **Selling a hold's seats is all or nothing in one statement.** `store.SellSeatsOfHold` flips
  seats to `SOLD` only where `hold_id` matches and the status is still `HELD`, and only if as
  many as the booking expects are still there; otherwise it flips none and returns the short
  count (correction 5). A sold seat keeps its `hold_id` as the record of which hold bought it.
- **The sweeper is driven from `holds`, takes each event shared with `SKIP LOCKED`, and runs
  under an advisory lock.** It never waits: a hold a release or payment is working on, and an
  event an organiser is editing, are left for the next run. A second sweeper leaves at once.
  It retries a batch once on SQLSTATE 40P01, which the lock order is meant to make impossible.
- **The worker's pool has the API's session limits (15 s statement, 15 s idle in
  transaction).** The sweeper locks hold and seat rows; a stuck one must not sit on them. The
  values are constants in `cmd/worker/main.go` because `config.Worker` has no fields for them.
- **Holds never write the `events` row.** Any write to it makes every poll of every block
  answer with the full block for 20 seconds, so nothing on the hold path flips `SOLD_OUT`.
- **Hold creation is limited to 20 a minute per user and 3,000 a minute per IP**
  (`RateLimits.HoldPerUser`, `HoldPerIP`), `POST /holds` and best available counted together.
  Reading and releasing a hold are not limited: a fan must always be able to give seats back.
- **A hold shows the fee and total (`fee_cents`, `total_cents`), as an estimate.** The fee is
  `BOOKING_FEE_BPS` basis points of the subtotal, whole cents, rounded down, computed by
  `domain.BookingFeeCents`, the one function the booking code must also use.
- **`internal/testinvariants` holds the inventory invariant checker.** Ordinary Go, no build
  tag, read-only, so any test package can call `testinvariants.Check(t, db, eventID)`.
- **No new Go dependency for M6.**
- **Admin unit tests run on `vitest` 3.2.7.** The same runner and version the shared package and
  the mobile app already use (no new package in the lockfile); it covers the admin's pure logic:
  shillings to cents, Nairobi time, drag-box hit-testing and the heatmap colour scale.
- **No chart or map library in the admin.** The sales chart, the heatmap and the layout editor are
  plain SVG: one path per block (96), seats drawn only for one open block.
- **The admin asks for hourly sales only for the last 48 hours.** The API refuses more than 2,000
  buckets and its default range starts at the first paid booking; day buckets use the default.
- **The admin switches off editing while its own publish request is in flight.** A write that
  waits on the publish lock can answer 500 after about 15 seconds; the site tracks the publish
  with a TanStack mutation key and disables event edits and block edits until it answers.
- **Nothing waits long for an event's lock: 409 `EVENT_BUSY` (review fix 1).** Publish holds
  the event's gate and row for 30 to 44 s on the full venue, and cancel for 10 to 16 s on a
  sold-out one. Everything else that takes that lock (paint, category create/edit/delete,
  event edit and delete, cancel, a second publish) waited under the 15 s statement timeout
  and answered 500. Each now sets `lock_timeout` for the one locking statement and answers
  `EVENT_BUSY` with nothing changed: 5 s for staff calls (`events.Policy.LockWait`), 3 s for
  a fan's hold (`booking.Policy.EventLockWait`) and for the layout editor
  (`venue.DefaultLockWait`). 5 s because a writer legitimately waits for the holds in flight
  when it reaches the gate (0.7 to 1.2 s in the worst storm measured). This replaces "the
  admin switches off editing while its own publish is in flight" as the safety net; that UI
  rule can stay as a courtesy.
- **The lock wait is set for one statement and lifted again; the SQL is in `internal/store`.**
  `store.SetLocalLockTimeout` and the `…Within` functions in `store/locks.go` (which also
  took over the two raw `set_config` calls that were in `internal/venue`). The limit is put
  back to the session's own before the rest of the transaction runs, so waits for hold and
  seat rows behave exactly as before; `store.IsLockTimeout` recognises SQLSTATE 55P03.
- **A hold on a busy event answers `EVENT_BUSY`, not a not-on-sale error.** `EVENT_NOT_ON_SALE`
  tells the app to stop offering seats; busy tells it to try again in a moment, which is the
  truth (an organiser's price change or a publish is in progress). A hold aimed at a draft
  that is mid-publish gets the same answer instead of 404: reading the event before the gate
  to tell the two apart would add a query to every hold for a case no real client reaches.
  The changes in `internal/booking` are `admit` (now a method, for the policy) calling
  `store.LockEventForHoldWithin`, and `Release` doing the same through
  `LockEventOfHoldWithin` (a release queued behind a 16 s cancel would otherwise meet the
  15 s statement timeout). `ExtendForPayment` still waits unbounded: it runs inside the
  payment transaction of M7, which should decide its own answer for a busy event.
- **A block edit locks the block `FOR NO KEY UPDATE`.** A publish holds `FOR KEY SHARE` on
  every block it copies seats of (the `event_seats.block_id` foreign key), which `FOR UPDATE`
  conflicts with and `FOR NO KEY UPDATE` does not; a move changes no key. Changing the code
  does (`UNIQUE (stand_id, code)`), so that `UPDATE` waits and `UpdateBlock` runs under the
  3 s lock wait. Delete and regenerate keep `FOR UPDATE` behind the venue lock.
- **An edit after publish is checked against what publish required (review fix 2).**
  `domain.PublishedEventProblems` on the merged row: both ends of the sales window set, in
  order, not after kickoff; and the kickoff not moved into the past. "Kickoff in the past"
  is only refused when the edit moves the kickoff: an event that has been played must stay
  editable. A category of a non-draft event must cost more than zero on edit AND on create
  (create was not in the review; without it an edit of such a category would be refused for
  a price the caller did not send).
- **Cancel has publish's timeouts (review fix 3).** Measured on the seeded venue sold out
  (57,568 tickets, 14,392 paid bookings with one payment each, 1,000 live holds): 16.0 s and
  10.7 s through the API on the dev laptop; the largest statements were queueing refunds
  (4.5 s) and voiding tickets (4.0 s). That is inside the ordinary limits on an idle
  machine (15 s a statement, 30 s a request) but with less than twice the room on the
  request limit, on hardware where publish varied by half between runs. A cancel that
  rolls back at 30 s on match-day load is the wrong failure, so it runs under
  `routeTimeout` and `SetLocalStatementTimeout` (`Policy.CancelTimeout`, the same 55 s).
  Still one transaction; a repeat is a no-op in about 60 ms.
- **The default sales call always succeeds (review fix 4).** No `bucket`: hours for a range
  up to 48 h, days beyond. No `from`: the first paid booking, moved forward to the most
  recent 2,000 buckets when needed. Only a `from` the caller sent can be refused. The
  `default: hour` on `SalesBucket` is gone from the contract because it is no longer true.
- **A block has at most 3,000 seats (review fix 5).** `seatmap.MaxSeatsPerBlock`, checked in
  `Validate` so previews and moves are covered too; the real maximum is 1,312. The error is
  on both `rows` and `seats_per_row` with rule `max_seats`.
- **The event lists accept counts up to 20 s old (review fix 9).** `Policy.ListCountsTTL`.
  One cache entry per event serves both limits: a list never forces a load that a seat map
  would not, and a seat map's 3-second refresh also freshens the list. Publish and cancel
  still drop the entry.
- **`CATEGORY_IN_USE.details.seats` counts every inventory row of the category.** Named
  `seats`, not `seats_on_sale`: a blocked seat still points at its category and still stops
  the delete. `CONFIRMATION_REQUIRED` keeps `{ sold, held }`.
- **Request bodies in the contract declare no `default`.** openapi-typescript turns a
  defaulted property into a required field; the default is stated in the description
  instead. A test keeps it so for every request body (`CreateEvent.max_tickets_per_user`
  and `CreateBlock.rotation_deg` had the same fault and were fixed with the reported ones).
- **`DELETE /admin/events/{id}` (drafts only, under the event lock) and `DELETE
  /admin/venues/{venueId}/templates/{templateId}`.** A cancelled draft is not deletable:
  the rule is "DRAFT", not "never published", so there is one status to check.
- **No new Go dependency and no migration 000008 for the M5 review fixes.**
- **Ticket QR text is `TLT1.<payload>.<signature>` with a fixed 51-byte binary payload (M7).**
  Version (1), key number (2), ticket UUID (16), event ID (8), gate group (16, ASCII, zero
  padded), nonce (8); integers big-endian. Always 160 characters, QR version 9 at level M
  (53 x 53 modules). The whole layout, the verification order and a worked example are in
  `docs/TICKET_FORMAT.md`, whose table and example are regenerated from the code by a test
  that fails when they differ. §9.1 put only the ticket ID, event and nonce in the payload;
  the gate group is added because correction 8 has the device check it offline.
- **The signature is Ed25519 over `"talanta.ticket.v1\0" || payload`, not over the bare
  payload.** A label in front means a signature this key ever makes for another purpose
  cannot be passed off as a ticket, and a ticket signature is useless elsewhere.
- **Token decoding is strict and canonical.** Only the base64url alphabet, no padding, and
  the unused bits of a last character must be zero. The 86th signature character carries two
  bits of it: a lenient decoder accepts 16 spellings of one signature, and Go's decoder also
  skips line breaks. Either way one ticket would have several valid texts.
- **Key IDs are `k<number>`, 1 to 65535, carried as two bytes.** A free-text key ID would
  need a variable or padded field; a number is enough to rotate with and keeps the payload
  at 51 bytes (68 base64 characters with no bits left over). `k0` is reserved for the
  ephemeral development key, which a configured server refuses to trust.
- **A ticket's signed text is stored (`tickets.qr_token`, migration 000008), not signed again
  on every read.** Recomputing needs the private key of whatever key signed the ticket, so
  after a rotation the server would have to keep every retired private key for as long as
  one of its tickets exists; then nothing is ever really retired, and a leak of the key file
  is a leak of every key there ever was. Stored, a retired key leaves only its public half
  (`TICKET_VERIFY_KEYS`), old tickets are still shown byte for byte as issued, and a read
  costs no signature. The cost is about 160 bytes a ticket (10 MB for a full stadium) and
  that the database holds the tokens, which it already does in effect: it holds every
  `web_token`, and whoever can read the table can read who sits where.
- **The signing key never sits in a struct field.** `ticketing.Signer` keeps it in a closure,
  so `%v`, `%+v`, `%#v`, slog and JSON of a signer, or of anything holding one in any kind of
  field, cannot print it (fmt prints an unexported nested field without calling its
  `String`). It is read by `ticketing.LoadKeyring` straight from the environment and is not
  part of `config.API`, which is printed and logged.
- **Production refuses to start without `TICKET_SIGNING_KEY`, with a malformed one, or with
  the test key published in `docs/TICKET_FORMAT.md`** (as signer or as a trusted verify
  key). Development and test with no key get a random one per process, key ID `k0`, and a
  loud warning.
- **Tickets are issued after the seats are sold, inside the same transaction; the precondition
  is "booking PAID, every item's seat SOLD through the booking's own hold, event not
  cancelled".** `ticketing.IssueForBooking` checks it under the booking's row lock and
  refuses otherwise, so it cannot hand out tickets for seats that were not bought. This puts
  a few statements after the `event_seats` write, which M6 asked to be last (the seat map
  poll goes by the time of that write). It is safe because issue waits for nothing: every
  row it locks (the event, the booking) the payment transaction already holds, and
  `TestIssue_DoesNotWaitBehindAQueuedCancel` proves the re-lock does not queue. Measured: 61
  ms for a 50-seat booking, begin to commit. The other order (issue first, sell after) would
  mean undoing an issue whenever the sale came up short (correction 5).
- **Issue is idempotent by an explicit check under the booking's row lock, with the
  `tickets_live_seat_idx` unique index behind it.** A booking that has any ticket, in any
  status, gets those back and nothing is written; two calls at once take turns. Without the
  check the second call would be a unique violation in the middle of a payment.
- **Issue runs under a savepoint.** A refusal (including the unique violation, which would
  otherwise abort the whole transaction) leaves the caller's transaction usable, so the
  payment flow can mark the booking `REFUND_REQUIRED` and commit that.
- **Issue re-takes the event row `FOR SHARE` and refuses a cancelled event.** A caller that
  followed the lock order already has the row and the statement costs nothing. One that did
  not could otherwise insert a ticket while a cancel was past its void statement, leaving a
  valid ticket for an event that is off.
- **One rule decides which tickets can be voided: `domain.TransitionTicket`.** VALID and USED
  may become VOID; nothing leaves VOID. Event cancel's `VoidTicketsOfEvent` hard-coded the
  two statuses; it and the new `VoidTicketsOfBooking` now take them from
  `domain.TicketStatusesThatCanBecome(VOID)`.
- **`GET /me/tickets` pages by (kickoff, event, inventory seat id, ticket id).** "Then seat"
  in the contract is the inventory id, which follows block, row and seat number; the ticket
  id breaks the tie between a void ticket and its replacement on one seat.
- **The web ticket page has its own route group and reads no access token.** Under
  `OptionalAuth` a stale `Authorization` header would turn the page into a JSON 401. Every
  answer of the page is HTML, including 429 and 500, and it has its own per-IP limit
  (`RateLimits.WebTicketPerIP`, 3,000 a minute: a stand of fans shares an address).
- **The web ticket page loads nothing and runs nothing.** `default-src 'none'`, images only
  as `data:` URLs (the QR codes are inline PNGs), one style block allowed by its SHA-256,
  no script. `Referrer-Policy: no-referrer` because the page's address is the secret. A QR
  is drawn only for a `VALID` ticket; unknown link, malformed link and a booking with no
  tickets get the same 404 page byte for byte.
- **`bookings.web_token` stays as migration 000005 made it: 32 bytes from `gen_random_bytes`
  (pgcrypto's CSPRNG), hex, unique.** Checked for this milestone: it is generated by the
  database on insert, never derived from the reference or the id, and never logged (the
  request log records the route pattern, not the path).
- **The ticket SMS goes to the phone of the fan who made the booking, names no event, and is
  sent after commit.** The link alone is 100 characters with the production origin, so the
  text is "Talanta: you're going! Booking TLT-XXXXXX, 2 tickets: <link>" (154 GSM
  characters) and falls back to shorter wordings for a longer `PUBLIC_BASE_URL`. An event
  title could be long or hold a character outside the GSM alphabet, which makes the whole
  SMS UCS-2 with a 70-character limit. `SendTicketSMS` returns nothing: a failed send is
  logged (with the link cut out of the provider's error) and can never fail a payment.
- **`PUBLIC_BASE_URL` is required and https in production, like `ADMIN_BASE_URL`.** The SMS
  link is a bearer secret for the tickets; a localhost or http link is a silent failure or
  a leak.
- **`github.com/skip2/go-qrcode` v0.0.0-20200617195104-da1b6568686e draws the QR PNGs.** The
  library named in ARCHITECTURE §2; it has no tagged release, so the pseudo-version of its
  newest commit is pinned. No dependencies of its own. Level M, four-module quiet zone,
  512 px by default (1.4 KB).
- **`github.com/makiuchi-d/gozxing` v0.1.1, in tests only.** A pure-Go port of ZXing that
  shares no code with the library that draws: the tests decode the PNG the API serves (and
  the images on the web page) and require exactly `qr_token` back. The API binary does not
  import it.
- **`contracttest` reads `text/html` and `image/png` bodies as strings.** kin-openapi has no
  decoder for either, and the web ticket page and the QR image are in the contract.
- **Bookings and payments (M7): one function finishes a payment, in one transaction.**
  `payments.Service.Finish` is where the M-Pesa callback, a repeat of it, the mock provider
  and reconciliation all end. It locks, in the system's order, the event (shared), the
  booking's hold, the booking and the payment, and then reads the payment's status: a
  second final state is refused by `domain.TransitionPayment`, which is what makes a
  duplicate callback, and a callback racing a reconciliation run, change nothing.
- **LOCK ORDER, extended for M7.** After (3) the hold row: (4) the booking row, then the
  payment row, then a refund row; `event_seats` is still written last, followed only by
  ticket issue. Every transaction that touches a booking, its payments or its refunds
  first takes the event shared and the booking's hold. Two statements take one payment row
  with nothing else and wait for nothing afterwards, so they cannot be part of a deadlock:
  recording that the provider accepted a request (`store.LockPaymentAlone`) and
  reconciliation's claim (`store.ClaimPaymentsForReconcile`, `FOR UPDATE SKIP LOCKED`).
  The comments are in `internal/store/payments.go`.
- **The provider is called outside any transaction.** Starting a payment commits its row
  (`INITIATED`, with the hold extended) and only then calls M-Pesa. Called inside, a slow
  Safaricom would keep the event's share lock, and every organiser edit, cancel and hold
  queued behind it would wait on Safaricom. The price is a window in which the row exists
  and the request has not been sent; reconciliation closes such a row after 60 seconds.
- **A booking's reference is `TLT-` and six of `ABCDEFGHJKMNPQRSTUVWXYZ23456789`, from
  crypto/rand.** No 0/O/1/I/L: a reference is read out over the phone and typed at a gate.
  About 887 million values; a collision is retried under a savepoint.
- **One hold, one live booking, decided at the hold's row lock.** Two `POST /bookings` for
  one hold take turns there, so the second finds the first's booking whichever key it
  carries: the same key replays it (200), another key answers `HOLD_ALREADY_BOOKED`. The
  `bookings_live_hold_key` and `(user_id, idempotency_key)` unique indexes stand behind
  that and are answered the same way, never as a 500. A replay is looked up before any
  lock, so it works when the event is busy or the hold has ended.
- **A payment needs 2 minutes left on its hold (`payments.Policy.MinHoldRemaining`), not
  6.** The first payment pushes the hold out to 6 minutes (correction 4). A hold is extended
  once, so a retry after a wrong PIN finds less; requiring 6 again would refuse every
  retry. Two minutes covers one PIN prompt, which M-Pesa itself gives up on after about a
  minute. A result that arrives after the hold has run out is still handled correctly (see
  below), so the number protects the fan's time, not the inventory.
- **A successful payment sells through the hold's STORED status (correction 5).** `ACTIVE`,
  even past its expiry: the seat statement (`store.SellSeatsOfHold`, `hold_id` matches and
  status still `HELD`) finds out whether the hold still has every seat, and sells all or
  none. `EXPIRED` or `RELEASED`: the seats went back on sale; they are not taken back, even
  if they happen to be free, because that would be a second hold made without the hold
  rules. So a late payment for a booking the sweeper already expired is refunded.
- **PAID and CONVERTED are written under a savepoint and undone when the sale comes up
  short.** The order inside is payment, booking, hold, then the seats, then the tickets at
  once. `Sold != n` rolls the savepoint back and the payment is recorded with a refund. So
  does a refusal from `ticketing.IssueForBooking` (the seats sold are not exactly the
  booking's): two independent guards, and breaking either one alone leaves the tests green.
- **Wrong amount: payment `FAILED` with `failure_reason: wrong_amount`, booking
  `REFUND_REQUIRED`.** §8.2 says "FAILED + refund queue" and the contract's `BookingStatus`
  says the booking is `REFUND_REQUIRED`; both hold. No new column: a `FAILED` payment whose
  `result_code` is 0 (M-Pesa said paid, we refused) is the wrong-amount case, and
  `domain.PaymentFailureReason` reads it that way. The refund is for what was actually
  paid, and the alert kind is `AMOUNT_MISMATCH`.
- **`TIMEOUT` is our verdict, so M-Pesa can overrule it.** `TIMEOUT -> SUCCESS` and
  `TIMEOUT -> FAILED` are legal; `SUCCESS` and `FAILED` are final. Otherwise money that
  arrived after we stopped waiting could not be recorded. A second `SUCCESS` for a booking
  another payment already paid is recorded and refunded on its own: the booking stays
  `PAID` and its tickets are not touched.
- **`CANCELLED`, `EXPIRED` and `FAILED` bookings may become `REFUND_REQUIRED`; `REFUNDED`
  may too.** A late success has to be recordable on a booking we had given up on, and a
  booking whose refunds were all paid can receive yet another payment. `PAID` has exactly
  one way out (`REFUND_REQUIRED`, on event cancel). Booking status `FAILED` is not written
  by anything: a booking whose payment failed stays `PENDING_PAYMENT` while its hold lives
  and becomes `EXPIRED` when the hold is gone, the same as one that was never paid for.
- **Status writes ask the transition rule with the row's real status.** The hold flow
  passed constants (`TransitionHold(ACTIVE, EXPIRED)`), which can never refuse. Bookings
  and payments are now read under their row lock in whatever status they have
  (`store.LockBookingsOfHolds`, `LockBookingsOfEvent`, `LockBooking`, `LockPayment`) and
  `domain.TransitionBooking` / `TransitionPayment` are asked with that. A booking the rule
  will not move (`PAID`, `REFUND_REQUIRED`) is left alone by a release, a sweep or a
  take-over instead of being filtered out by a WHERE clause. Event cancel asks once per
  status actually present among the event's bookings, which costs one extra pass over rows
  it rewrites anyway.
- **The callback is believed only as far as it can be checked.** Matched by
  `CheckoutRequestID` to a payment that is still open; the `Amount` must equal the booking
  total; the source address must be in `MPESA_CALLBACK_ALLOWED_CIDRS` (resolved by
  `httpapi.ClientIP`, so a forged `X-Forwarded-For` does not help). A "paid" with no
  readable amount is not applied: the provider is asked (STK query, authenticated) and that
  answer is used. A receipt already recorded on another payment is treated the same way
  (a replay). Daraja's STK query names no amount; a query that says "paid" is taken for the
  amount we asked for, because an STK prompt cannot be paid in part.
- **The webhook answers before it works.** It checks the address and the envelope, hands
  the callback to a bounded background runner (`payments.Background`, 256 at once, tied to
  shutdown) and answers 200. Work that cannot start, or that fails (the event is locked by
  a cancel), is left to reconciliation: the callback is an optimisation, never the only way
  a payment gets finished. The ticket SMS runs on the same runner, after the commit.
- **Reconciliation claims payments with a lease.** `FOR UPDATE SKIP LOCKED` plus a
  `last_queried_at` stamp written by the same statement: two workers never ask about one
  payment, and a payment a callback is finishing is skipped. `query_attempts` counts the
  claims. After 5 minutes without a final answer the payment is `TIMEOUT` and the
  organiser gets a `PAYMENT_UNRESOLVED` alert, because money may have moved.
- **The mock provider keeps no state.** Its checkout request ID carries the outcome, the
  amount and the time, so the worker (another process) answers a status query for a
  payment the API started. It is refused twice in production: `config.LoadPayments` will
  not select it and `mock.New` will not build it.
- **Production needs M-Pesa fully configured or does not start.** `PAYMENT_PROVIDER`
  defaults to `mpesa` in production and `mock` in development and test. With `mpesa`, a
  missing `MPESA_*` variable is a startup error in every environment: nothing falls back to
  the mock. The two secrets are `config.Secret`, which prints as `[redacted]` in fmt, slog
  and JSON, because `config.API` is printed.
- **The Daraja client retries network errors only, and an STK push only when the
  connection could not be made.** A push that timed out after it was sent may have reached
  Daraja; sending it again would put a second prompt on the fan's phone. The token and the
  query are safe to repeat and are retried on any network error (twice). A call that got
  any HTTP response is never retried. Amounts go to Daraja in whole shillings and a
  booking total that is not a multiple of 100 cents is refused, not rounded.
- **Completing a refund never touches seats.** A `REFUND_REQUIRED` booking of a live event
  owns no sold seat (it got that way because its payment could not sell), and a cancelled
  event's sold seats stay `SOLD` on purpose. There is no operation that refunds one paid
  booking of a live event; adding one needs a rule for putting its seats back on sale,
  which does not exist. `SOLD -> AVAILABLE` is still not a legal seat move.
- **The sales summary and the payment flow do not share a cache key.** A payment does not
  drop the events service's 3-second counts cache; the dashboard was already documented as
  a few seconds behind.
- **`internal/testinvariants` has two sets of rules.** `Check` (inventory) is unchanged, so
  the earlier tests that write `SOLD` seats and bookings straight into the tables as
  fixtures keep passing. `CheckLedger` adds the payment rules (a sold seat has one paid
  booking item, a converted hold and one live ticket; a paid booking has its money and its
  tickets; money that bought nothing has a refund) and every payment test ends with it. A
  test proves each ledger rule fires on a deliberately corrupt state.
- **No new Go dependency and no migration 000009 for M7 bookings and payments.**
- **Fonts are bundled, not fetched from Google at run time.** The app must render with no
  network, and the admin site makes no third-party requests.
