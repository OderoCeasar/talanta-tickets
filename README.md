# Talanta Tickets

Ticketing for Talanta Stadium, Nairobi. Fans pick seats on a map and pay with M-Pesa in a
mobile app, organisers run events from a web site, and gate staff scan tickets even with no
network. One Go backend and one Postgres database serve all three.

The design is in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). What is built, what is not, and
the known limits are in [docs/STATUS.md](docs/STATUS.md).

**The stadium layout is made up.** No official seating plan was available, so the seeder
builds a believable one: four stands (North, South, East, West), each with a Lower and an
Upper tier, 96 blocks and 59,568 sellable seats, with VIP and VVIP in the West stand. Do not
read it as the real stadium.

| Path | What |
|------|------|
| `backend/` | Go API and worker (chi, pgx, sqlc), migrations, the OpenAPI contract |
| `mobile/` | Fan and gate-staff app (Expo, expo-router) |
| `admin/` | Organiser site (React, Vite) |
| `packages/shared/` | API types generated from the contract, and the client both apps use |
| `deploy/` | Production compose file and Caddy block |
| `docs/` | Architecture, API notes, ticket format, decisions, status |

## Run it locally

Needs Docker, Node 22+, pnpm 9 and Go 1.27.

```bash
make dev        # Postgres, migrations, API on http://localhost:8080, worker, Adminer on :8081
make seed       # the venue, a template, two demo events and one login per role
make install    # JS dependencies
make admin      # organiser site on http://localhost:5173 (proxies /v1 to the API)
make mobile     # Expo dev server
```

`make seed` prints the demo logins. They exist only on a local development database:

| Role | Sign in with |
|---|---|
| Admin | `admin@talanta.test` / `admin-dev-password` on the organiser site |
| Organiser | `organiser@talanta.test` / `organiser-dev-password` on the organiser site |
| Fan | any Kenyan phone number in the app; the sign-in code is shown on screen in development |
| Gate staff | `+254700000002` in the app |

Locally no money moves and no SMS is sent. Payments go through a mock that plays M-Pesa, and
sign-in codes and ticket links are written to the API log. Both shortcuts are refused when
`APP_ENV=production`.

The app reads the API address from `mobile/.env.local`; see [mobile/README.md](mobile/README.md)
for the emulator and a real phone.

`make help` lists the other targets.

## Try it

**Organiser.** Sign in at http://localhost:5173. Create an event from the "Talanta default"
template, set prices on the Pricing tab, check the Publish tab says everything is in place,
and publish. Publishing the full venue takes 30 to 45 seconds. Open the Sales tab and leave it
open.

**Fan.** In the app, open the event, choose a stand and a block, pick seats and continue.
Sign in with a phone number, check the total and the countdown, and pay. In a development
build a small picker chooses what the mock payment does (paid, wrong PIN, not enough money,
cancelled, timed out and so on). After a paid one the tickets appear with their QR codes, and
the organiser's Sales tab moves without a reload.

**Gate staff.** Gate staff download an event's ticket list before the match and then scan with
no network; scans upload when the network is back. The backend for this is built and tested.
The scanning screens in the app are listed in `docs/STATUS.md`.

## How it stops two fans getting the same seat

Holding seats is one database transaction. It locks the requested seat rows in ID order, so
two requests for the same seat take turns and two requests for overlapping seats cannot
deadlock. If any requested seat is gone the whole request is refused with the list of seats
that were lost, and nothing is held. A hold lasts ten minutes. Expired holds are reclaimed by
the next request that wants the seat, so correctness does not depend on the background
sweeper having run.

A payment that succeeds sells the seats only if every one of them is still held by that hold.
If not, nothing is sold, the booking is marked for refund and the organiser is alerted. The
system will record a refund it owes before it will sell a seat twice.

The test for this fires 1,000 hold requests from parallel clients at 10 seats and expects
exactly 10 winners, 990 refusals and no seat held twice. With the row lock removed the same
test fails.

## Tests

```bash
make test               # backend unit tests
make test-integration   # unit + integration tests; starts Postgres 16 in Docker
make lint               # gofmt, go vet, and TypeScript typecheck for the three JS workspaces
pnpm -r test            # JS unit tests
```

The integration tests run against a real Postgres, never a mocked one, and take about 20
minutes with the race detector on.

## The API contract

`backend/docs/openapi.yaml` is the contract. Tests check real responses against it, and
`make openapi-clients` regenerates the TypeScript types both apps use, so neither app
hand-writes a request type. Behaviour that does not fit in the contract (error codes, the
seat map's compact format, payment outcomes, gate rules) is in
[docs/API_NOTES.md](docs/API_NOTES.md). The ticket QR format, with a worked example, is in
[docs/TICKET_FORMAT.md](docs/TICKET_FORMAT.md).

## Configuration

Every setting comes from the environment. `backend/.env.example` lists them all with what
each is for. An unset `APP_ENV` means production, and in production the API refuses to start
if a required secret is missing. `docs/STATUS.md` has the list of what a production server
needs.

## Limits worth knowing before relying on it

These are stated in full in `docs/STATUS.md`. The ones that matter most:

- **M-Pesa has never been called.** The Daraja client is tested against a fake server only.
  It needs a sandbox run with real credentials before any money is taken.
- **No SMS provider is connected.** In production, sign-in codes and ticket links are not
  delivered, so fans cannot sign in there yet.
- **Two offline gate phones cannot see each other's scans.** Both would admit the same
  ticket. Tickets are bound to a gate group (a stand and tier), so that window is one gate
  group and not the whole stadium. It is reduced, not solved.
- **A ticket's QR code is static.** A screenshot of it scans. The app shows a live clock on
  the ticket so a screenshot looks stale to a steward.
- **Refunds are recorded, not sent.** The organiser sends the money back in the M-Pesa
  portal and marks the refund as done.

## Deploying

Pushing to `main` deploys the backend and the organiser site; mobile builds are started by
hand from the Actions tab. Setup steps are in [docs/deployment.md](docs/deployment.md).
