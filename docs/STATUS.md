# Status

One line each. Updated at the end of every milestone.

## Done

- M1 Foundation: `make dev` brings up db, migrate, api, worker, adminer; `make test` and `make lint` pass. CI workflow left as it was (deployment files are off limits).
- M2 Schema and seed: migrations 000002–000007 go up/down/up; seeder makes 59,580 seats (layout 2.8 s) and inserts 0 rows on a second run.
- API contract: `backend/docs/openapi.yaml` (72 operations), lints clean, TypeScript types generate.

- M3 Auth: phone OTP, email + password with optional two-step, rotating refresh tokens with reuse detection, role checks, staff invitations, first admin from env. Integration suite passes; the invite journey was run in a browser against the real API.
- M4 Events and inventory: event, category, layout, template and painting endpoints; publish creates exactly one inventory row per active seat (59,568) and a second publish changes nothing.
- M5 Seat map API: three levels plus the changed-since poll. Level 1 is 4,800 B, level 2 at most 6,557 B, level 3 for the largest block (1,312 seats) 43,617 B raw / 14,541 B gzipped; a poll with one change is 110 B.
- M5 follow-up, review fixes: requests behind a publish answer 409 `EVENT_BUSY` after 3 to 5 s instead of 500; edits after publish keep the sales window, a future kickoff and prices above zero; cancel of a sold-out venue (57,568 tickets) takes 10.7 to 16.0 s under its own timeouts; the default sales call always succeeds; a block is capped at 3,000 seats; drafts and templates can be deleted. Contract is 74 operations.
- M6 Holds and locking: hold, release, best available, sweeper, per-user limit, one payment extension, sell-by-hold. 1,000 parallel requests at 10 seats: 10 winners, 990 `SEATS_UNAVAILABLE`, 0 other errors, 0 seats held twice; 10 runs in a row passed. With the seat row lock removed the same test goes red (seats with 2 and 4 winners).
- M7 Bookings, payments, tickets: booking from a hold with an idempotency key; mock provider with nine outcomes; M-Pesa Daraja provider (tested against a fake server only); callback; reconciliation worker; refunds and organiser alerts; signed tickets issued in the payment transaction. A test passes for every row of the §8.2 table; a double-tapped Pay makes one booking; a late payment for resold seats ends REFUND_REQUIRED with no seat sold twice. Whole suite green; checked by hand on the local api + worker.
- M8 Mobile fan flow (on `main`): ran end to end on an Android 16 emulator against a stand-in server; not yet re-run against the real API.
- M9 Admin web (on `main`): events, pricing, seat painting, publish, venue layout, templates, sales dashboard. A real full-venue publish through the browser showed the wait state and "Published" after 37.9 s.
- Admin web, first slice (on `main`): login, Team (invite, resend, revoke), set-password page, account page.

## Partly done

- M8: smooth zoom and pan on the largest block is not proven (the emulator was too loaded to judge); pinch was not exercised; no installable build was made.
- M9: cancel event, and acting on refund-required bookings, are built but were not exercised against the real API.

## Not started

- M10 Gate mode
- M11 Hardening

## Known bugs and limits

- Publishing 59,568 seats takes 30–44 s on the dev laptop (four foreign-key checks per row on `postgres:16-alpine`) against a 55 s limit for that route. Past the limit it rolls back and answers 500; a retry is safe. An async publish would remove the limit.
- Level 3 for the largest blocks is up to 46,245 B raw (14,930 B gzipped at most), over the ~40 KB in §5.1; 8 of 96 blocks are over. Shrinking it means changing the seat tuple in the contract.
- Nothing sets an event to SOLD_OUT or COMPLETED yet.
- An ended hold (expired or released) lists no seats, because there is no hold-seats table.
- `booking.ExtendForPayment` (used by M7) still waits without limit for an event that is being cancelled or published; M7 should bound it like hold and release.
- The admin and mobile apps do not yet handle `EVENT_BUSY` (retry, or "this event is being published").
- M-Pesa is untested against Safaricom: no real OAuth, STK push, STK query or callback has been made, and the callback source ranges in `.env.example` are placeholders.
- A callback from an allowed address with a known payment ID and the right amount is accepted as genuine (as §8 specifies). The IP allowlist is the only defence against a forged one; confirming each success with an STK query before selling is not built.
- A success callback for an STK request whose ID was never recorded (the push timed out on the network but Daraja took it) cannot be matched; it is logged at error level only.
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
  - `BOOKING_FEE_BPS` must produce whole-shilling fees, or bookings cannot be paid by M-Pesa.
  - Optional: `BOOKING_FEE_BPS` (default 0) — booking fee in basis points of the subtotal.
  - The full list with defaults is in `backend/.env.example`.
- Before merging the schema commit, check production has no rows that migration 000002 rejects, or the migrate step fails and leaves the schema marked dirty: `SELECT count(*) FROM users WHERE phone_e164 !~ '^\+[1-9][0-9]{7,14}$';` and `SELECT lower(email), count(*) FROM users WHERE email IS NOT NULL GROUP BY 1 HAVING count(*) > 1;` must both return nothing.
- The venue layout exists only after the seeder runs. Against production it creates the venue and the template only: no demo events and no demo users.
- Migrations 000005 and 000008 add NOT NULL columns to `tickets` (and 000005 to `scan_logs`); they fail if production already has rows in those tables.
- The worker (`cmd/worker`: hold sweeper, payment reconciliation, hourly `auth-cleanup`) is not in the production image or compose file. Until it is, expired holds are still reclaimed at hold time, but unpaid bookings are not expired, payments are not reconciled, and `otp_codes` and `refresh_tokens` grow without being trimmed.
- New env vars for `/opt/talanta/.env` will be listed here as each milestone adds them.
