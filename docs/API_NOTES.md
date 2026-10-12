# API notes

Companion to `backend/docs/openapi.yaml`, which is the contract. This file holds what is
easier to read as a table: error codes, the compact array layouts, the one-letter codes, and
where the API differs from `docs/ARCHITECTURE.md` §10 and §11.

74 operations on 64 paths, all implemented. Lint: `pnpm --package=@redocly/cli@2.62.1 dlx redocly lint backend/docs/openapi.yaml`.

## Basics

- Base path `/v1`. `GET /healthz` is also served with no prefix.
- Bearer JWT access token, 15 minutes. Refresh token 30 days, rotating.
- IDs are int64 JSON numbers. Ticket IDs, `client_scan_id` are UUID strings.
- Money: integer cents in `*_cents`, `currency` is always `KES`.
- Times: RFC 3339 UTC.
- Paging: `limit` (1 to 100, default 20) and `cursor`; answers are `{ items, next_cursor }`.
  Tiny lists (categories, venues, templates, my holds, gate events) are `{ items }` only.
- Another user's hold, booking, payment or ticket answers 404, not 403.
- 429 always carries `Retry-After` (seconds).
- Every answer carries `X-Request-Id`. A client may send its own (8 to 64 characters of
  `A-Z a-z 0-9 - _`) and it is used; anything else is replaced.
- Request bodies are JSON only (`Content-Type: application/json`), at most 1 MiB, with
  unknown fields refused. An unknown path or a wrong method answers 404 `NOT_FOUND`.
- A fan (or gate steward) calling anything under `/admin`, existing or not, gets 403.

## Error shape

```json
{ "error": { "code": "SEATS_UNAVAILABLE", "message": "…", "details": { "unavailable_seat_ids": [910005] } } }
```

`details` is null or one of the fixed shapes below. Exceptions that do not use this shape:
`GET /healthz` 503 (`{ "status": "degraded" }`), `GET /t/{webToken}` (HTML, every status), and
`POST /webhooks/mpesa/callback` (always 200).

## Error codes

| Code | HTTP | Meaning | `details` |
|---|---|---|---|
| `VALIDATION_FAILED` | 400 | Body, query, path or header value is malformed | `fields[] {field, rule, message}` |
| `IDEMPOTENCY_KEY_REQUIRED` | 400 | `Idempotency-Key` header missing or malformed on `POST /bookings` | `fields[]` |
| `UNAUTHENTICATED` | 401 | No access token, or a bad one | null |
| `TOKEN_EXPIRED` | 401 | Access token expired: refresh, retry once | null |
| `OTP_INVALID` | 401 | Wrong phone code, or none was requested | null |
| `OTP_EXPIRED` | 401 | Phone code is older than 5 minutes | null |
| `INVALID_CREDENTIALS` | 401 | Wrong email or password (same answer for both) | null |
| `MFA_TOKEN_INVALID` | 401 | `mfa_token` expired, used or malformed: start again at `/auth/login` | null |
| `TOTP_INVALID` | 401 | Wrong TOTP code at sign-in; may retry | null |
| `REFRESH_TOKEN_INVALID` | 401 | Refresh token unknown, expired or revoked | null |
| `REFRESH_TOKEN_REUSED` | 401 | A used refresh token was presented; the whole chain is revoked | null |
| `FORBIDDEN` | 403 | Signed in, wrong role | null |
| `MOCK_NOT_ALLOWED` | 403 | `X-Mock-Outcome` sent when `APP_ENV=production` | null |
| `NOT_FOUND` | 404 | No such thing, or not the caller's | null |
| `INVITATION_INVALID` | 404 | Invitation token unknown, revoked or already accepted | null |
| `INVITATION_EXPIRED` | 410 | Invitation is past its expiry | null |
| `SEATS_UNAVAILABLE` | 409 | One or more requested seats are not free; nothing was held | `unavailable_seat_ids[]` |
| `NO_ADJACENT_SEATS` | 409 | Best available found no run of neighbouring seats | `category_id, quantity, available` |
| `EVENT_NOT_ON_SALE` | 409 | Event status is not `ON_SALE` | `event_status, sales_open_at, sales_close_at` |
| `SALES_NOT_OPEN` | 409 | Before `sales_open_at` | same |
| `SALES_CLOSED` | 409 | After `sales_close_at` | same |
| `HOLD_EXPIRED` | 409 | The hold ran out; or, when starting a payment, it has under 2 minutes left (`hold_status` is then still `ACTIVE`) | `hold_id, hold_status, expires_at` |
| `HOLD_NOT_ACTIVE` | 409 | The hold was released or already paid for | same |
| `HOLD_ALREADY_BOOKED` | 409 | The hold has a booking made with a different idempotency key | `booking_reference, booking_status` |
| `BOOKING_NOT_PAYABLE` | 409 | Booking is not `PENDING_PAYMENT` | `booking_reference, booking_status` |
| `PAYMENT_IN_PROGRESS` | 409 | A payment for the booking is still live | `payment_id, payment_status` |
| `EVENT_NOT_READY` | 409 | Publish refused | `problems[]` (same as readiness) |
| `INVALID_STATE_TRANSITION` | 409 | Publish or cancel not allowed from the current status | `current_status` |
| `EVENT_NOT_EDITABLE` | 409 | Event is past the status where this edit is allowed | `current_status` |
| `EVENT_BUSY` | 409 | The event is locked by a publish, a cancel or another edit that did not finish within the lock wait; nothing changed, send the request again. Also on `POST /bookings`, `POST /payments/mpesa/stk` and `POST /admin/refunds/{id}/complete` | null |
| `BLOCK_HAS_INVENTORY` | 409 | Block is used by a published event, or an event at the venue is being published right now; cannot delete or regenerate | `event_ids[]` (empty in the second case) |
| `CATEGORY_IN_USE` | 409 | Category has inventory or bookings | `seats, sold, held` |
| `CONFIRMATION_REQUIRED` | 409 | Price change after the event has left `DRAFT` needs `confirm: true` | `sold, held` |
| `ALREADY_EXISTS` | 409 | Unique name or code is taken | `fields[]` |
| `EMAIL_ALREADY_REGISTERED` | 409 | A user with this email exists | null |
| `INVITATION_ALREADY_PENDING` | 409 | An unexpired invitation for this email exists | null |
| `INVITATION_NOT_PENDING` | 409 | Resend or revoke on an accepted (or, for resend, revoked) invitation | null |
| `TOTP_ALREADY_ENABLED` | 409 | TOTP is already on | null |
| `TOTP_NOT_ENABLED` | 409 | TOTP is not on | null |
| `TOTP_SETUP_REQUIRED` | 409 | `enable` called before `setup` | null |
| `PAYLOAD_TOO_LARGE` | 413 | Request body over the size limit | null |
| `TICKET_LIMIT_EXCEEDED` | 422 | Hold would pass `max_tickets_per_user` | `allowed, held_or_bought, requested, remaining` |
| `IDEMPOTENCY_KEY_REUSED` | 422 | Key already used for a different hold | null |
| `VENUE_MISMATCH` | 422 | Template, event, block or row belongs to another venue | `fields[]` or null |
| `PASSWORD_INCORRECT` | 422 | Current password is wrong (signed-in routes) | null |
| `TOTP_CODE_INCORRECT` | 422 | Wrong code when turning TOTP on | null |
| `RATE_LIMITED` | 429 | Too many requests | `retry_after_seconds` |
| `OTP_ATTEMPTS_EXCEEDED` | 429 | 5 wrong phone codes; request a new one | `retry_after_seconds` |
| `INTERNAL` | 500 | Fault on our side | null |
| `PAYMENT_PROVIDER_UNAVAILABLE` | 502 | M-Pesa did not accept the STK request. The attempt is kept as a `FAILED` payment; the booking stays payable | null |

Wrong password and wrong TOTP code on signed-in routes are 422, not 401, so a client that
refreshes its token on any 401 does not mistake them for an expired session.

429 is declared on the auth endpoints, the public reads, holds, bookings, payments, the
ticket QR image and the gate's scan upload. Admin routes and the two gate reads are not
rate limited.

## Validation details

`details.fields[].field` is the JSON name of the request body field (`email`,
`new_password`, `seat_ids[2]`, `inner.name`), or the query, path or header name
(`limit`, `cursor`, `invitationId`, `Content-Type`). Two special values: `body` (the body is
missing, is not JSON, or is not a single object) and the name of an unknown field.

| `rule` | Meaning |
|---|---|
| `required` | Missing or empty |
| `email`, `e164`, `number`, `len`, `min`, `max`, `oneof`, `gt`, `gte`, `lte` | The value breaks that rule (`gt` on `price_cents`: a category of an event that has left `DRAFT` must cost more than zero) |
| `type` | Wrong JSON type (a number where a string is expected) |
| `unknown` | The body has a field the endpoint does not accept |
| `json` | The body is not valid JSON |
| `content_type` | The body was not sent as `application/json` |
| `id`, `range`, `cursor`, `reference` | Path ID, `limit`, `cursor` or booking reference is malformed; `range` on `from` of the sales call: a `from` the caller sent is more than 2,000 buckets back |
| `datetime` | Not an RFC 3339 time (body field or `from`/`to` query) |
| `url` | `poster_url` is not an `http(s)` address |
| `unique` | A list repeats a value (`block_ids`, `row_ids`); also the rule on `ALREADY_EXISTS` |
| `hexcolor`, `blockcode` | Not `#RRGGBB`; not capitals, digits and dashes |
| `before_kickoff`, `after_open`, `after_from` | Times in the wrong order (`gates_open_at`, `sales_close_at`, `to`) |
| `future` | `kickoff_at` of an event that has left `DRAFT` would be moved into the past |
| `max_seats` | `rows` x `seats_per_row` is more than 3,000 seats in one block (on both fields) |
| `whole_shillings` | `price_cents` of a category is not a multiple of 100: M-Pesa takes no cents |
| `plan_bounds` | The block's outline would leave `[0, plan_width] × [0, plan_height]` |
| `layout` | The seat geometry cannot be made (for example a row too long for the arc) |
| `venue` | On `VENUE_MISMATCH`: the block, row, template or event is at another venue |
| `uuid`, `gategroup`, `event` | In a scan batch (`scans[3].ticket_id`): not a UUID; not a gate group such as `N-LOWER`; no event with that id |

## Sign-in behaviour the contract does not spell out

- **Phones.** Clients should send E.164 (the contract's `Phone` pattern). The server also
  accepts the Kenyan local forms `07…`, `01…` and `2547…`/`2541…`, with spaces or dashes, and
  stores and returns E.164, so one number is always one account. Sign-in codes are sent only
  to the calling codes in `OTP_ALLOWED_COUNTRY_CODES` (default `254`); any other number
  answers 400 `VALIDATION_FAILED` on `phone` (rule `e164`) on both request and verify.
- **OTP limits.** One code per phone per 60 s, five per phone per hour, five verify
  attempts per code (the sixth answers 429 `OTP_ATTEMPTS_EXCEEDED` even with the right
  code). A new code retires the old one. A staff account is never signed in by phone code.
  Across all phones at most `OTP_GLOBAL_HOURLY_LIMIT` (2,000) codes an hour and
  `OTP_GLOBAL_DAILY_LIMIT` (10,000) a day are sent; past that, requests answer 429
  `RATE_LIMITED` and the API logs an error-level alert.
- **Refresh.** 400 only for a malformed body; 401 only when the token is unknown, expired,
  revoked or reused; 429 leaves the token unused and still good. A stale `Authorization`
  header on `/auth/*` is ignored. Two refreshes racing with the same token count as reuse
  and end the session, so a client must not refresh in parallel.
- **Access token claims:** `sub` (user id as a string), `role`, `sid` (session), `iat`,
  `exp`, `jti`. HS256.
- **Staff sign-in limits.** Ten password attempts per email per 15 minutes, thirty per IP.
  The per-email count is cleared by a finished sign-in: the password alone when the user has
  no TOTP, the TOTP step when they do (a good password followed by no code does not clear
  it). An `mfa_token` lives 5 minutes, allows five codes and works once; a TOTP code that has
  signed the user in is refused if replayed. Across all `mfa_token`s, five TOTP codes may be
  tried per user per 15 minutes; after that `/auth/totp/verify` answers 429 `RATE_LIMITED`
  with `Retry-After`, even for the right code, until the window ends.
- **Busy server.** At most `ARGON2_MAX_CONCURRENT` (4) password hashes run at once; other
  requests wait. One still waiting when its request times out answers 429 `RATE_LIMITED`
  (`Retry-After: 5`). This applies to login, invitation accept, password change and TOTP
  disable.
- **Caller's address.** Forwarding headers are read only on connections from
  `TRUSTED_PROXY_CIDRS`; the address is then the right-most `X-Forwarded-For` entry that is
  not one of those proxies. `CLIENT_IP_HEADER` (empty by default) names a single-address
  header such as `CF-Connecting-IP` to believe first. It may be set only when the origin
  accepts connections solely from the CDN that writes it, or the proxy overwrites it; a
  proxy that passes it through would let any caller choose their own rate-limit bucket.
  Production must set `TRUSTED_PROXY_CIDRS` to a range or to `none` (exposed directly).
- **Invitations.** A dead link is reported (404/410) before the form is validated. Accept
  takes the role from the invitation only. `invite_url` is `ADMIN_BASE_URL/invite/<token>`.
- **Per-IP limits** (per 10 or 15 minutes unless said): OTP request 300, OTP verify 600,
  login 30, TOTP verify 30, invitation preview and accept 30 together, refresh 1,200 a
  minute. Password change and TOTP settings: 10 per user per 15 minutes.

## Compact array layouts

Arrays are used where one answer carries thousands of rows. The generated TypeScript types
are real tuples.

### Level-3 seat — `SeatTuple` (`GET /events/{eventId}/blocks/{blockId}/seats`)

`rows[].seats[]` is `[event_seat_id, seat_number, x, y, type_code, category_index, status_code]`

| Pos | Name | Type | Meaning |
|---|---|---|---|
| 0 | `event_seat_id` | int64 | What to send in `POST /holds` `seat_ids` |
| 1 | `seat_number` | int | Number within the row. Seat label = row label + number |
| 2 | `x` | number | Seat centre, venue plan coordinates, at most 2 decimals |
| 3 | `y` | number | same |
| 4 | `type_code` | 0 to 3 | `0` STANDARD, `1` ACCESSIBLE, `2` COMPANION, `3` RESTRICTED_VIEW |
| 5 | `category_index` | int | Position in the answer's `categories` array; `-1` = no category (always blocked) |
| 6 | `status_code` | letter | See below |

### Level-3 change — `SeatChangeTuple` (same endpoint with `?since=`)

`changes[]` is `[event_seat_id, status_code]`.

The answer has `kind: "full"` or `kind: "delta"`. A `since` request can still get `full`
(marker unreadable or too old, or something other than status changed); the client then
replaces its cached block. The marker is opaque. How it works is in "Seat map behaviour"
below.

### Seat status letters

| Code | Meaning |
|---|---|
| `A` | Available |
| `H` | Held by someone else |
| `M` | Held by you (only with a FAN token that owns the hold) |
| `S` | Sold |
| `B` | Blocked |

Who holds a seat is never sent: the query selects only "is this hold mine" and "has it
lapsed", never the hold or its user. A lapsed hold that the sweeper has not cleared yet is
`A`, to its former holder too.

### Physical seat — `PhysicalSeatTuple` (`generate-seats`, `GET /admin/blocks/{blockId}/seats`)

`[seat_id, seat_number, x, y, type_code, active]` — `seat_id` is null in a dry run; `active`
is `0` or `1`. Row `id` is also null in a dry run.

### Manifest ticket — `ManifestTicketTuple` (`GET /gate/events/{eventId}/manifest`)

`tickets[]` is `[ticket_id, gate_group_index, status_code]`

| Pos | Name | Type | Meaning |
|---|---|---|---|
| 0 | `ticket_id` | UUID string | The ticket ID inside the signed QR payload |
| 1 | `gate_group_index` | int | Position in the manifest's `gate_groups` array |
| 2 | `status_code` | letter | `V` VALID, `U` USED, `X` VOID, `T` TRANSFERRED |

Full manifest (`full: true`): only `V` and `U`. With `since` (`full: false`): any ticket
that changed, including `X` and `T`, which the device removes. The index is into the
`gate_groups` of the same answer. More in "Gate mode" below.

### Points and polygons

A point is `[x, y]`; a polygon is an array of points, closed, last point not repeated.

## Measured sizes (seeded Talanta venue, 59,568 seats on sale)

Measured through the real router with `Accept-Encoding: gzip` (chi compress, level 5) by
`TestSeededVenue_SeatMapPayloadSizes`.

| Payload | Raw | Gzipped | §5.1 target |
|---|---|---|---|
| Level 1, 8 stands | 4,800 B (4.7 KB) | 1,584 B | ~4 KB |
| Level 2, largest stand (14 blocks) | 6,557 B (6.4 KB) | 1,993 B | ~10 KB |
| Level 3, largest answer of all 96 blocks (block of 1,312 seats) | 46,245 B (45.2 KB) | 14,274 B | ~40 KB |
| Level 3, largest gzipped answer (another block of 1,312 seats) | 44,930 B | 14,930 B | |
| Level 3 poll, nothing changed | 100 B | not compressed | |
| Level 3 poll, 6 seats changed | 165 B | not compressed | |

Every block is measured and the largest answer reported. Level 3 costs 33 to 35 bytes a
seat raw (ids, labels and coordinates of later blocks have more digits), so the answer for
blocks of the same seat count differs by thousands of bytes: 8 of the 96 blocks pass 40 KB
and the largest is 13% over. (The figure first reported, 43,617 B, was the lowest-id block
among the equally large ones, not the maximum. Exact bytes move by a few with the ids a
database happens to hand out.) A curved block's coordinates are irregular and compress to a
third, not the eighth estimated before measuring. What would shrink it, each a contract
change: send `x`/`y` only for curved blocks and let the client lay a straight block out
from origin, rotation and pitch; send the first `event_seat_id` of a row and let the rest
follow by position; send coordinates as integers in tenths of a plan unit.

The gate manifest (not built yet) was estimated at 2.8 MB raw for 60,000 tickets, bound by
the 16 random bytes in each UUID.

## Differences from ARCHITECTURE §10 and §11

- **`/v1` prefix** on every route. The admin site shares the origin (see DECISIONS).
- **Staff sign-in, by the owner's instruction (differs from §11):** TOTP is optional.
  `POST /auth/login` answers the full session when the user has no TOTP, or
  `{ mfa_required: true, mfa_token }` when they do; `POST /auth/totp/verify` is the second
  step. Staff turn TOTP on and off themselves (`/me/totp/setup`, `/enable`, `/disable`) and
  can change their password (`/me/password`).
- **Staff are invited by email** (not in §10 or §11): `/admin/invitations` (create, list,
  resend, revoke) and `/admin/users`, ADMIN only; `/auth/invitations/{token}` (preview) and
  `/accept` are public. `invite_url` is returned only by create and resend.
- **`POST /auth/logout`** added. `POST /auth/refresh` takes `{ refresh_token }` in the body.
- **`GET /me`, `PATCH /me`** added.
- **`GET /me/holds`** added: after a restart the app has no other way to find a hold that
  has no booking yet, and those seats count against the ticket limit.
- **Level 3 has two answer shapes** (`full`, `delta`) on one URL, chosen by `since`.
  §7 calls the value an etag; here it is an opaque `marker`.
- **SSE stream `GET /events/{id}/blocks/{bid}/stream` (§7 v2): not in the contract.** Future work.
- **`POST /tickets/{id}/transfer`: not in the contract.** Future work. The `TRANSFERRED`
  ticket status and the `T` manifest code exist so adding it later changes no types.
- **`GET /tickets/{ticketId}`** and **`GET /t/{webToken}`** (web ticket page, §9.4) added.
  The web token is per booking (`bookings.web_token`), so the page shows all its tickets.
- **Hold errors:** `EVENT_NOT_ON_SALE`, `SALES_NOT_OPEN`, `SALES_CLOSED` (409),
  `TICKET_LIMIT_EXCEEDED` (422). The 409 body is the standard error shape with
  `details.unavailable_seat_ids`, not a bare `{ unavailable[] }`.
- **`POST /bookings`** answers 200 instead of 201 when the idempotency key replays.
- **`X-Mock-Outcome`** header on `POST /payments/mpesa/stk`, dev only.
- **`Payment.failure_reason`** added: a stable value for the app, next to Daraja's text.
- **`EVENT_BUSY`** can answer `POST /bookings`, `POST /payments/mpesa/stk` and
  `POST /admin/refunds/{refundId}/complete` (see "Bookings and payments behaviour").
- **Scan results** include `WRONG_GATE` (correction 8). Booking statuses include
  `REFUND_REQUIRED` (correction 3).
- **Manifest carries `keys[]`**, not one `public_key`, because tickets record the key that
  signed them (`tickets.key_id`) so keys can be rotated.
- **Admin** gained what the site needs beyond §10: venue read, block edit and delete, block
  seats, templates, event list and read, readiness, category edit and delete, assignments
  read, event bookings, alerts, refunds.
- **Gate** gained `GET /gate/events`.

## Not in the contract (open)

- Editing a single seat's type or switching a seat off (`seats.seat_type`, `is_active`).
  The seeder sets them; the editor shows them but cannot change them.
- Marking an event `COMPLETED` (expected of the worker) and `SOLD_OUT` (expected of the
  hold or payment flow). Nothing needs to move `PUBLISHED` to `ON_SALE`: every read derives
  it from the sales window (see "Event status" below).
- Blocking and unblocking individual seats after publish.
- A scan of a QR that cannot be read as a ticket ID is not uploaded (`scan_logs.ticket_id`
  is required).
- Disabling or changing the role of a staff user.

## Events and inventory behaviour (M4)

- **Event status.** The stored status of a published event is `PUBLISHED` or `ON_SALE`,
  set once at publish. Every answer carries the *effective* status instead: `ON_SALE`
  exactly while now is inside `[sales_open_at, sales_close_at)`, `PUBLISHED` outside it.
  Nothing flips it in the background. `domain.EffectiveEventStatus` is the rule; the list
  filters use the same rule in SQL. Legal moves of the stored status are one table in
  `domain.TransitionEvent`.
- **Publish** takes the event row lock, then checks status, then readiness, then inserts
  one inventory row per active seat. Painting, category changes, event edits, delete and
  cancel take the same lock, so the inventory can never disagree with the painting. A repeat on a
  published event answers 200 with `already_published: true`. It runs under its own request
  timeout (the server's write timeout less five seconds, 55 s by default) and raises the
  statement timeout for its own transaction.
- **Busy events (`EVENT_BUSY`).** Nothing queues behind a publish or a cancel for long.
  Every call that needs the event's lock waits a bounded time and then answers 409
  `EVENT_BUSY` with nothing changed: 5 seconds for staff calls (paint, category create,
  edit and delete, event edit and delete, cancel, a second publish), 3 seconds for a fan's
  `POST /holds`, best available and `DELETE /holds/{id}`. The client retries the same request; the admin should
  say "this event is being published" rather than show an error. Before this the request
  waited 15 seconds and answered 500. In the layout editor, a move, a new label or a new
  curve of a block is not held up by a publish at all; a new block `code` is, and answers
  `EVENT_BUSY` after 3 seconds, as does a second editor of the same block. Deleting or
  regenerating a block during a publish still answers `BLOCK_HAS_INVENTORY` with an empty
  `event_ids`. A hold request for a `DRAFT` event that is being published gets `EVENT_BUSY`
  too, where a moment earlier or later it gets 404 or 201.
- **Edits after publish.** Once an event has left `DRAFT`, `PATCH /admin/events/{id}` is
  checked on the merged event against what publish required: `sales_open_at` and
  `sales_close_at` must both stay set (rule `required`), in order and not after kickoff,
  and `kickoff_at` may not be moved into the past (rule `future`). A kickoff that has
  simply passed does not stop other edits. A category of such an event must cost more than
  zero, on create and on edit, with or without `confirm` (rule `gt` on `price_cents`).
- **Delete.** `DELETE /admin/events/{id}` removes a `DRAFT` with its categories and
  painting; any other status is 409 `EVENT_NOT_EDITABLE`. `DELETE
  /admin/venues/{venueId}/templates/{templateId}` removes a template; events made from it
  keep what was copied.
- **Readiness** needs: at least one active seat, at least one category, every active seat
  painted (row override, else block), every category priced above zero, both ends of the
  sales window set, close after open and not after kickoff, kickoff in the future.
- **Cancel** is allowed from every status but `COMPLETED`; see the operation for what it does.
  It is one transaction and, like publish, has the longer request and statement timeouts:
  on the full venue sold out (57,568 tickets, 14,392 paid bookings, 1,000 live holds) it
  took 10.7 s and 16.0 s in two runs on the dev laptop. A repeat answers in milliseconds
  with zero counts.
- **Categories.** A price change once the event has left `DRAFT` needs `confirm: true`. A
  category can be deleted while nothing points at it (no inventory row, no booking item).
  `CATEGORY_IN_USE` carries `seats` (inventory rows of the category, any status), `sold`
  and `held`: `seats > 0` with the other two at zero means only "the event is published".
  `price_cents` is whole cents: `10.5` is refused.
- **Painting** is allowed only while `DRAFT`. Painting a block does not clear the row
  overrides inside it.
- **Lists.** `GET /events` shows `PUBLISHED`, `ON_SALE` and `SOLD_OUT` with kickoff from
  now (or `from`) on, soonest first. `GET /admin/events` shows everything, latest first.
  `from` is inclusive and `to` exclusive in both. `seats_available` and `price_from_cents`
  in either LIST may be up to 20 seconds old; one event read on its own, and the seat map,
  keep the 3 seconds.
- **Public reads and `Authorization`.** The public event and seat map reads need no token,
  but look at one if it is sent. A header that is malformed, not a valid token or expired
  answers 401 on all six of them, as the contract now says; send no header to be anonymous.
- **Block size.** A block is at most 200 rows, 500 seats per row and 3,000 seats in all
  (`seatmap.MaxSeatsPerBlock`); the largest block of the real stadium has 1,312.
- **Seat generation** replaces a block's rows and seats; the row overrides of draft events
  on the old rows go with them (the block's own painting stays). `venues.capacity` is
  recounted on every generate and delete. Seat labels are row label + number (`C14`), with
  a dash when rows are numbered (`12-14`).
- **Sales defaults.** `GET /admin/events/{id}/sales` with no parameters always succeeds.
  No `bucket`: `hour` for a range of 48 hours or less, `day` beyond (`series.bucket` says
  which). No `from`: the first paid booking, or the most recent 2,000 buckets when that
  would be more. A `from` the caller sends is never moved and is 400 (rule `range`) past
  2,000 buckets.
- **Sales.** Day buckets are Nairobi days (21:00 UTC to 21:00 UTC). `refund_required_cents`
  is the queued refunds of `REFUND_REQUIRED` bookings, or a booking's total while none is
  queued. A refund that is `DONE` no longer counts, and a booking leaves both figures when
  it becomes `REFUNDED`. `Payment.failure_reason` is derived from Daraja's result code (1
  insufficient funds, 2001 wrong PIN, 1032 cancelled, 1037 and 1019 timeout; a `FAILED`
  payment with code 0 is `wrong_amount`) in `domain.PaymentFailureReason`.

## Seat map behaviour (M5)

- **Counts** (level 1, level 2, event `seats_available`/`price_from_cents`, category
  counts, the sales dashboard) come from one per-event aggregate kept in memory for 3
  seconds per API process and dropped on publish and cancel. The two event lists accept a
  copy up to 20 seconds old (`Policy.ListCountsTTL`), because a list reads the aggregate of
  every event on its page. Level 3 is never cached.
- **`seats_total`** on events, stands, blocks and categories counts every inventory row,
  blocked ones included. The sales heatmap's block `total` leaves blocked seats out.
- **Markers.** The server reads the database clock before it selects seats and puts that
  time, the event and the block into the marker. On `?since=` it returns every seat of the
  block written after `marker − 20 s`, plus seats whose hold ran out in that span. The 20
  seconds are the point: `updated_at` is stamped when a row is written, not when its
  transaction commits, and the pool's statement and idle-in-transaction limits (15 s each)
  bound how long that gap can be. A client therefore sees a recent change more than once,
  which is harmless. The answer is `full` instead when the marker is unreadable, for
  another event or block, from the future, older than 5 minutes, or when the event row
  itself changed in the same span (a category edit touches it).
- **Rate limit.** Public event and seat map reads share 12,000 requests a minute per IP
  (`RateLimits.PublicReadPerIP`): loose on purpose, because a stadium of phones shares a
  few addresses and each polls an open block every 5 seconds.

## Holds behaviour (M6)

- **All or nothing.** `POST /holds` holds every seat asked for or none. A seat that is held,
  sold, blocked, not part of the event, or does not exist is listed in
  `details.unavailable_seat_ids`, in ascending order. A loser of a race may be told about one
  seat or several of its seats, never about a seat it did not ask for.
- **Order of checks:** body (400), the event's lock (409 `EVENT_BUSY` when an organiser's
  publish, cancel or edit keeps it for more than 3 seconds), event exists and is not a
  draft (404), event on sale and inside the sales window (409), ticket limit (422), then
  the seats (409). Best available checks the category (404) after the limit.
- **Clock.** `expires_at`, `remaining_seconds`, the sales window and "has this hold run out"
  are all judged by the database clock. `remaining_seconds` is rounded down.
- **Status.** `status` is the effective one: an `ACTIVE` hold past `expires_at` is reported
  `EXPIRED` at once, whether or not the sweeper has closed it, and is left out of
  `GET /me/holds`. Its seats are shown as available on the seat map and can be held by anyone.
- **Seats of an ended hold.** `seats` lists what belongs to the hold now. A hold that is
  `EXPIRED` or `RELEASED` has an empty list and zero amounts; a `CONVERTED` one still lists
  its (sold) seats.
- **Amounts.** `subtotal_cents`, `fee_cents` and `total_cents` are an estimate at the
  categories' prices now; the booking copies the binding prices. The fee is `BOOKING_FEE_BPS`
  basis points of the subtotal in whole cents, rounded down (default 0).
- **Ticket limit.** Counts seats in the caller's live holds, in `PAID` bookings, and in a
  `PENDING_PAYMENT` booking that has a payment in flight. Each seat counts once.
  `details.remaining` is how many more may be held now.
- **Best available.** Neighbouring seats are consecutive seat numbers of one row of one
  block. STANDARD seats only, unless the category has no run of them at all (then
  restricted-view seats may be included); accessible and companion seats are never handed
  out. `details.available` counts every free seat of the category, of any type.
- **Release.** Frees only seats still held by that hold. Releasing a hold whose time had
  already run out records it as `EXPIRED` (its unpaid booking `EXPIRED`); otherwise
  `RELEASED` (booking `CANCELLED`). 409 `PAYMENT_IN_PROGRESS` while a payment is `INITIATED`
  or `PENDING`.
- **Rate limit.** 20 hold creations a minute per user and 3,000 per IP, `POST /holds` and
  `POST /holds/best-available` counted together. Reading and releasing are not limited.
- **`SOLD_OUT`.** A hold is refused with `EVENT_NOT_ON_SALE` for a stored `SOLD_OUT` event,
  as the contract says. Nothing sets that status yet (see "Not in the contract").

## Tickets behaviour (M7)

- **The token.** `qr_token` is `TLT1.<payload>.<signature>`, always 160 characters; the
  layout, the offline checks in order and a worked example with a test key are in
  `docs/TICKET_FORMAT.md`. It is signed once, when the ticket is issued, and stored; the
  same text comes back on every read, also after the signing key is changed.
- **QR size.** 160 characters at error correction M is QR version 9: 53 x 53 modules, 61
  with the quiet zone. The app should draw level M too.
- **Owner only.** `GET /tickets/{ticketId}` and `/qr` find a ticket only for the fan whose
  booking it belongs to; anyone else's and a made-up ID are the same 404. `ticketId` that
  is not a UUID is 400 (field `ticketId`, rule `id`). Staff roles get 403.
- **`GET /me/tickets`.** Soonest kickoff first, then seat (inventory id). Every status is
  listed unless `status` is sent. `event.status` is the effective status, as on events.
  `location.gate_group` is the gate group signed into the token. Answers that carry
  `qr_token` are sent `Cache-Control: private, no-store`.
- **`GET /tickets/{ticketId}/qr`.** A black-on-white PNG, `size` x `size` pixels (128 to
  1024, default 512, about 1.4 KB), with a four-module quiet zone. 60 requests a minute per
  user (429); a drawn image is kept in a small in-process cache (1,024 images) by token
  and size, since a ticket's token never changes. Modules are a whole
  number of pixels, so there is a little extra white when `size` is not a multiple of 61.
  It is drawn for any status: a void ticket's QR is harmless, the manifest refuses it.
- **Web ticket page, `GET /v1/t/{webToken}`.** HTML for every status (200, 404, 429, 500).
  Reads no access token. Shows the booking reference, the event (times in Nairobi time)
  and each seat; a `VALID` ticket with its QR inline, a `USED` or `VOID` one marked so,
  without a QR. A link of the wrong shape, an unknown link and the link of a booking with
  no tickets get the same 404 page. Headers: `Cache-Control: private, no-store`,
  `X-Robots-Tag: noindex, nofollow, noarchive`, `Referrer-Policy: no-referrer`,
  `X-Frame-Options: DENY`, and a Content-Security-Policy of `default-src 'none'` with only
  `data:` images and the page's one style block allowed. 300 requests a minute per IP. Its
  QR images come from the same cache as the image endpoint.
- **Ticket keys at startup.** A public key that is not a canonical Ed25519 point, or is
  one of the eight small-order points, is refused (in `TICKET_VERIFY_KEYS` and as the
  signing key's public half): under such a key one fixed signature verifies any ticket.
  A `TICKET_VERIFY_KEYS` entry that is the signing seed itself is refused too (the list is
  sent to gate devices). The seed of a *retired* key pasted there cannot be told from a
  public key: both are 32 bytes.
  Startup errors about `TICKET_SIGNING_KEY_ID` and `TICKET_VERIFY_KEYS` name the variable
  and the entry's position and never repeat the value.
- **SMS.** After a payment commits the fan who made the booking gets "Talanta: you're
  going! Booking TLT-XXXXXX, 2 tickets: `<PUBLIC_BASE_URL>/v1/t/<web token>`" (154
  characters with the production origin). No provider is wired yet: the mock logs the text
  in development and only "not sent" in production, where the ticket service logs a warning
  "ticket sms NOT delivered" (booking id only, no link) instead of reporting it as sent.
- **Issue and void are not endpoints.** Tickets are made inside the payment transaction
  (`ticketing.IssueForBooking`) and voided by a refund (`VoidForBooking`) or an event
  cancel. Statuses move only as `domain.TransitionTicket` allows: `VALID` to `USED`, `VOID`
  or `TRANSFERRED`; `USED` to `VOID`; nothing leaves `VOID`.
- **Contract changes made with this work:** `getWebTicket` declares a 500 (HTML) and its
  description says what the page shows; `GateManifest.keys[].key_id` has the pattern
  `^k[1-9][0-9]{0,4}$`; the `uploadScans` example token is the worked example of
  `docs/TICKET_FORMAT.md`; `Ticket.qr_token` says it is 160 characters.

## Bookings and payments behaviour (M7)

- **Booking.** `POST /bookings` needs `Idempotency-Key` (8 to 128 of `A-Z a-z 0-9 - _`);
  missing or malformed is 400 `IDEMPOTENCY_KEY_REQUIRED` with `details.fields` naming
  `Idempotency-Key`. The same key again answers 200 with the same booking, also after the
  hold has ended. Another key for a hold that has a live booking is 409
  `HOLD_ALREADY_BOOKED`; the same key for another hold is 422 `IDEMPOTENCY_KEY_REUSED`. Two
  requests arriving together for one hold make one booking, whatever keys they carry.
- **Money.** `items[].unit_price_cents` is copied from the seat's category when the booking
  is made and never changes. `fee_cents` is `BOOKING_FEE_BPS` basis points of the subtotal
  **rounded to the nearest whole shilling** (a multiple of 100 cents; half a shilling rounds
  up): the same sum the hold showed. So any fee rate gives a payable booking
  (`BOOKING_FEE_BPS=250` on a KES 1,500 seat is KES 38.00, not 37.50). `total_cents` is what
  M-Pesa is asked for. M-Pesa takes whole shillings: a booking whose total is not a multiple
  of 100 cents is refused with 409 `BOOKING_NOT_PAYABLE`, never rounded. Neither a fee
  nor a price can cause that any more: a category price with cents in it is refused when it
  is set (rule `whole_shillings`). The refusal stays as the last guard.
- **`hold_expires_at`** is set while the booking is `PENDING_PAYMENT` and null afterwards.
  `latest_payment` is the newest attempt, of any status. `tickets` is empty until `PAID`.
- **Owner only.** `GET /bookings/{reference}` and `GET /payments/{paymentId}` find a row
  only for the fan it belongs to; anyone else's is the same 404 as one that does not exist.
  A reference of the wrong shape is 400 (field `reference`). Staff roles get 403.
- **`GET /me/bookings`.** Newest first, every status unless `status` is sent. Answers that
  can carry ticket QR texts are sent `Cache-Control: private, no-store`.
- **Starting a payment.** `phone` may be `07…`, `01…`, `2547…` or E.164, Kenyan (`+254`)
  only; anything else is 400 on `phone`. The first payment pushes the hold's expiry out to
  6 minutes, once. Any payment needs 2 minutes left on the hold; with less it is 409
  `HOLD_EXPIRED` whose `details.hold_status` is `ACTIVE`. A booking with a live payment
  answers 409 `PAYMENT_IN_PROGRESS` naming it. 30 bookings-plus-payments a minute per user.
  What M-Pesa does with the request has three answers:
  - it accepts: **202**, payment `PENDING`;
  - the request provably did not reach it, or it refused (no connection, a 4xx, Daraja's own
    error): **502** `PAYMENT_PROVIDER_UNAVAILABLE`, the attempt is kept as a `FAILED`
    payment (`failure_reason: other`) and the fan can retry at once;
  - we cannot tell (it timed out after it was sent, the answer was unreadable): **202**,
    payment `PENDING`, and `result_desc` is "We are checking with M-Pesa whether the
    request went through. Do not pay again yet." The fan may or may not get a prompt. A
    second payment is refused with `PAYMENT_IN_PROGRESS` until this one resolves (by its
    callback, or `TIMEOUT` after 5 minutes), so the fan cannot pay twice. Clients should
    show that text when `status` is `PENDING` and `result_desc` is not null.
- **`Payment` no longer carries `checkout_request_id` or `merchant_request_id`.** They were
  removed from the schema: the CheckoutRequestID is the key callbacks are matched by, and
  no client used it for anything.
- **How a payment ends** (the same table is in the contract under the STK operation):

  | What happened | `Payment.status` | `failure_reason` | `booking_status` |
  |---|---|---|---|
  | Paid, seats sold, tickets issued | `SUCCESS` | null | `PAID` |
  | Insufficient funds, wrong PIN, cancelled, prompt timed out | `FAILED` | that reason | `PENDING_PAYMENT` while the hold lives, else `EXPIRED` |
  | No answer from M-Pesa within 5 minutes | `TIMEOUT` | `timeout` | as above |
  | **Paid the wrong amount** | **`FAILED`** | **`wrong_amount`** | **`REFUND_REQUIRED`** |
  | Paid, but the seats were gone | `SUCCESS` | null | `REFUND_REQUIRED` |
  | Paid twice (a second payment for a `PAID` booking) | `SUCCESS` | null | `PAID`: tickets kept, that payment refunded |

  So `status: SUCCESS` alone does not mean tickets: read `booking_status`. A `FAILED`
  payment with `failure_reason: wrong_amount` has `result_code` 0: M-Pesa itself reported a
  different amount, and that amount is in the refund queue. (Against real Daraja this cannot
  happen: its query names no amount and a prompt cannot be paid in part or in excess. The
  mock's `wrong_amount` outcome is the provider reporting a different amount.) One more row:
  **paid, but the tickets could not be issued** is `SUCCESS` / `REFUND_REQUIRED`, refunded.
- **Polling.** Stop when `status` is `SUCCESS`, `FAILED` or `TIMEOUT`. **Neither `FAILED`
  nor `TIMEOUT` is always the last word**: if M-Pesa later confirms the money arrived, the
  payment becomes `SUCCESS` (seats sold if the hold still has them all, otherwise a refund).
  The fan may start another payment meanwhile; `GET /bookings/{reference}` always shows the
  truth, and is what the app resumes from after being closed.
- **Late money.** When a payment succeeds after its hold ran out: if nobody took a seat of
  the hold and no sweeper closed it, the seats are sold and the booking is `PAID` as if it
  had been on time. If any seat was taken by someone else, or the hold was closed, nothing
  is sold: payment `SUCCESS`, booking `REFUND_REQUIRED`, a refund for the amount paid, an
  organiser alert. The same when the event was cancelled meanwhile. No seat is ever sold
  twice to make the ledger come out right.
- **The callback, `POST /v1/webhooks/mpesa/callback`.** 200
  `{"ResultCode":0,"ResultDesc":"Accepted"}` for any body from any address; no token is read.
  A callback is taken up only when it comes from `MPESA_CALLBACK_ALLOWED_CIDRS` and is
  Daraja's envelope (at most 64 KiB; a `ResultCode` that does not fit 32 bits is not one).
  Then:
  - **It is written down before the 200 is sent** (`payment_callbacks`, the inbox). The work
    runs after the answer and, if it cannot finish (the event is busy, Daraja cannot be
    asked, the API restarts), the worker's drain job (`CALLBACK_DRAIN_INTERVAL`, 5 s) tries
    again with a growing wait, for 7 days, then raises a `PAYMENT_UNRESOLVED` alert. If the
    callback cannot be written down the answer is **503**, not "Accepted".
  - **The body is a hint, never the authority.** A "paid" is confirmed with Daraja's
    authenticated STK query before anything is sold or refunded; if the query cannot be
    made the payment stays as it is and the callback waits. A failure is taken from the body
    when its code is a documented final failure (1, 1001, 1019, 1025, 1032, 1037, 2001,
    9999); any other non-zero code is checked with Daraja and, if Daraja has no final
    answer, left to reconciliation.
  - **`Amount` is never money.** A confirmed payment is paid in full at the amount we
    requested. A body naming another amount raises an `AMOUNT_MISMATCH` alert ("no refund
    was queued for the callback's figure") and changes nothing else. A forged
    `Amount: 900000.00` no longer queues a refund of KES 900,000.
  - **A success for a `FAILED` or `TIMEOUT` payment is applied** (money received after
    all). A failure for a `SUCCESS` payment is ignored.
  - **A "paid" for an ID no payment has** is stored (`unmatched_callbacks`), raises a
    `PAYMENT_UNRESOLVED` alert naming the receipt, amount and phone, and is matched to a
    payment that has no M-Pesa ID by phone + amount + time (started within 24 hours before
    it arrived, exactly one candidate). The match alone decides nothing: the ID is written
    on the payment only once Daraja confirms that ID was paid; then the normal path runs.
    A failure for an unknown ID is logged and dropped.
  - With `PAYMENT_PROVIDER=none` every callback is ignored (200, nothing stored).
  The body is kept in the inbox, and in `payments.raw_callback` once its result has been
  applied. `Amount` is read as a decimal number of shillings, never through a float.
- **Reconciliation** (worker, every `RECONCILE_INTERVAL`, 30 s), in this order:
  1. Live payments with **no M-Pesa ID** (no call to Daraja is needed, so a slow Daraja
     cannot hold them up). Provably never sent: `TIMEOUT` at 60 seconds, no alert. Possibly
     sent: left until 5 minutes in case the callback comes, then `TIMEOUT` **with** a
     `PAYMENT_UNRESOLVED` alert naming the phone and amount to look for.
  2. Live payments with an ID, 60 seconds old: asked about with STK query and settled as a
     callback would have settled them. Only a code Daraja documents as a final failure
     closes a payment; "still under processing" (4999) and unknown codes are "ask again".
     After 5 minutes with no final answer: `TIMEOUT` and a `PAYMENT_UNRESOLVED` alert.
  3. **`TIMEOUT` payments with an ID are asked about again**: every 5 minutes for 24 hours,
     then daily until they are 7 days old. Daraja's last word reaches the same finishing
     function with no callback at all.
  A run stops starting Daraja calls after a minute; the rest waits for the next run.
- **Release and the sweeper.** `DELETE /holds/{id}` is 409 `PAYMENT_IN_PROGRESS` while a
  payment is live. The sweeper frees a lapsed hold's seats but leaves a booking with a live
  payment `PENDING_PAYMENT`; when that payment fails the booking becomes `EXPIRED`, and if
  it succeeds it is refunded.
- **Alerts** (`GET /admin/alerts`, newest first). Each carries `event_title` as well as
  `event_id`. `REFUND_REQUIRED`: a payment bought nothing, or was a second payment for a paid
  booking. `AMOUNT_MISMATCH`: M-Pesa reported a wrong amount (refund queued), or a callback
  named an amount other than the one requested (no refund queued; check the statement).
  `PAYMENT_UNRESOLVED`: we do not know what became of some money (no answer in 5 minutes; a
  request that may have reached M-Pesa under an ID we never learned; a payment M-Pesa
  reports that matches no booking; a callback that could not be confirmed in 7 days).
  `acknowledged=false` gives the open ones; acknowledging twice keeps the first time.
  **The system closes an alert itself when what it asks for is done**, in the same
  transaction, and says so in `resolution` (null for an open alert and for one a person
  acknowledged):
  - a `PAYMENT_UNRESOLVED` alert, when its payment later ends in tickets ("Resolved
    automatically: the payment succeeded and the tickets were issued.") or in a queued
    refund ("…a refund is queued for it.");
  - a `REFUND_REQUIRED` or `AMOUNT_MISMATCH` alert, when the refund of its payment is marked
    as paid back ("Resolved automatically: the refund was marked as paid back.").
  An alert a person already acknowledged is never rewritten, and an alert with no payment
  behind it (an unmatched callback that stays unmatched) is only ever closed by a person.
  An unmatched callback's alert is filed under the event its phone last paid for, or the
  newest event, because an alert must belong to an event; its message says it may not be
  that event's.
- **Refunds** (`GET /admin/refunds`, oldest first; `status`, `event_id` and
  `booking_reference` filter it). One per payment that has to go back, for the amount M-Pesa
  confirmed; `phone` is the number that paid; `booking_status` is the booking's status now.
  Event cancel queues one per successful payment the same way.
  `POST /admin/refunds/{id}/complete` records that it was paid by hand: the refund is
  `DONE`; a `REFUND_REQUIRED` booking with no refund left pending becomes `REFUNDED` and any
  ticket it still has is voided; a `PAID` booking (duplicate payment) stays `PAID` with its
  tickets. It answers 200 every time; `already_completed` is `false` on the call that did
  the work and `true` on a repeat (the field is only in this operation's answer). **Seats
  are not touched**: a `REFUND_REQUIRED` booking of a live event owns no sold seat, and a
  cancelled event's sold seats stay `SOLD` (the event is off). There is no way to refund one
  paid booking of a live event and put its seats back on sale.
- **Sales summary.** `refund_required_cents` is every refund of the event still pending,
  whatever its booking's status (a second payment on a booking that stays `PAID` counts),
  plus the total of a `REFUND_REQUIRED` booking with no refund row yet.
- **Mock payments (development and test only).** `X-Mock-Outcome` on the STK call:
  `success`, `insufficient_funds`, `wrong_pin`, `user_cancel`, `timeout`, `no_callback`
  (the worker's reconciliation finds the success after a minute), `duplicate_callback`,
  `late_callback` (success after `MOCK_LATE_CALLBACK_DELAY`, 7 minutes: by then the
  payment is `TIMEOUT` and the hold has expired), `wrong_amount`. Without the header the
  phone numbers `+254700000101` … `+254700000108` play the same list after `success`; any
  other number succeeds. The callback arrives after `MOCK_CALLBACK_DELAY` (2 s). In
  production the header is 403 `MOCK_NOT_ALLOWED` before anything else is read, and the
  mock provider cannot be configured.
- **Contract changes made with the money-path review** (after the M7 work):
  - `Payment`: **`checkout_request_id` and `merchant_request_id` removed** (generated
    TypeScript type changes in mobile and admin).
  - `Alert`: `event_title` (string) and `resolution` (string or null) added, both required.
  - `Refund`: `booking_status` added (required); `already_completed` (boolean, optional,
    only in the answer of `completeRefund`).
  - `listRefunds`: optional query parameter `booking_reference`.
  - `mpesaCallback`: a **503** answer (`MpesaCallbackNotStored`) when a real callback could
    not be stored.
  - `startMpesaStkPush`: no schema change; 202 now also covers "may have reached M-Pesa"
    (`result_desc` set on a `PENDING` payment), 502 means "provably not sent or refused".
  - Descriptions only: `fee_cents` (whole shillings), `AlertKind`, `Payment.failure_reason`,
    `Payment.result_desc`, `Refund.amount_cents`, category prices, the outcome table.
- **Contract changes made with the M7 work** (descriptions unless said): `createBooking`,
  `startMpesaStkPush` gained `EVENT_BUSY` in their 409 list and `completeRefund` gained a
  409 answer (`EVENT_BUSY`), the one change that adds a response to an operation;
  the STK operation documents the outcome table, the 2-minute rule, phone forms and the
  magic numbers; `Payment.status`, `Payment.failure_reason`, `PaymentStatus`,
  `PaymentFailureReason`, `BookingStatus`, `MockOutcome`, `AlertKind`, `HoldStateDetails`,
  `getPayment`, `mpesaCallback` and `completeRefund` say what the code does. No schema
  gained, lost or renamed a field.

## Gate mode (M10)

Three operations, GATE_STAFF and ADMIN only (a fan or an organiser gets 403, also for a
path under `/gate` that does not exist). Answers are sent `Cache-Control: no-store`.

### Gate events, `GET /gate/events`

Events stored as `PUBLISHED`, `ON_SALE` or `SOLD_OUT` whose kickoff is later than 24 hours
ago, soonest first, at most 50. `event.status` is the effective status, as everywhere.
`gate_groups` is one `<stand code>-<tier>` per stand of the venue, sorted. `ticket_count`
counts `VALID` and `USED` tickets. A cancelled event drops off the list; its manifest still
answers.

`manifest_version` is **not** a manifest version. It is a change marker: a digest of the
event's details, the key set, the gate groups and the number of tickets per gate group and
status. It is the same from one listing to the next exactly when none of those changed,
so a device may use it to skip a sync. Sent as `since` it gets a full manifest.

### Manifest, `GET /gate/events/{eventId}/manifest[?since=<version>]`

- **Keys.** `keys[]` is every public key the server accepts ticket signatures from
  (`TICKET_SIGNING_KEY`'s public half and `TICKET_VERIFY_KEYS`), 32 bytes each as 43
  characters of base64url. Public keys only: the gate service is built from the verifier
  and is never handed the signer. (With no key configured in development the one key is
  `k0`, which is outside the contract's `key_id` pattern.)
- **Full** (no `since`, `full: true`): every `VALID` (`V`) and `USED` (`U`) ticket. The
  device replaces its set.
- **Changes only** (`since`, `full: false`): every ticket written after the version, in
  any status. `X` (void) and `T` (transferred) mean "remove it". Each tuple is the ticket's
  status now, so applying an answer twice, or two overlapping answers, is harmless.
- **`gate_group_index`** points into the `gate_groups` of the same answer. Store the text.
- **`ticket_count`** is read in the same database snapshot as `tickets`: after applying the
  answer the device holds exactly that many tickets. If it does not, download in full.
- **Draft** events and unknown ids are 404. A cancelled or completed event answers (full:
  no tickets; changes: every ticket as `X`).

**How a version works.** It is the database clock read before anything else, with the event
id, in an opaque text. A ticket's change stamp (`tickets.updated_at`) is set when the row
is written, not when its transaction commits, so a transaction can commit a stamp that is
older than a version already handed out. A changes-only answer therefore starts **two
minutes before** the version it is given: longer than any transaction that writes tickets
can stay open (15 s statement and idle limits; an event cancel, the longest, is cut off at
55 s). The price is that a ticket scanned or sold in the last two minutes is sent again on
every sync, about 50 bytes each. The answer is the full manifest instead when `since` is
unreadable, is for another event, is from the future, or is older than 7 days.

**Measured** (seeded Talanta, every seat sold: 59,568 tickets, 8 gate groups; laptop, no
race detector, through the whole router):

| Answer | Raw | gzip | Server time |
|---|---|---|---|
| Full manifest | 2,800,203 B (2.80 MB) | 1,285,494 B (1.29 MB), 21.6 B a ticket | 570 to 810 ms, of which 250 to 270 ms the query and building the list |
| Changes only, 100 tickets changed | 5,208 B | 2,510 B | 77 to 102 ms |

The ticket ids are random, which is why gzip stops at 46%. The changes-only read is an
index scan on `tickets_event_updated_idx` (9 buffers for 100 rows). The full read and the
changes-only `ticket_count` pass over the event's tickets once (a sequential scan when the
event is most of the table, as in the test; 4,255 buffers). No ETag: an unchanged event
costs one changes-only request of a few hundred bytes.

### Scan upload, `POST /gate/scans`

One batch from one device: `device_id` and 1 to 500 scans. More than 500 scans, or a body
over 1 MiB, is 413. One transaction per batch: a 400, 409 or 5xx means nothing of the batch
was stored.

**Idempotency.** A scan whose `client_scan_id` is already stored is not stored again and
not judged again. Its answer is the result the server holds for it now, about the ticket
it was stored with; nothing is taken from the second upload. The same id twice in one
batch is one scan. So a device may resend any batch, any number of times.

**The decision, for a scan that is new.** The first row that applies:

| # | Condition | Result | Ticket |
|---|---|---|---|
| 1 | `token` sent and it does not verify (bad shape, unknown key id, bad signature), or it is for another ticket than `ticket_id` | `INVALID` | untouched |
| 2 | `token` sent, signed for another event than the scan's `event_id` | `WRONG_EVENT` | untouched |
| 3 | `token` sent, signed for another gate group than the scan's `gate_group` | `WRONG_GATE` | untouched |
| 4 | No ticket with that id | `INVALID` (stored all the same; `ticket_status` is null) | none |
| 5 | `token` sent and it is not the exact text the ticket was issued with | `INVALID` | untouched |
| 6 | The ticket belongs to another event than `event_id` | `WRONG_EVENT` | untouched |
| 7 | The ticket's gate group is not `gate_group` | `WRONG_GATE` | untouched |
| 8 | The ticket is `VOID` or `TRANSFERRED` | `INVALID` | untouched |
| 9 | The device's own `result` is not `ADMITTED` | the device's result, unchanged | untouched |
| 10 | The device said `ADMITTED`, and this is the earliest admission of the ticket by `scanned_at` (ties: the smaller `client_scan_id`) | `ADMITTED` | `USED`, `used_at` = that scan time |
| 11 | The device said `ADMITTED`, and an earlier admission exists | `DUPLICATE` | stays `USED` |

Rows 6 and 7 are checked against the ticket's own row, so they hold when no token is sent.
Row 9: the steward turned the person away, and the server never turns a refusal into an
admission afterwards. `overridden` is true when the result differs from what the device
sent. `first_admitted_at` is the ticket's `used_at`.

**When the later scan is uploaded first.** Phone B admits a ticket at 19:01 and syncs:
`ADMITTED`, ticket `USED`, `used_at` 19:01. Phone A, which admitted the same ticket at
19:00, syncs later. By the rule A's scan is the admission: A is answered `ADMITTED`, the
stored result of B's scan is changed to `DUPLICATE`, and `used_at` moves to 19:00. **Phone
B is not told.** It was answered `ADMITTED` and the server has no way to reach it. It
learns only if it uploads that scan again (the answer is then `DUPLICATE`,
`overridden: true`). A device should therefore resend the scans it was last told were
`ADMITTED` once more before it closes a session. Either way two people are inside on one
ticket; the server can name the scans, not undo the entry.

**Scan times are bounded, not trusted.** `scanned_at` is the device's clock. A time in the
future, more than 7 days old, or before the ticket was issued is replaced by the time the
server received the batch (logged as `times_replaced`). A phone whose clock is wrong by
minutes is believed, and can win or lose a tie it should not; the order of two scans from
two phones is only as good as their clocks.

**A ticket voided after it was scanned** (refund, cancelled event). The stored scan keeps
its `ADMITTED` result and the ticket keeps `used_at`; its status is `VOID`. A scan of that
ticket uploaded after the void is `INVALID`, even if it was made before: the server judges
by the status at upload and keeps no history of statuses. The scan log still holds its
`scanned_at`.

**Other answers.** An `event_id` that does not exist is 400 for the whole batch (field
`scans[i].event_id`, rule `event`). While an organiser's cancel or edit holds the event for
more than 3 seconds the answer is 409 `EVENT_BUSY`: send the batch again. Limits: 120
uploads a minute per device (counted per signed-in user and `device_id`) and 3,000 a minute
per user, 429 with `Retry-After`.

**What is stored.** One `scan_logs` row per scan: `client_scan_id`, `ticket_id`, `event_id`
and `gate_id` (the gate group) as the device sent them, `scanned_at` (bounded as above),
`device_id`, `scanned_by` (the signed-in user), `result` (the server's), `synced_at`. The
device's own verdict and the token are not stored; a token is never logged.

**Not uploaded.** A QR that cannot be read far enough to give a ticket id (not `TLT1`, bad
base64, wrong length, unknown version) has nothing to put in `ticket_id` and stays on the
device. There is no endpoint for it.

**A token is optional.** Without one the server relies on the ticket id alone, so a signed-in
gate account could mark any ticket it knows the id of as used; the manifest gives it every
id. Scans carry `scanned_by` and `device_id` for that reason. Send the token.

### The honest limit

Two phones that are both offline cannot see each other's scans. If the same QR (a
screenshot, say) is shown to both before either has synced, both admit it. Nothing on the
server can prevent that: it hears about both scans only afterwards, keeps the earlier as
`ADMITTED`, and marks the other `DUPLICATE`.

What bounds it: a ticket is bound to one gate group. A `W-LOWER` ticket is refused
(`WRONG_GATE`) at every gate that is not `W-LOWER`, offline, from the signature alone. So
the two phones have to be at the same stand and tier; it is not "any two gates of the
stadium". The window closes for a ticket as soon as one phone that admitted it has synced
and the other has synced after that. It is not solved, and the API does not claim it is.

### Contract changes made with this work

- `uploadScans` gained a **409** (`EVENT_BUSY`) and a **429** answer. Generated clients
  gain those two error cases; no success type changes.
- `getTicketQr` gained a **429** answer (60 a minute per user).
- Descriptions only, no field added, removed or renamed: `listGateEvents` (which statuses,
  24 hours, at most 50), `getGateManifest` (the two-minute look-back, 7 days, indexes,
  sizes, 404 for a draft), `uploadScans` (the decision order, results that change later,
  scan time bounds, unknown event), `GateEventListItem.manifest_version` (**its meaning
  changed**: a change marker, not a version to send as `since`), `GateManifest.keys`,
  `gate_groups`, `ticket_count`, `Scan.scanned_at`, `first_admitted_at`.

## Test phone numbers (`OTP_TEST_CODES`)

A server can name up to 20 phone numbers that always get a fixed sign-in code. It is for
signing in to the app before an SMS provider is connected, and it works in production.

- `POST /auth/otp/request` for a test number answers exactly as for any phone. The code is
  never in the answer, and no SMS is sent.
- `POST /auth/otp/verify` is unchanged: the code is stored hashed, expires after 5 minutes,
  works once per request, and has the same limit of 5 wrong attempts.
- Test numbers do not count against the hourly and daily SMS caps. The per-phone limits
  (one request a minute, five an hour) still apply.
- A code that is one digit repeated or a run such as 123456 is refused at startup.
- The API logs a warning at startup with how many test numbers are set, never the codes.
