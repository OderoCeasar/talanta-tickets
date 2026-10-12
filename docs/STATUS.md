# Status

One line each. Updated at the end of every milestone. Test counts are in `docs/TEST_REPORT.md`.

## Done

- M1 Foundation: `make dev` brings up db, migrate, api, worker, adminer; `make test` and `make lint` pass. CI workflow left as it was (deployment files are off limits).
- M2 Schema and seed: migrations 000002–000007 go up/down/up; seeder makes 59,580 seats (layout 2.8 s) and inserts 0 rows on a second run.
- API contract: `backend/docs/openapi.yaml`, lints clean, TypeScript types generate.
- M3 Auth: phone OTP, email + password with optional two-step, rotating refresh tokens with reuse detection, role checks, staff invitations, first admin from env. Integration suite passes; the invite journey was run in a browser against the real API.
- M4 Events and inventory: event, category, layout, template and painting endpoints; publish creates exactly one inventory row per active seat (59,568) and a second publish changes nothing.
- M5 Seat map API: three levels plus the changed-since poll. Level 1 is 4,800 B, level 2 at most 6,557 B, level 3 for the largest block (1,312 seats) 43,617 B raw / 14,541 B gzipped; a poll with one change is 110 B.
- M5 follow-up, review fixes: requests behind a publish answer 409 `EVENT_BUSY` after 3 to 5 s instead of 500; edits after publish keep the sales window, a future kickoff and prices above zero; cancel of a sold-out venue (57,568 tickets) takes 10.7 to 16.0 s under its own timeouts; the default sales call always succeeds; a block is capped at 3,000 seats; drafts and templates can be deleted. Contract is 74 operations.
- M6 Holds and locking: hold, release, best available, sweeper, per-user limit, one payment extension, sell-by-hold. 1,000 parallel requests at 10 seats: 10 winners, 990 `SEATS_UNAVAILABLE`, 0 other errors, 0 seats held twice; 10 runs in a row passed. With the seat row lock removed the same test goes red (seats with 2 and 4 winners).
- M7 Bookings, payments, tickets: booking from a hold with an idempotency key; mock provider with nine outcomes; M-Pesa Daraja provider (tested against a fake server only); callback; reconciliation worker; refunds and organiser alerts; signed tickets issued in the payment transaction. A test passes for every row of the §8.2 table; a double-tapped Pay makes one booking; a late payment for resold seats ends REFUND_REQUIRED with no seat sold twice. Whole suite green; checked by hand on the local api + worker.
- Money-path review fixes (on `backend/payments-tickets`, migration 000009): a success after a FAILED or TIMEOUT verdict is applied; an STK push whose answer was lost stays live and its callback is matched; callbacks are stored before the webhook answers and retried by a worker job; TIMEOUT payments are re-queried for 7 days; every "paid" callback is confirmed with an STK query before anything is sold or refunded; unsignable tickets end in a refund; fees are whole shillings; the ledger checker sees dropped money. Each has a test on real Postgres that was seen red first.
- M8 Mobile fan flow: ran on an Android 16 emulator against the REAL API (sign in, 4 seats on the 1,312-seat block, checkout total from the hold, wrong PIN, retry, 4 tickets; the QR decoded from the ticket screen equals the ticket's token). No booking exists until the fan taps Pay. Inter and Plus Jakarta Sans fonts and an app icon are in (branch `mobile/real-api-fonts-icon`).
- M9 Admin web (on `main`): events, pricing, seat painting, publish, venue layout, templates, sales dashboard. A real full-venue publish through the browser showed the wait state and "Published" after 37.9 s. Alerts and Refunds screens, the busy-event message, delete for drafts and templates, a wait state for cancel, and the Inter and Plus Jakarta Sans fonts are on `main` too.
- Admin web, first slice (on `main`): login, Team (invite, resend, revoke), set-password page, account page.
- M10 Gate mode, backend (branch `backend/gate`): gate events, manifest with a change marker, scan upload where the server decides each result. A ticket bought through the API scanned ADMITTED, then DUPLICATE; wrong gate and a tampered token were refused. Full manifest for 59,568 tickets is 1.29 MB gzipped.
- M11: whole backend suite green on `backend/gate` (22 packages); load test written and run (`make loadtest`); README rewritten; results in `docs/TEST_REPORT.md`.

## Partly done

- M8: smooth zoom and pan on the largest block is not proven and pinch was not exercised (emulator only, no real phone); no installable build was made, so the icon was not seen on a launcher. Not run on the emulator: hold expiry back to the map, paying with under 2 minutes left, payments unavailable, a timed-out payment settling late, tickets offline, large system font. The app stopped responding to taps once during the run and the cause was not established.
- M9: the admin role was exercised only on Team, Alerts and Refunds; a long cancel (an event with sales) was not observed; the admin does not yet use `Alert.event_title`, `Alert.resolution`, `Refund.booking_status`, `already_completed` or the `booking_reference` filter the API now offers.
- M10 Gate mode, app: scanning screens with offline verification and an upload queue are built with unit tests; see the end of this file for whether the emulator run was done.
- M11: the load test passes on correctness (50 holds, 450 refusals, 0 errors, 0 double holds, six runs) and FAILS the speed target: p95 is 1.7 to 2.8 s against 300 ms on the dev laptop. The second, mixed load test was not run. See `docs/TEST_REPORT.md`.

## Not started

- Nothing. What is unfinished is listed under "Partly done" and "Known bugs and limits".


## Known bugs and limits

- Publishing 59,568 seats takes 30–44 s on the dev laptop (four foreign-key checks per row on `postgres:16-alpine`) against a 55 s limit for that route. Past the limit it rolls back and answers 500; a retry is safe. An async publish would remove the limit.
- Level 3 for the largest blocks is up to 46,245 B raw (14,930 B gzipped at most), over the ~40 KB in §5.1; 8 of 96 blocks are over. Shrinking it means changing the seat tuple in the contract.
- Nothing sets an event to SOLD_OUT or COMPLETED yet.
- After a payment that ends in a refund with nothing sold, the hold stays ACTIVE until it expires (at most 10 minutes), so those seats are not back on sale at once.
- A hold request alone takes about 65 ms on the dev laptop (several statements across five lock steps); folding it into fewer round trips is the first thing to do for speed.
- Gate: two offline phones at the same gate group both admit the same ticket; a phone told ADMITTED can later lose to an earlier scan and only learns it by uploading that scan again; scan order is only as good as the phones' clocks; a signed-in gate account can mark a ticket ID used without presenting its QR (the account and device are recorded).
- An ended hold (expired or released) lists no seats, because there is no hold-seats table.
- M-Pesa is untested against Safaricom: no real OAuth, STK push, STK query or callback has been made, and the callback source ranges in `.env.example` are placeholders. In particular unverified: the list of STK result codes treated as final failures (`domain.IsFinalSTKFailure`, written from Daraja's documentation), that Daraja's callback carries the full paying number (an unknown-ID callback is matched by it; a masked number matches nothing and is left to a person), how long Daraja answers an STK query for an old CheckoutRequestID (TIMEOUT payments are re-queried for 7 days), and whether Daraja resends a callback answered 503.
- Every "paid" callback now costs one STK query to Daraja before it is applied. If Daraja's query is down, paid callbacks wait in the inbox (retried for 7 days) and reconciliation times payments out with an alert; nothing is sold on the callback body alone.
- An unknown-ID "paid" callback is matched to a payment only when exactly one ID-less payment has the same phone and amount in the 24 hours before it; otherwise it stays for a person, with an alert filed under a guessed event (alerts must belong to an event).
- A stored callback is only retried while the worker runs (`payment-callback-drain`); the worker is not in the production image yet (see below).
- The ticket QR is static, so a screenshot copies it. The rotating QR of §9.3 is not built.
- Booking status FAILED is never written: a failed payment leaves the booking payable while its hold lives.
- Diagrams (the booking and payment state diagram, ERD, sequence diagrams) are deferred on the owner's instruction.
- No real SMS provider: in production the OTP is not delivered, so fans cannot sign in there until one is added (M7 adds the interface; a provider account is the owner's).
- Rate limits (including the per-user TOTP cap and the global SMS caps), TOTP replay protection and MFA-token single use are held in memory per process: a restart resets them, and a second API instance would not share them.
- TOTP setup and enable do not re-ask for the password: a borrowed signed-in session can turn two-step sign-in on with its own authenticator (turning it off does ask).
- TOTP secrets are stored unencrypted in `users.totp_secret`: a database leak gives away the second factor.
- A login with the old password racing a password change can leave one session alive: the change revokes the sessions that exist when it commits, not one created a moment later.
- A stranger can use up a fan's five OTP requests per hour by asking for codes for that number; the fan is then locked out of sign-in until the hour ends.
- Five wrong TOTP codes in 15 minutes hold back the real owner too, and anyone who knows the password can cause that.
- No "forgot password" flow for staff, and no way to disable a staff user or change a role.
- Ten wrong passwords in 15 minutes for one email also holds back the real owner of that account.
- staticcheck is not run: its newest release cannot type-check Go 1.27.

## For the owner to do in production (not done by this build)

- Before merging `backend/auth-invitations`, add to `/opt/talanta/.env` (the API refuses to start in production without the first one):
  - `JWT_SECRET` — required, at least 32 random bytes (`openssl rand -base64 48`).
  - `APP_ENV=production` — unset already means production, but say it explicitly.
  - `ADMIN_BASE_URL=https://talanta.vendorsoko.com` — now required and must be `https://`; the API refuses to start in production without it (there is no localhost default any more).
  - `BOOTSTRAP_ADMIN_EMAIL` and `BOOTSTRAP_ADMIN_PASSWORD` (10+ characters) — creates the first admin at startup when none exists; remove the password from the file afterwards.
  - `TRUSTED_PROXY_CIDRS` — now required; the API refuses to start in production when it is empty. Set it to the docker network range Caddy connects from (for example `172.16.0.0/12`), or to `none` only if the API is exposed directly with no proxy in front.
  - `CLIENT_IP_HEADER` — now empty by default; leave it unset. Set it to `CF-Connecting-IP` only once the origin accepts connections solely from Cloudflare (firewall or tunnel), because Caddy passes that header through from anyone who can reach the origin.
  - Optional, defaults are sensible: `ARGON2_MAX_CONCURRENT` (4), `OTP_ALLOWED_COUNTRY_CODES` (254), `OTP_GLOBAL_HOURLY_LIMIT` (2000), `OTP_GLOBAL_DAILY_LIMIT` (10000). Alert on the error-level log line "ALERT: global sign-in SMS cap reached".
  - Optional: `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM` to email invitations. Without them the admin copies the link. The server must offer STARTTLS (or use port 465); otherwise mail is not sent unless `SMTP_ALLOW_PLAINTEXT=true`.
  - Before merging `backend/payments-tickets`: `TICKET_SIGNING_KEY` — required; base64 of a 32-byte Ed25519 seed (`openssl rand -base64 32`). Keep a safe copy: tickets signed with a lost key can still be checked with its public half, but no new ones can be issued under that key ID. `TICKET_SIGNING_KEY_ID` defaults to `k1`.
  - Before merging `backend/payments-tickets`: `PUBLIC_BASE_URL=https://talanta.vendorsoko.com` — required, https; used for the ticket link sent by SMS.
  - To take real payments: `MPESA_ENV`, `MPESA_CONSUMER_KEY`, `MPESA_CONSUMER_SECRET`, `MPESA_SHORTCODE`, `MPESA_PASSKEY`, `MPESA_CALLBACK_URL` (https) and `MPESA_CALLBACK_ALLOWED_CIDRS`. With none of them set the API starts with payments off and answers every attempt to pay "not available"; with only some set it refuses to start. The mock is refused in production.
  - The worker, once it runs in production, needs the same `TICKET_SIGNING_KEY`, key ID, `PUBLIC_BASE_URL` and `MPESA_*` values as the API.
  - Optional: `CALLBACK_DRAIN_INTERVAL` (worker, default 5s) — how often stored M-Pesa callbacks are retried.
  - Optional, for testing before an SMS provider is connected: `OTP_TEST_CODES` — phone=code pairs (for example `+2547XXXXXXXX=<six digits>`) that sign in with a fixed code and get no SMS. Anyone who knows a pair can sign in as that number; remove it once SMS works.
  - Optional: `BOOKING_FEE_BPS` (default 0) — booking fee in basis points of the subtotal.
  - The full list with defaults is in `backend/.env.example`.
- Before merging the schema commit, check production has no rows that migration 000002 rejects, or the migrate step fails and leaves the schema marked dirty: `SELECT count(*) FROM users WHERE phone_e164 !~ '^\+[1-9][0-9]{7,14}$';` and `SELECT lower(email), count(*) FROM users WHERE email IS NOT NULL GROUP BY 1 HAVING count(*) > 1;` must both return nothing.
- The venue layout exists only after the seeder runs. Against production it creates the venue and the template only: no demo events and no demo users.
- Migration 000009 adds a CHECK on `stands.code` (`^[A-Z0-9]{1,10}$`); it fails if production has a stand code outside it: `SELECT code FROM stands WHERE code !~ '^[A-Z0-9]{1,10}$';` must return nothing.
- Migrations 000005 and 000008 add NOT NULL columns to `tickets` (and 000005 to `scan_logs`); they fail if production already has rows in those tables.
- The worker (`cmd/worker`: hold sweeper, payment reconciliation, the callback drain, hourly `auth-cleanup`) is not in the production image or compose file. Until it is, expired holds are still reclaimed at hold time, but unpaid bookings are not expired, payments are not reconciled, a callback that could not be applied at once is not retried, and `otp_codes` and `refresh_tokens` grow without being trimmed.
