# API notes

Companion to `backend/docs/openapi.yaml`, which is the contract. This file holds what is
easier to read as a table: error codes, the compact array layouts, the one-letter codes, and
where the API differs from `docs/ARCHITECTURE.md` §10 and §11.

72 operations on 63 paths. Lint: `pnpm --package=@redocly/cli@2.62.1 dlx redocly lint backend/docs/openapi.yaml`.

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
`GET /healthz` 503 (`{ "status": "degraded" }`), `GET /t/{webToken}` (HTML), and
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
| `HOLD_EXPIRED` | 409 | The hold ran out | `hold_id, hold_status, expires_at` |
| `HOLD_NOT_ACTIVE` | 409 | The hold was released or already paid for | same |
| `HOLD_ALREADY_BOOKED` | 409 | The hold has a booking made with a different idempotency key | `booking_reference, booking_status` |
| `BOOKING_NOT_PAYABLE` | 409 | Booking is not `PENDING_PAYMENT` | `booking_reference, booking_status` |
| `PAYMENT_IN_PROGRESS` | 409 | A payment for the booking is still live | `payment_id, payment_status` |
| `EVENT_NOT_READY` | 409 | Publish refused | `problems[]` (same as readiness) |
| `INVALID_STATE_TRANSITION` | 409 | Publish or cancel not allowed from the current status | `current_status` |
| `EVENT_NOT_EDITABLE` | 409 | Event is past the status where this edit is allowed | `current_status` |
| `BLOCK_HAS_INVENTORY` | 409 | Block is used by a published event; cannot delete or regenerate | `event_ids[]` |
| `CATEGORY_IN_USE` | 409 | Category has inventory or bookings | `sold, held` |
| `CONFIRMATION_REQUIRED` | 409 | Price change after sales started needs `confirm: true` | `sold, held` |
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
| `PAYMENT_PROVIDER_UNAVAILABLE` | 502 | M-Pesa did not accept the STK request | null |

Wrong password and wrong TOTP code on signed-in routes are 422, not 401, so a client that
refreshes its token on any 401 does not mistake them for an expired session.

429 is declared on the auth endpoints, the public reads, holds, bookings and payments. Admin
and gate routes are not rate limited.

## Validation details

`details.fields[].field` is the JSON name of the request body field (`email`,
`new_password`, `seat_ids[2]`, `inner.name`), or the query, path or header name
(`limit`, `cursor`, `invitationId`, `Content-Type`). Two special values: `body` (the body is
missing, is not JSON, or is not a single object) and the name of an unknown field.

| `rule` | Meaning |
|---|---|
| `required` | Missing or empty |
| `email`, `e164`, `number`, `len`, `min`, `max`, `oneof`, `gte`, `lte` | The value breaks that rule |
| `type` | Wrong JSON type (a number where a string is expected) |
| `unknown` | The body has a field the endpoint does not accept |
| `json` | The body is not valid JSON |
| `content_type` | The body was not sent as `application/json` |
| `id`, `range`, `cursor` | Path ID, `limit`, or `cursor` is malformed |

## Sign-in behaviour the contract does not spell out

- **Phones.** Clients should send E.164 (the contract's `Phone` pattern). The server also
  accepts the Kenyan local forms `07…`, `01…` and `2547…`/`2541…`, with spaces or dashes, and
  stores and returns E.164, so one number is always one account.
- **OTP limits.** One code per phone per 60 s, five per phone per hour, five verify
  attempts per code (the sixth answers 429 `OTP_ATTEMPTS_EXCEEDED` even with the right
  code). A new code retires the old one. A staff account is never signed in by phone code.
- **Refresh.** 400 only for a malformed body; 401 only when the token is unknown, expired,
  revoked or reused; 429 leaves the token unused and still good. A stale `Authorization`
  header on `/auth/*` is ignored. Two refreshes racing with the same token count as reuse
  and end the session, so a client must not refresh in parallel.
- **Access token claims:** `sub` (user id as a string), `role`, `sid` (session), `iat`,
  `exp`, `jti`. HS256.
- **Staff sign-in limits.** Ten password attempts per email per 15 minutes (cleared by a
  good sign-in), thirty per IP. An `mfa_token` lives 5 minutes, allows five codes and works
  once; a TOTP code that has signed the user in is refused if replayed.
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
replaces its cached block. The marker is opaque; the server builds it from
`event_seats.updated_at` (correction 9).

### Seat status letters

| Code | Meaning |
|---|---|
| `A` | Available |
| `H` | Held by someone else |
| `M` | Held by you (only with a FAN token that owns the hold) |
| `S` | Sold |
| `B` | Blocked |

Who holds a seat is never sent. A lapsed hold that the sweeper has not cleared yet is `A`.

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
that changed, including `X` and `T`, which the device removes.

### Points and polygons

A point is `[x, y]`; a polygon is an array of points, closed, last point not repeated.

## Estimated sizes (synthetic data, not yet measured on the seeded venue)

| Payload | Raw | Gzipped |
|---|---|---|
| Level 3, 300 seats | 11 KB | 1.7 KB |
| Level 3, 1,200 seats | 43 KB | 5.2 KB |
| Level 3, 2,000 seats | 71 KB | 8.0 KB |
| Manifest, 60,000 tickets | 2.8 MB | 1.4 MB |

Level 3 meets the 40 KB target raw up to about 1,150 seats, and at every size once gzipped.
The manifest is bound by the 16 random bytes in each UUID (0.96 MB at best), so it cannot
get much smaller; it is downloaded once before the match and then by `since`.

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
- Marking an event `COMPLETED`, and moving `PUBLISHED` to `ON_SALE` when sales open. Both
  are expected to be done by the worker, not by a request.
- Blocking and unblocking individual seats after publish.
- A scan of a QR that cannot be read as a ticket ID is not uploaded (`scan_logs.ticket_id`
  is required).
- Disabling or changing the role of a staff user.
