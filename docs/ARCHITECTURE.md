# Talanta Stadium Ticketing — Architecture & Build Plan

**Project:** Mobile-first ticket booking with interactive seat selection for Talanta Stadium
(Raila Odinga International Stadium), Nairobi.
**Context:** University capstone project + but can be scaled for mvp.
**Author:** oderoceasar@gmail.com
**Date:** 2026-09-13
**Status:** Design.

---

## 1. What the system does

Two audiences, one backend.

**Fan (Expo mobile app)**
1. Browse upcoming matches and events at Talanta Stadium.
2. Open an event, see ticket categories and prices.
3. Drill into a visual seat map and pick specific seats.
4. Pay with M-Pesa.
5. Receive a ticket with a scannable code, held in the app.

**Organiser (React web admin)**
1. Publish an event (fixture, date, gates-open time, poster).
2. Configure stands, blocks, rows and seats — the physical layout.
3. Assign seats to categories and set per-event pricing.
4. Monitor sales in real time: revenue, sold/held/available per category.

**Gate staff (mode inside the same mobile app)**
Scan tickets at the turnstile, including when the network is down.

---

## 2. Stack decisions

| Layer | Choice | Why |
|---|---|---|
| Fan app | **React Native + Expo (TypeScript)** | One codebase, Android-first for the Kenyan market; Expo Go makes demoing to a supervisor trivial. |
| Backend | **Go 1.27** | Cheap concurrency for hold expiry sweepers and payment reconciliation; a single static binary is easy to deploy and easy to defend in a viva. |
| Database | **PostgreSQL 16** | The whole project hinges on atomic seat reservation. `SELECT … FOR UPDATE` and real transactions are the reason this is not a NoSQL project. |
| Admin web | **React + Vite + TypeScript** | Shares types and API client with the Expo app. |
| Payments | **M-Pesa Daraja STK Push** | Designed now, implemented behind an interface later (see §8). |

### Go libraries (deliberately small)

| Need | Library | Note |
|---|---|---|
| HTTP router | `go-chi/chi/v5` | Stdlib-shaped, no framework lock-in. |
| Postgres driver | `jackc/pgx/v5` | Fastest, best Postgres type support. |
| Queries | `sqlc` | Generates type-safe Go from hand-written SQL. The critical queries here are hand-written anyway — an ORM would fight you. **Do not use GORM for the seat-locking path.** |
| Migrations | `golang-migrate` | Plain `.up.sql` / `.down.sql`. |
| Config | `caarlos0/env` or stdlib flags | Keep it boring. |
| Logging | `log/slog` (stdlib) | Structured logs, no dependency. |
| Validation | `go-playground/validator/v10` | Request DTO validation. |
| JWT | `golang-jwt/jwt/v5` | Access + refresh tokens. |
| Tests | `stretchr/testify` + `testcontainers-go` | Real Postgres in integration tests — essential for proving the locking works. |
| QR | `skip2/go-qrcode` | Server-side PNG generation for SMS/PDF fallback. |

---

## 3. Assumptions (state these in the writeup)

- Stadium capacity ~60,000 across four stands: **North, South, East, West**, each split into
  **Lower** and **Upper** tiers, with VIP/VVIP in the West stand.
- A stand contains **blocks**; a block contains **rows**; a row contains **seats**. This four-level
  hierarchy is what makes 60,000 seats tractable.
- Physical layout is **permanent per venue**. Pricing and availability are **per event**.
- One fan may buy several seats in one booking; each seat yields its **own ticket** so a group can split up.
- Currency is KES, integer **cents** everywhere. Never use floats for money.
- All timestamps stored `TIMESTAMPTZ` in UTC; displayed in `Africa/Nairobi`.

---

## 4. Domain model

```
Venue ──< Stand ──< Block ──< Row ──< Seat          (physical, permanent)
                                        │
Event ──< EventCategory (price)         │
   │                                    │
   └──< EventSeat ──────────────────────┘          (inventory, per event)
          │
          ├──< Hold (temporary, expires)
          └──< BookingItem ──> Booking ──> Payment
                                  │
                                  └──< Ticket ──< ScanLog
```

### Tables

**`venues`** — `id, name, city, capacity, plan_width, plan_height`
`plan_width/height` define the coordinate space the stadium plan is drawn in.

**`stands`** — `id, venue_id, code (N/S/E/W), name, tier (LOWER|UPPER), polygon jsonb, display_order`
`polygon` is the outline drawn on the stadium overview map.

**`blocks`** — `id, stand_id, code (e.g. "N12"), label, origin_x, origin_y, rotation_deg, arc_radius, seat_pitch_x, seat_pitch_y`
A block is a local grid placed and rotated on the stadium plan. `arc_radius` (nullable) curves rows
around the pitch; null means a straight rectangular block.

**`rows`** — `id, block_id, label ("A", "B", …), row_index, seat_count, x_offset`
`x_offset` staggers rows that don't start at the same seat number.

**`seats`** — `id, row_id, block_id, seat_number, label, x, y, seat_type (STANDARD|ACCESSIBLE|COMPANION|RESTRICTED_VIEW), is_active`
`x, y` are pre-computed at layout-save time so the app never does trigonometry at render time.
Unique on `(row_id, seat_number)`.

**`events`** — `id, venue_id, title, competition, home_team, away_team, kickoff_at, gates_open_at, poster_url, status (DRAFT|PUBLISHED|ON_SALE|SOLD_OUT|CANCELLED|COMPLETED), sales_open_at, sales_close_at, max_tickets_per_user`

**`event_categories`** — `id, event_id, name ("VIP", "Regular", "Terrace"), price_cents, currency, color_hex, description`

**`event_seats`** — the inventory table, one row per seat per event:
`id, event_id, seat_id, category_id, status (AVAILABLE|HELD|SOLD|BLOCKED), hold_id, hold_expires_at, version`
Unique on `(event_id, seat_id)`. Indexed on `(event_id, block_id, status)`.
Populated when the organiser publishes the event — ~60,000 inserts via `COPY`, under a second.

**`holds`** — `id, event_id, user_id, expires_at, status (ACTIVE|CONVERTED|EXPIRED|RELEASED), created_at`

**`bookings`** — `id, reference (human-readable, e.g. `TLT-8H3K2M`), event_id, user_id, hold_id, status (PENDING_PAYMENT|PAID|FAILED|EXPIRED|CANCELLED|REFUNDED), subtotal_cents, fee_cents, total_cents, idempotency_key, created_at, paid_at`

**`booking_items`** — `id, booking_id, event_seat_id, category_id, unit_price_cents`
Price is **snapshotted here**. If the organiser changes prices later, past bookings must not move.

**`payments`** — `id, booking_id, provider (MPESA), amount_cents, phone_e164, status (INITIATED|PENDING|SUCCESS|FAILED|TIMEOUT|REVERSED), merchant_request_id, checkout_request_id, mpesa_receipt, result_code, result_desc, raw_callback jsonb, created_at, completed_at`

**`tickets`** — `id (uuid), booking_id, event_seat_id, holder_name, holder_phone, token_nonce, status (VALID|USED|VOID|TRANSFERRED), issued_at, used_at`
One ticket per seat.

**`scan_logs`** — `id, ticket_id, gate_id, scanned_by, scanned_at, device_id, result (ADMITTED|DUPLICATE|INVALID|WRONG_EVENT), synced_at`

**`users`** — `id, phone_e164 (unique), name, email, role (FAN|ORGANISER|ADMIN|GATE_STAFF), created_at`

---

## 5. The seat map — the hard part

### 5.1 You cannot render 60,000 seats

An SVG with 60,000 nodes will freeze a mid-range Android phone. The answer is **level of detail** —
three zoom levels, each a different data payload:

| Level | What's drawn | Payload | Interaction |
|---|---|---|---|
| **1. Stadium** | ~8 stand polygons, tinted by cheapest available price | ~4 KB | Tap a stand |
| **2. Stand** | ~10–20 block rectangles, each with an availability count and price badge | ~10 KB | Tap a block |
| **3. Block** | 300–2,000 individual seats, real geometry, live status | ~40 KB | Tap seats to select |

Only level 3 fetches seat rows, and only for **one block at a time**. That caps the worst case at
about 2,000 rendered nodes, which React Native handles comfortably.

### 5.2 Rendering choice

- Levels 1 and 2: `react-native-svg`. A handful of polygons.
- Level 3: `react-native-svg` up to ~1,200 seats; switch to
  `@shopify/react-native-skia` above that. Skia draws to a canvas and stays at 60fps where SVG
  starts dropping frames.
- Pinch/pan with `react-native-gesture-handler` + `react-native-reanimated`, driven on the UI
  thread so zooming never blocks on JS.
- Hit-testing: don't attach a touch handler to every seat. Keep a flat array of seat bounds and do
  a spatial lookup on tap — one handler for the whole canvas.

### 5.3 Colour language (fixed early, used everywhere)

| State | Meaning |
|---|---|
| Category colour | Available — the colour *is* the price tier |
| Grey | Sold |
| Amber outline | Held by someone else right now |
| Solid dark + tick | Selected by you |
| Hatched | Blocked (broken seat, media, segregation buffer) |

Accessible seats carry a wheelchair glyph. Restricted-view seats carry a warning glyph and must show
a confirmation sheet before selection — this prevents a whole class of refund requests.

### 5.4 "Best available"

Most fans do not want to pick a seat; they want four seats together in the cheapest stand. Offer a
**Best Available** button that runs seat selection server-side: find the first `N` contiguous seats in
one row within a category. This is a genuinely different query (see §6.4) and is the highest-value
feature per line of code in the whole project.

### 5.5 Admin layout editor

The organiser should never place 60,000 seats by hand. The editor works in three steps:

1. **Place blocks** — drag rectangles onto the stadium plan, set rotation and optional arc radius.
2. **Generate seats** — enter rows × seats-per-row, row-label scheme (A–Z, then AA), numbering
   direction, and per-row offsets. The server generates seats and computes `x, y`.
3. **Paint categories** — marquee-select blocks or rows and assign a category; set the price once
   per category.

Store layouts as **venue templates** so the second and third events take minutes, not hours.

---

## 6. Concurrency — the core of the project

This is what the examiners will probe. Two fans tapping the same seat at the same moment must not
both get it.

### 6.1 Lifecycle

```
AVAILABLE ──select──> HELD ──pay──> SOLD
    ^                   │
    └───expire/release──┘
```

A hold lives **10 minutes**. That must be comfortably longer than an STK Push round-trip so a fan
who mistypes their PIN can retry without losing the seats.

### 6.2 Acquiring a hold (single transaction)

```sql
BEGIN;

WITH candidate AS (
    SELECT id
    FROM event_seats
    WHERE event_id = $1
      AND id = ANY($2::bigint[])
      AND (status = 'AVAILABLE'
           OR (status = 'HELD' AND hold_expires_at < now()))
    ORDER BY id                 -- deterministic order prevents deadlocks
    FOR UPDATE
)
UPDATE event_seats es
SET status          = 'HELD',
    hold_id         = $3,
    hold_expires_at = now() + interval '10 minutes'
FROM candidate c
WHERE es.id = c.id
RETURNING es.id;

COMMIT;
```

Three things make this correct, and each is worth a paragraph in the writeup:

1. **`FOR UPDATE`** takes a row-level write lock. A concurrent transaction touching the same seat
   blocks until this one commits, then re-evaluates the `WHERE` clause and finds the seat taken.
2. **`ORDER BY id`** means every transaction locks seats in the same order. Without it, two requests
   for seats `{5,9}` and `{9,5}` can deadlock.
3. **Expired holds are reclaimed in the `WHERE` clause**, not by a background job. The sweeper is a
   convenience; correctness never depends on it having run.

The handler compares `len(returned)` to `len(requested)`. On mismatch it rolls back and returns
**409 Conflict** with the exact seat IDs that were lost, so the app can grey them out and ask the fan
to re-pick — never a silent partial booking.

### 6.3 Expiry sweeper

A goroutine every 30 seconds:

```sql
UPDATE event_seats
SET status = 'AVAILABLE', hold_id = NULL, hold_expires_at = NULL
WHERE status = 'HELD' AND hold_expires_at < now()
  AND id IN (SELECT id FROM event_seats
             WHERE status='HELD' AND hold_expires_at < now()
             LIMIT 1000 FOR UPDATE SKIP LOCKED);
```

`SKIP LOCKED` keeps the sweeper from ever blocking a live booking. Batched so one long transaction
can't lock the table. Run it on a single instance, or guard with a Postgres advisory lock if you
deploy more than one.

### 6.4 Best-available query

```sql
SELECT s.row_id, array_agg(es.id ORDER BY s.seat_number) AS seat_ids
FROM event_seats es
JOIN seats s ON s.id = es.seat_id
WHERE es.event_id = $1 AND es.category_id = $2 AND es.status = 'AVAILABLE'
GROUP BY s.row_id
HAVING count(*) >= $3
ORDER BY min(s.seat_number)
LIMIT 20
FOR UPDATE SKIP LOCKED;
```

Then in Go, scan each row's seat numbers for a **contiguous** run of length `N` and hold the first
match. Note the deliberate contrast with §6.2: here `SKIP LOCKED` is correct — if another fan is
mid-transaction on a row, just try the next row. For explicit seat picks, skipping silently would be
wrong; the fan must be told their exact seat is gone.

### 6.5 Anti-abuse

- `max_tickets_per_user` per event, enforced at hold time across all of a user's active holds and
  paid bookings.
- Rate-limit hold creation per user and per IP.
- Idempotency key required on booking creation — a double-tapped Pay button must not create two
  bookings.

---

## 7. Realtime seat updates

Two options, and for a capstone the simpler one is defensible:

**v1 — polling.** While a block is open, `GET /events/{id}/blocks/{bid}/seats?since=<etag>` every
5 seconds, returning only changed seats. Simple, works on flaky 3G, and the 409 conflict path already
handles the race. **Start here.**

**v2 — Server-Sent Events.** Postgres `LISTEN/NOTIFY` on seat status change → Go fans out to
subscribers of that block → `GET /events/{id}/blocks/{bid}/stream`. SSE beats WebSockets here because
the flow is one-directional and it survives proxies. Add this if time allows; it is a strong demo
moment (a seat turning amber on one phone while another fan taps it).

---

## 8. M-Pesa — designed now, built later

Implement against an interface from day one so the whole booking flow works before Daraja
credentials arrive:

```go
type PaymentProvider interface {
    Initiate(ctx context.Context, req PaymentRequest) (PaymentInit, error)
    Query(ctx context.Context, checkoutRequestID string) (PaymentStatus, error)
}
```

Two implementations: `MockProvider` (simulates success, insufficient funds, wrong PIN, user cancel,
and timeout — drive it from a test header) and `DarajaProvider` later. **Build and demo on the mock.**

### 8.1 Flow

```
App                 Go API                 Daraja                Fan's phone
 │  POST /bookings    │                      │                       │
 │───────────────────>│ hold → booking       │                       │
 │                    │ PENDING_PAYMENT      │                       │
 │  POST /payments    │                      │                       │
 │───────────────────>│──── STK Push ───────>│──── PIN prompt ──────>│
 │                    │<── CheckoutRequestID │                       │
 │<── PENDING ────────│                      │                       │
 │  poll status       │                      │<──── PIN entered ─────│
 │───────────────────>│<─── callback ────────│                       │
 │<── PAID ───────────│ seats → SOLD, issue tickets                  │
```

### 8.2 Failure modes that must be handled

These are what separate a passing project from a good one:

| Failure | Handling |
|---|---|
| Callback never arrives | Reconciliation worker polls `STK Query` for `PENDING` payments older than 60s, up to 5 minutes. |
| Callback arrives twice | Idempotent handler keyed on `checkout_request_id`; second call is a no-op. |
| Callback arrives after hold expired, seats resold | Record payment, flag booking `REFUND_REQUIRED`, alert organiser. Never double-sell a seat to fix an accounting problem. Document this — it is the sharpest edge case in the system. |
| Fan pays the wrong amount | Compare callback amount to `booking.total_cents`; mismatch → `FAILED` + refund queue. |
| Callback is forged | Daraja callbacks are unauthenticated. Verify by matching `checkout_request_id` to a known pending payment, verifying the amount, and allowlisting Safaricom's callback IP ranges. **Never trust the callback body alone.** |
| Fan closes the app mid-payment | Booking state lives server-side; on reopen the app resumes from `GET /bookings/{ref}`. |

### 8.3 Practical notes for when you implement

- Daraja sandbox needs a **public HTTPS callback URL** — use `ngrok` in development.
- STK Push shortcode/passkey and OAuth credentials go in env vars, never in git.
- Store the full raw callback JSON in `payments.raw_callback` for dispute resolution.
- Money moves once: wrap "mark payment success + flip seats to SOLD + issue tickets" in **one**
  database transaction.

---

## 9. Tickets and gate scanning

### 9.1 QR payload

Do **not** encode the ticket UUID directly — anyone could forge one. Encode a signed token:

```
payload  = base64url(ticket_uuid || event_id || nonce)
signature = Ed25519_sign(server_private_key, payload)
qr_text  = "TLT1." + payload + "." + base64url(signature)
```

Roughly 120 characters — a comfortable QR version 6, scans instantly even on a cracked screen.

**Ed25519, not HMAC**, for one reason: the gate scanner can verify with only the **public** key, so
you never ship a signing secret to a device that could be stolen at a stadium.

### 9.2 Offline gates

With 60,000 people in one place, mobile data will not be reliable. The gate app must work offline:

1. Before the match, each device downloads the event's public key plus the set of valid ticket IDs.
2. At the gate, verify the signature **offline** and check membership locally.
3. Record every scan locally; sync in the background when connectivity returns.

**Double-scan across gates** is the honest limitation: two offline devices cannot know about each
other. Mitigation — tickets are gate-bound. A North Lower ticket only scans at North gates, so the
duplicate window is one gate, not the whole stadium. Say this explicitly in the writeup rather than
pretending offline single-use is solved; naming a limitation and bounding it reads as competence.

### 9.3 Screenshot sharing

A static QR can be WhatsApp'd to a friend. Two defences, in order of effort:

- **Cheap:** the in-app ticket animates and shows a live clock, so a screenshot is visibly stale to a
  steward.
- **Proper:** rotating QR — append a 30-second TOTP counter to the signed payload, refreshed in-app.
  Scanner accepts ±1 window. Needs the device clock to be roughly right.

### 9.4 SMS fallback

Not every fan will keep the app installed. On payment success, send an SMS with the booking reference
and a short link to a web ticket page. In Kenya this is not a nice-to-have.

---

## 10. API surface

```
POST   /auth/otp/request            { phone }
POST   /auth/otp/verify             { phone, code } -> { access, refresh }
POST   /auth/refresh

GET    /events                      ?from&to&status
GET    /events/{id}
GET    /events/{id}/categories
GET    /events/{id}/map                        -- level 1: stands + price-from
GET    /events/{id}/stands/{sid}/blocks        -- level 2: blocks + availability counts
GET    /events/{id}/blocks/{bid}/seats         -- level 3: individual seats
GET    /events/{id}/blocks/{bid}/stream        -- SSE (v2)

POST   /holds                       { event_id, seat_ids[] } -> 201 | 409 { unavailable[] }
POST   /holds/best-available        { event_id, category_id, quantity }
DELETE /holds/{id}
GET    /holds/{id}                  -- remaining seconds

POST   /bookings                    { hold_id, Idempotency-Key header }
GET    /bookings/{ref}
GET    /me/bookings

POST   /payments/mpesa/stk          { booking_id, phone }
GET    /payments/{id}
POST   /webhooks/mpesa/callback     -- public, unauthenticated, IP-allowlisted

GET    /me/tickets
GET    /tickets/{id}/qr
POST   /tickets/{id}/transfer       { to_phone }

-- Admin
POST   /admin/events                ; PATCH /admin/events/{id} ; POST /admin/events/{id}/publish
POST   /admin/venues/{id}/blocks    ; POST /admin/blocks/{id}/generate-seats
POST   /admin/events/{id}/categories ; POST /admin/events/{id}/assign-category
GET    /admin/events/{id}/sales     -- revenue, sold/held/available by category, timeseries
POST   /admin/events/{id}/cancel    -- triggers refund queue

-- Gate
GET    /gate/events/{id}/manifest   -- public key + valid ticket ID set
POST   /gate/scans                  -- batch upload of offline scans
```

---

## 11. Auth

- **Fans:** phone number + 6-digit SMS OTP. No passwords — correct for the Kenyan market and it makes
  the M-Pesa number the identity, which simplifies reconciliation.
- **Admin/organiser:** email + password (argon2id) + TOTP 2FA.
- JWT access token (15 min) + refresh token (30 days, rotating, stored hashed).
- Roles: `FAN`, `ORGANISER`, `ADMIN`, `GATE_STAFF`, enforced by chi middleware.
- Rate-limit OTP requests hard — SMS costs real money and is the obvious abuse vector.

---

## 12. Repository layout

```
talanta-tickets/
├── backend/                       # Go
│   ├── cmd/
│   │   ├── api/main.go
│   │   ├── worker/main.go         # hold sweeper + payment reconciliation
│   │   └── seed/main.go           # generate the Talanta layout + demo events
│   ├── internal/
│   │   ├── domain/                # entities, state machines, no I/O
│   │   ├── http/                  # handlers, middleware, DTOs
│   │   ├── store/                 # sqlc-generated + hand-written locking queries
│   │   ├── booking/               # hold + booking orchestration
│   │   ├── payments/              # PaymentProvider, mpesa/, mock/
│   │   ├── ticketing/             # Ed25519 signing, QR
│   │   ├── seatmap/               # layout generation, x/y computation
│   │   └── auth/
│   ├── migrations/
│   ├── docs/openapi.yaml
│   └── Makefile
├── mobile/                        # Expo
│   ├── app/                       # expo-router
│   │   ├── (tabs)/events.tsx | tickets.tsx | profile.tsx
│   │   ├── event/[id]/index.tsx | map.tsx | checkout.tsx
│   │   └── ticket/[id].tsx
│   ├── src/features/seatmap/      # SeatMapCanvas, LOD levels, gestures
│   ├── src/api/                   # generated client + TanStack Query hooks
│   └── src/store/                 # zustand: selection, hold countdown
├── admin/                         # React + Vite
│   └── src/features/{events,layout-editor,pricing,sales}/
├── docs/
│   ├── ARCHITECTURE.md            # this file
│   ├── ERD.png
│   └── diagrams/
└── docker-compose.yml             # postgres + api + adminer
```

**State management in Expo:** TanStack Query for anything server-owned (events, seats, bookings);
zustand only for local UI state (current selection, hold countdown). Do not mirror server data into
zustand — that is where stale seat maps come from.

---

## 13. Features worth adding (brainstorm output, ranked)

**High value, low effort — do these**
1. **Best available** (§5.4) — most fans never open the seat map.
2. **SMS ticket fallback** (§9.4).
3. **Booking reference** like `TLT-8H3K2M` — fans read it aloud at the gate.
4. **Countdown timer** on the checkout screen showing the hold expiring. Converts hesitation into sales and makes the concurrency model visible in a demo.
5. **Accessible seating** flagged in the map with a companion seat auto-suggested.

**High value, real effort — pick one or two**
6. **Ticket transfer** to another phone number. Kills the screenshot-sharing problem by giving fans a legitimate path, and it demos beautifully.
7. **Virtual waiting room** for high-demand matches. A token-bucket queue in front of the seat map. At 60,000 seats and a Harambee Stars derby this is a real requirement, and it is an excellent capstone talking point.
8. **Live sales dashboard** with a seat map heatmap of what is selling.
9. **Refunds on cancelled or postponed matches** — a state machine the organiser triggers.

**Nice to have — mention as future work**
10. Season tickets and member presale codes.
11. Dynamic pricing by demand.
12. Apple/Google Wallet passes.
13. Turnstile hardware integration.
14. Post-match analytics: no-show rate by stand.

---

## 14. Build plan (12 weeks)

| Week | Deliverable | Done when |
|---|---|---|
| 1 | Repo, docker-compose, Postgres, migrations, health endpoint | `make dev` brings up API + DB |
| 2 | Schema + seed: full Talanta layout, 4 stands, ~60k seats | Seeder runs in < 5s |
| 3 | Auth: OTP request/verify, JWT, roles | Fan logs in on device |
| 4 | Event + category CRUD, publish → materialise `event_seats` | Admin publishes an event |
| 5 | **Seat map API, all three LOD levels** | `/blocks/{id}/seats` returns geometry |
| 6 | **Holds + locking + sweeper + concurrency tests** | Two parallel clients, 1,000 requests, zero double-sells |
| 7 | Expo: events list, event detail, LOD 1 + 2 | Tap through stadium → stand → block |
| 8 | **Expo: interactive seat map**, pinch/pan, selection | Pick 4 seats, see the timer |
| 9 | Bookings + mock payment + ticket issuance + QR | Full flow ends with a ticket |
| 10 | Admin web: layout editor + pricing + sales dashboard | Organiser configures an event unaided |
| 11 | Gate scanner + offline verification | Scan a ticket in airplane mode |
| 12 | Polish, load test, ERD and diagrams, writeup | Report submitted |

**Week 6 is the spine of the project.** If it slips, cut features 6–9 from §13, not the concurrency tests.

### Load test to run in week 12

`k6` or `vegeta`: 500 virtual users all attempting to hold the **same 50 seats**. Pass criteria —
exactly 50 successes, 450 clean 409s, zero seats sold twice, p95 under 300ms. That single chart
justifies every design decision in §6, and it is the strongest slide in the defence.

---

## 15. Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Seat map performance on low-end Android | High | LOD from day one (§5.1); test on a real 2GB device, not the simulator |
| Daraja credentials arrive late or never | Medium | Mock provider behind an interface (§8); the demo never depends on Safaricom |
| Layout editor becomes its own project | High | Template-driven generation, not free-form drawing; timebox to week 10 |
| Concurrency bugs found late | Critical | Integration tests with `testcontainers-go` from week 6 |
| Scope creep from §13 | High | Weeks 1–9 are fixed; §13 items only from week 10 |

---

## 16. Mapping to capstone deliverables

| Report chapter | Source in this document |
|---|---|
| Problem statement | §1 |
| Requirements (functional / non-functional) | §1, §10, §15 |
| System architecture | §2, §12 |
| Database design + ERD | §4 |
| Algorithm design / novelty | **§6** — this is your contribution chapter |
| UI/UX design | §5 |
| Security | §8.2, §9.1, §11 |
| Testing & evaluation | §14 load test, week 6 tests |
| Limitations & future work | §9.2, §13 (items 10–14), §15 |

The examinable core of this project is **§6 (concurrency) and §5 (level-of-detail seat rendering)**.
Everything else is competent CRUD. Spend your defence time there.

---

## 17. Open questions

1. Is the real Talanta stand/block layout published anywhere, or do we design a plausible one and say so?
2. Are there fixed ticket categories from the FKF/organiser, or does the organiser define them per event?
3. Does the gate use phones, or is there existing turnstile hardware to integrate with?
4. Will you get Daraja sandbox credentials during the project, and is a public callback URL available?
5. Does the supervisor require a specific methodology (Agile/SDLC) or diagram notation (UML) in the report?
