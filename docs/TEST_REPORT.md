# Test report

What was run, the exact counts, what failed, and what could not be run. Failures and gaps
come first. Dated 2026-10-12, on the code of branch `backend/gate`.

## Failures and gaps

- **The load test misses the speed target.** p95 was 1.7 to 2.8 seconds against the 300 ms
  of ARCHITECTURE §14. Correctness passed on every run. Numbers and the reason are under
  "Load test".
- **M-Pesa has never been called.** Every payment test runs against the mock provider or a
  fake Daraja server. Real OAuth, STK push, STK query, callback delivery and Safaricom's
  callback addresses are untested.
- **No real phone.** The app ran on an Android 16 emulator in Expo Go only. Smooth zoom and
  pan on the largest block, pinch, and the camera were not judged on a device, and no
  installable build was made.
- **The second load test of the QA brief was not run**: many users across many blocks,
  reading the seat map and booking, for a few minutes.
- **Gate scanning in the app was run with typed-in codes, not the camera.** The emulator has
  no camera feed. The journey passed on a 27-ticket manifest; a full-stadium manifest was
  measured off the device only.
- **Cross-process and long-running behaviour is thin.** Rate limits and the mock's state
  were tested in one process; no test ran the sweeper or reconciliation for more than a few
  seconds of load, or with two API instances.

## Machine

Every number below is from one laptop: Intel Core i5-8350U (4 cores, 8 threads), 16 GB,
with Postgres 16 in Docker on the same machine and the load generator beside it. The CPU
governor was `powersave` and the cores were running near 0.8 GHz. **These are not production
numbers.**

## Backend suite

`go test -race -tags=integration ./...` against real Postgres 16 (testcontainers), on the
exact tree of `backend/gate`: **22 packages, all passed, 0 failures.** The same suite passed
on `backend/payments-tickets` alone (21 packages; the gate package is not on it).

It takes about 20 minutes. Test packages start their own Postgres; on a busy machine that
start-up once exceeded the default 60 s wait and failed whole packages before any assertion
ran, so the wait is now 150 s.

## Seat holds (ARCHITECTURE §6)

Real parallel transactions through the real service, 64 at a time, race detector on. The
suite was run 10 times in a row by the lead and passed 10 times.

| Test | Requests | Winners | Refused | Other errors | Seats held twice |
|---|---|---|---|---|---|
| 1,000 requests at 10 seats | 1,000 | 10 | 990 | 0 | 0 |
| 1,000 requests, groups of 3, disjoint | 1,000 | 4 | 996 | 0 | 0 |
| 1,000 requests, overlapping windows of 3 | 1,000 | 2 to 4 | the rest | 0 | 0 |
| `{5,9}` against `{9,5}`, 300 rounds | 600 | 1 per round | 1 per round | 0 deadlocks | 0 |
| 4 seats asked, 1 already taken | 1 | 0 seats held | 409 names that 1 seat | | |
| Expired hold taken over, sweeper off | 200 | 10 | 190 | 0 | 0 |
| One user, two requests that together break the limit, 100 rounds | 200 | 1 per round | 1 per round | 0 | |
| Best available, 50 groups of 4 on exactly 50 runs | 50 | 50 | 0 | 0 | 0 |
| Sweeper running beside live holds | 600 | 300 | 300 | 0 | 0 |
| Holds racing an event cancel | 1,000 | 118 to 258 | the rest | 0 | 0 held after cancel |

**Shown able to fail.** With `FOR UPDATE` removed from the seat lock, the first test went
red: 19 to 21 winners for 10 seats, and seats with 2 and 4 winners. With the per-user
advisory lock removed, both requests won in 99 of 100 rounds. With the "all or nothing"
rollback removed, a hold of 1 seat was granted where 3 were asked.

## Payments (ARCHITECTURE §8.2)

One passing test for each row of the failure table:

| Failure | Result |
|---|---|
| Callback never arrives | Reconciliation settles it; with no answer for 5 minutes it times out and alerts |
| Callback arrives twice | The second changes nothing |
| Callback after the hold expired and the seats were resold | Payment recorded, booking `REFUND_REQUIRED`, refund queued, alert raised; the other fan's seats untouched |
| Wrong amount | Nothing sold; alert raised |
| Forged callback (unknown ID, disallowed address, forged forwarding header, replayed receipt, no amount, junk) | Nothing changes |
| Fan closes the app mid-payment | `GET /bookings/{ref}` shows the truth at each stage |

Also passing: a double-tapped Pay makes one booking (24 identical requests at once; 6
different keys at once, 8 rounds); two callbacks at the same instant sell once (12 rounds of
6); callback and reconciliation at once sell once (20 rounds); a success after a failure or
a timeout is applied, not dropped; an STK push whose answer was lost stays open and blocks a
second payment; a stored callback is retried until applied.

**Shown able to fail.** Each of these was broken on purpose and seen red: the "already
final" check, the amount check, the success-after-failure path, the lost-answer path and the
timed-out re-query. Two guards on a sale (the seat count check and the `hold_id` condition)
each stayed green when broken alone, because the ticket precondition also refuses; both went
red when the second guard was removed too.

Every payment test ends by checking the ledger: a sold seat has exactly one paid booking,
one converted hold and one live ticket; money received with no tickets has a refund.

## Tickets and gate

- A good token verifies; any single changed character in the payload or signature fails; a
  token signed with another key, for another event, or for another stand is refused with the
  right result.
- The worked example in `docs/TICKET_FORMAT.md` is regenerated by a test and compared with
  the document.
- Two devices admitting the same ticket offline: after both upload, one is `ADMITTED` and
  one `DUPLICATE`, by scan time, whichever uploads first. The same batch twice changes
  nothing.
- A fan cannot fetch another fan's ticket or QR (404). The signing key appears in no
  response or log line.

Full manifest for 59,568 tickets: 2,800,203 B raw, 1,285,494 B gzipped, 0.6 to 0.8 s on the
server. A `since` request with 100 changed tickets: 5,208 B raw, 0.1 s.

## Load test

`make loadtest`: 500 fans, each a different signed-in user, each asking for one of the same
50 seats, all released at the same instant against the real HTTP API.

| Run | Sent | Holds won | Refused (409) | 5xx | Seats held twice | p50 | p95 | p99 |
|---|---|---|---|---|---|---|---|---|
| 1 | 500 | 50 | 450 | 0 | 0 | 1.85 s | 2.50 s | 2.55 s |
| 2 | 500 | 50 | 450 | 0 | 0 | 1.50 s | 2.20 s | 2.25 s |
| 3 | 500 | 50 | 450 | 0 | 0 | 1.21 s | 1.84 s | 1.92 s |
| 4 (pool of 32) | 500 | 50 | 450 | 0 | 0 | 2.08 s | 2.79 s | 2.83 s |
| 5 (pool of 32) | 500 | 50 | 450 | 0 | 0 | 1.10 s | 1.82 s | 1.84 s |
| 6 (pool of 32) | 500 | 50 | 450 | 0 | 0 | 1.12 s | 1.73 s | 1.76 s |

After each run the database showed 50 of 50 seats held by 50 different active holds, no hold
owning no seat, and sold + held + available + blocked equal to the 59,568 seats of the event.

**Correctness: passed, six runs of six.** Exactly 50 successes, 450 clean 409s, zero double
holds, zero server errors.

**Speed: failed. p95 is 1.7 to 2.8 s, not under 300 ms.** Why:

- One hold request alone took 65 ms on this machine, and 8 at once took 165 ms each. A hold
  is several statements across five lock steps (event gate, event row, the per-user lock,
  the seat rows, then the write), each a round trip to Postgres in Docker.
- 500 of them released in the same instant then queue behind 8 slow hardware threads shared
  with Postgres and the load generator. The whole burst finishes in about 2 seconds, which
  is roughly 200 to 270 holds a second.
- It is not the connection pool: raising it from the default 8 to 32 changed nothing
  (runs 4 to 6).
- The fans who lose do not wait long on the winner; the time is the queue for CPU.

What would bring it down: a server with real cores at full clock speed; folding the hold
into fewer round trips (one statement or a stored function); and spreading a burst over a
second with a waiting room, which ARCHITECTURE §13 already lists as future work. None of
these was done.

The test creates its 500 fans in the database and signs their tokens with the development
secret, because signing 500 fans in through the OTP endpoint from one address is stopped by
the per-address sign-in limit. It refuses to run against anything but a local database.

## Journeys through the real API

| Journey | Run by | Result |
|---|---|---|
| Fan signs in, holds 4 seats, books, pays through the mock, gets 4 tickets | lead, with curl; and the app on the emulator | Passed |
| Two fans go for the same seats | lead, with curl; and the app | One wins, the other gets 409 naming exactly the seats lost |
| Fan pays with the wrong PIN, then retries | lead, with curl; and the app | Failed payment, then paid, one booking |
| Payment whose callback never arrives | lead, with curl; and the app | Settled by the worker after about 65 s |
| Fan pays late after the seats were resold | backend engineer, with curl | One sale, one `REFUND_REQUIRED`, alert visible to the organiser |
| Organiser creates, prices and publishes an event; sales numbers follow bookings | organiser site in a real browser | Passed; a full-venue publish took 37.9 s |
| Admin invites a person, who sets a password and signs in | organiser site in a real browser | Passed, 11 steps |
| Admin acknowledges an alert and marks a refund as paid back | organiser site in a real browser | Passed |
| Gate staff download the manifest, scan a ticket, scan it again, upload | backend engineer, with curl | `ADMITTED`, then `DUPLICATE`; wrong gate and a tampered token refused |
| Gate staff scan with the network off in the app, then reconnect | the app on the emulator, typed-in codes | Valid ticket green; repeat, changed character, wrong stand and wrong event all red; 14 scans uploaded and matched in the database |
| Fan holds seats and does nothing: the seats become free again | integration tests only | Passed (not run by hand) |
