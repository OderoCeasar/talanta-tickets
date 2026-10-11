# Kickoff prompt — Talanta Stadium Ticketing

Paste everything below the line into Claude Code, from the root of an empty repo that
already contains `docs/ARCHITECTURE.md`, `CLAUDE.md` and `.claude/agents/`.

---

You are the lead engineer on Talanta Stadium Ticketing. Build the whole system described in
`docs/ARCHITECTURE.md` so that it actually runs end to end: a Go API and worker, a Postgres
database, an Expo mobile app for fans and gate staff, and a React admin site for organisers.

Work the way a senior engineer would. That means: read before you write, make small changes,
run what you build, prove the risky parts with tests, and tell me plainly what is not done.

## 1. Read first

1. Read `docs/ARCHITECTURE.md` in full. It is the source of truth for scope, stack, data
   model, API and build order.
2. Read `CLAUDE.md`. It holds the standing rules and a list of corrections to the
   architecture document. Where the two disagree, `CLAUDE.md` wins.
3. Before writing code, write `docs/PLAN.md`: the milestones below, the order you will do
   them in, which sub agent owns each piece, and any question you could not answer from the
   documents. Then start. Do not wait for my approval unless a question blocks you.

## 2. Decisions already made (do not ask again)

The architecture document ends with open questions. Use these answers:

- **Stadium layout:** no official layout is available. Design a believable one in the seeder:
  4 stands (N, S, E, W), Lower and Upper tiers, VIP and VVIP in West, about 60,000 seats.
  Say in the README that the layout is made up.
- **Ticket categories:** the organiser defines them per event.
- **Gates:** staff use phones running gate mode in the same Expo app. No turnstile hardware.
- **Payments:** build and demo on the mock provider. Write the Daraja provider behind the
  same interface, driven by env vars, and switched off by default. Never call Safaricom in
  tests.
- **SMS (OTP and ticket fallback):** put it behind an interface with a mock that logs the
  message. In dev mode the OTP is also returned in the API response so the app can be used
  without a real SMS provider. This must be impossible to turn on in production mode.
- **Report format:** out of scope. Produce code, tests, diagrams sources and a README only.

## 3. How to use the sub agents

You coordinate. The sub agents in `.claude/agents/` do the focused work.

| Sub agent | Owns |
|---|---|
| `db-architect` | Migrations, indexes, sqlc queries, the seeder |
| `booking-concurrency-engineer` | Holds, best available, sweeper, per-user limits, the concurrency tests |
| `go-api-engineer` | HTTP layer, auth, events, admin endpoints, seat map endpoints, OpenAPI |
| `payments-engineer` | Payment provider interface, mock, Daraja, callback, reconciliation |
| `ticketing-gate-engineer` | Signed tickets, QR, gate manifest, offline scan sync |
| `mobile-engineer` | Expo app: fan flow, seat map, tickets, gate mode |
| `admin-web-engineer` | React admin: events, layout editor, pricing, sales dashboard |
| `qa-engineer` | Integration tests, end-to-end checks, load test |
| `code-reviewer` | Read-only review at the end of every milestone |

Rules for delegating:

- Give each sub agent one clear task with: the goal, the files it may touch, the section of
  the architecture document that applies, and how you will check the result.
- The API contract comes first. `backend/docs/openapi.yaml` is written before any client
  code. Mobile and admin generate their API client from it. Nobody hand-writes request types.
- Run sub agents in parallel only when they touch different folders. Backend packages that
  share the database schema go one after another.
- When a sub agent reports back, check its claim yourself. Run the tests and the command it
  says passes. Do not pass on a result you have not seen.
- At the end of each milestone, send the diff to `code-reviewer`, then fix every finding
  marked as blocking before moving on.

## 4. Milestones

Do them in order. A milestone is finished only when every "done when" line has been run and
seen to pass. Commit at the end of each one with a clear message.

**M1 — Foundation**
Repo layout from §12, `docker-compose.yml` (postgres, api, worker, adminer), Makefile,
config from env, structured logging, health endpoint, CI workflow that runs lint and tests.
Done when: `make dev` starts the API and database from a clean clone, and `make test` and
`make lint` pass.

**M2 — Schema and seed**
All tables from §4 with the corrections in `CLAUDE.md`. Seeder builds the full venue and
two demo events.
Done when: migrations go up and down cleanly, the seeder creates about 60,000 seats in under
5 seconds, and running it twice does not create duplicates.

**M3 — Auth**
Phone OTP for fans, email + password + TOTP for organisers and admins, JWT access and
rotating refresh tokens, role middleware, OTP rate limits.
Done when: tests cover a good login, a wrong code, an expired code, a reused refresh token,
and a fan being refused on an admin route.

**M4 — Events and inventory**
Event and category CRUD, layout endpoints, publish creates `event_seats` for every seat.
Done when: publishing an event creates exactly one inventory row per active seat, and
publishing twice is safe.

**M5 — Seat map API**
The three levels from §5.1, plus the "changed since" polling endpoint from §7.
Done when: level 1 and 2 payloads stay within the sizes in §5.1, level 3 returns one block
only, and the polling endpoint returns only seats that changed.

**M6 — Holds and locking (the spine)**
Hold, release, best available, sweeper, per-user ticket limit, rate limits.
Done when: an integration test against real Postgres fires 1,000 hold requests from many
parallel clients at the same small set of seats and shows no seat held twice, no deadlock,
and the right number of 409 answers. Also show the test can fail: remove the row lock on a
scratch branch, run it, and record that it goes red.

**M7 — Bookings, payments, tickets**
Booking from a hold with an idempotency key, mock payment covering every outcome, callback
handler, reconciliation worker, ticket issue with signed QR, SMS fallback through the mock.
Done when: there is a passing test for every row in the §8.2 failure table, a double-tapped
Pay creates one booking, and a late payment for resold seats ends as `REFUND_REQUIRED`
without selling a seat twice.

**M8 — Mobile fan flow**
Events list, event detail, stadium → stand → block map, seat selection, best available,
checkout with countdown, payment status, ticket screen with QR.
Done when: on an Android emulator or device a fan can log in, pick 4 seats, pay through the
mock and see 4 tickets. The largest block scrolls and zooms without visible stutter.

**M9 — Admin web**
Login with 2FA, event management, layout editor (place blocks, generate seats, paint
categories), pricing, live sales dashboard.
Done when: an organiser can create an event from the saved venue template, set prices,
publish it, and watch the sales numbers change while a booking is made.

**M10 — Gate mode**
Manifest download, offline signature check, local duplicate check, gate-bound tickets, scan
log upload when back online.
Done when: a valid ticket scans with the network off, a second scan of the same ticket is
refused, a forged code is refused, a ticket for another stand is refused, and scans upload
after reconnecting.

**M11 — Hardening**
Load test from §14, error handling pass, security pass, README with setup and demo steps,
ERD and sequence diagrams as source files in `docs/diagrams/`.
Done when: 500 virtual users go for the same 50 seats and the result is exactly 50
successes, 450 clean 409s and zero double sales. Report the measured p95. If it is over
300ms, say so and say why.

After M7 the backend is complete. If time or context runs short, finish M8 before M9 and
M10, and leave §13 items 6 to 14 as documented future work.

## 5. Proof I want to see

Do not tell me something works. Show me.

- For each milestone, paste the command you ran and the last lines of its output.
- For M6 and M11, give exact counts: requests sent, holds won, 409s, double sales.
- If you could not run something (no emulator, no Docker, no network), say that in the first
  line of the milestone report. Never write "should work".
- Keep `docs/STATUS.md` up to date: what is done, what is partly done, what is not started,
  and known bugs. One line each.

## 6. When to stop and ask

Ask me only if:

- A decision cannot be undone later and the documents do not cover it.
- You need a secret or an account I have to create.
- A milestone's "done when" cannot be met after a real attempt. Tell me what you tried.

Otherwise make the sensible choice, write it in `docs/DECISIONS.md` with one line of
reasoning, and keep going.

Start now with `docs/PLAN.md`, then M1.
