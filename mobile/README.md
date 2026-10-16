# Talanta Tickets — mobile app

The Expo app fans use to pick seats, pay with M-Pesa and show their tickets. Expo SDK 57,
React Native 0.86, expo-router, TypeScript strict.

Gate staff sign in to the same app: a `GATE_STAFF` or `ADMIN` user lands in gate mode
instead of the fan tabs (see "Gate mode" below).

## Run it

Everything is run from the repository root (pnpm workspace, `node-linker=hoisted`).

```sh
pnpm install

# 1. Tell the app where the API is. The file is git-ignored.
echo 'EXPO_PUBLIC_API_URL=http://10.0.2.2:8080' > mobile/.env.local

# 2. Start Metro. Port 8081 is Metro's default; pass --port if something else has it.
pnpm --filter @talanta/mobile exec expo start --go --android
```

`EXPO_PUBLIC_API_URL` is the API **origin**, with no `/v1` and no trailing slash. The app
adds `/v1` itself. It is read at bundle time, so restart Metro after changing it.

| Where the app runs | Value |
|---|---|
| Android emulator, API on your machine | `http://10.0.2.2:<port>` (the emulator's name for the host; `localhost` is the emulator itself). This is the default when the variable is unset, with port 8080. |
| A real phone on the same Wi-Fi | `http://<your machine's LAN address>:<port>` |
| EAS preview and production builds | set in `eas.json` (not edited here) |

Every native module the app uses (Skia, reanimated, gesture-handler, react-native-svg,
secure-store, brightness, crypto, async-storage) is in **Expo Go for SDK 57**, so no native
build is needed for development. `expo run:android` (a development build) also works.

Android emulator from the command line:

```sh
export ANDROID_HOME=$HOME/Android/Sdk
$ANDROID_HOME/emulator/emulator -avd Medium_Phone_API_36.1 -no-window -no-audio -no-boot-anim &
```

### Seeing the welcome slides again

They show once per phone, to someone signed out, and the flag survives sign-out. To see them
as a new fan would, clear the app's storage (`adb shell pm clear host.exp.exponent` in Expo
Go), or open Profile → "How Talanta Tickets works".

### Signing in without SMS

When the API is not in production, `POST /auth/otp/request` returns the code as `dev_code`.
The app shows it in a box marked "Development only" with a "Use this code" link. In
production the field is never sent and the box never appears.

### Test payments

Development builds (`__DEV__`) show a "mock outcome" picker on checkout. It sends the
`X-Mock-Outcome` header with the STK push request. Production builds never send the header.

### Fonts and icon

Inter (body, labels, buttons, inputs, tab bar) and Plus Jakarta Sans (titles, headers,
headline prices) are bundled from `@expo-google-fonts/*`; nothing is fetched at run time.
The family names live in `src/fonts.ts`, one per weight, and text styles in `src/theme.ts`
name a family and never set `fontWeight`. `useFonts` in `app/_layout.tsx` registers them
(Expo Go); the `expo-font` plugin in `app.json` embeds the same files in a real build.

The icon, Android adaptive layers, splash mark and favicon are rendered from the SVGs in
`assets/src/`:

```sh
node mobile/assets/src/render.mjs                 # rewrites mobile/assets/*.png
node mobile/assets/src/render.mjs --preview <dir> # also writes 512 px previews
```

Expo Go shows its own launcher icon; the app's icon and splash appear in a development or
release build.

## Checks

```sh
pnpm --filter @talanta/mobile typecheck
pnpm --filter @talanta/mobile test        # vitest, pure logic only
pnpm --filter @talanta/mobile exec expo-doctor
pnpm --filter @talanta/mobile exec expo export --platform android --output-dir /tmp/talanta-export
```

The unit tests cover the code with no React Native imports: phone normalisation, money and
Nairobi-time formatting, the hold countdown, error wording, the seat geometry and grid
index (tap hit-testing on rotated and curved blocks), seat labels, checkout amounts from
the hold or the booking, payment outcomes, applying a poll delta to the cached
block, how each seat is drawn, pan/zoom limits, and the QR matrix.

## How it is put together

```
app/                        expo-router screens
  index.tsx                 where the app opens (src/lib/startRoute.ts): welcome, events or gate
  welcome.tsx               four swipeable slides on first launch; Profile replays them
  (tabs)/events|tickets|profile
  sign-in.tsx               phone number, then the 6-digit code
  event/[id]/index.tsx      event detail
  event/[id]/map.tsx        level 1: the stadium
  event/[id]/stand/[standId].tsx   level 2: one stand
  event/[id]/block/[blockId].tsx   level 3: the seats
  event/[id]/best.tsx       best available
  checkout/[holdId].tsx     summary, countdown, pay, payment status
  booking/[reference].tsx   confirmation with the tickets
  ticket/[id].tsx           one ticket with its QR
  gate/index.tsx            gate mode: choose event and gate, ticket list, sync, end session
  gate/scan.tsx             the scanner: camera, typed code, full-screen result
  gate/history.tsx          this phone's scans, what is uploaded, what the server changed
src/api/                    the shared typed client, tokens, TanStack Query hooks, small local storage
src/features/seatmap/       geometry + grid index, draw batches, SVG and Skia canvases, gestures, polling
src/features/booking/       resume banner
src/features/tickets/       QR drawing, ticket row, upcoming/past grouping
src/features/welcome/       the welcome slides' drawings (SVG, no image files)
src/features/gate/          ticket check (token, verdict), manifest, scanner, upload queue: pure;
                            sqlStore (SQLite), device (API calls), GateProvider (screen state)
src/lib/                    phone, money, time, countdown, checkout amounts, error wording
src/fonts.ts                the font family names (one per weight); src/fontAssets.ts the files
assets/                     icon, adaptive icon layers, splash mark, favicon; assets/src the SVGs
src/store/bookingFlow.ts    zustand: the seat selection and the hold expiry, nothing else
src/ui/                     buttons, cards, banners, states, icons, the Seats → Pay → Tickets step bar
```

## Gate mode

For stewards (`GATE_STAFF`) and admins. The rules it follows are `docs/TICKET_FORMAT.md`
section 4 and "Gate mode" in `docs/API_NOTES.md`.

**Using it**

1. Sign in with the steward's number. The app opens in gate mode.
2. Choose the event, then the gate this phone is at (`N-LOWER`, `W-UPPER`, ...), then
   **Download tickets and start**. Do this with a connection: a full stadium is about
   1.3 MB. The screen then shows how many tickets are on the phone and when the list was
   last updated (Nairobi time), and warns if the phone's clock is more than two minutes off
   the server's.
3. **Start scanning.** Point the camera at a ticket's QR code. The whole screen turns green
   (let them in) or red with the reason: already used, wrong gate (and which gate the ticket
   is for), wrong event, not a valid ticket. The phone vibrates differently for each. The
   result clears by itself, or on a tap. **Type a code** is for a broken camera.
4. Scanning needs no network. Every scan is written to the phone before the result is
   shown, and uploads by itself when the connection returns, when the app comes back to the
   front, and every 20 seconds while gate mode is open. "N waiting to upload" is on the
   home screen and under the camera. **Sync now** uploads and refreshes the ticket list.
5. **History** lists this phone's scans for the event, which are uploaded, and under
   "Changed after sync" the ones the server decided differently (for example another phone
   scanned the ticket first).
6. **End session** before putting the phone away: it uploads what is waiting and asks the
   server once more about the scans it had accepted, which is the only way to learn that
   one of them was later given to another phone's earlier scan.

**What to know**

- The ticket list and the scans are in a SQLite file (`talanta-gate.db`), not AsyncStorage:
  a scan looks one ticket up by ID and changes one row out of about 60,000. The file
  belongs to the phone, not the account: scans that have not uploaded survive a sign-out
  and upload when a gate account is signed in again.
- The check is done on the phone with the public keys from the manifest
  (`@noble/curves`, Ed25519). The phone holds no secret.
- A scan leaves the upload queue only when a `200` lists its `client_scan_id`.
- Uploading runs while the app is open. It is not an operating-system background task: a
  phone left in a pocket with the app closed uploads when the app is next opened.
- Two phones that are both offline cannot see each other's scans; a ticket shown to both is
  let in twice and the server marks the later one afterwards. Tickets are bound to one gate
  group, so both phones have to be at the same stand and tier.
- An emulator's camera shows a made-up room, not a ticket; use **Type a code** there.

Rules the code follows:

- **API types are generated.** Requests go through `createApiClient` from `@talanta/shared`
  (openapi-fetch typed by `backend/docs/openapi.yaml`). Nothing here declares a request or
  response shape by hand.
- **Server data lives in TanStack Query.** zustand holds only the tapped seat IDs and the
  hold's `expires_at` (plus the server/phone clock difference).
- **Tokens are in `expo-secure-store`.** A 401 triggers one quiet refresh and one retry;
  if the refresh is refused the app signs out.
- **Money is integer cents.** `formatKes` splits shillings and cents with integer
  arithmetic; it throws on a non-integer instead of rounding.
- **Times are shown in Africa/Nairobi** whatever the phone's zone (a fixed UTC+3; Kenya has
  no daylight saving).

### Seat map

- Levels 1 and 2 are a handful of `react-native-svg` polygons plus a list of the same
  stands/blocks underneath (for large fonts and screen readers).
- Level 3 is one component, `SeatCanvas`. Up to 1,200 seats it draws with react-native-svg;
  above that with Skia. Both read the same "scene" (`layers.ts`): seats that look the same
  are batched, so SVG gets about a dozen `<Path>` nodes and Skia one recorded `<Picture>`.
  There is no React component and no touch handler per seat.
- Pinch and pan are gesture-handler worklets writing reanimated shared values. SVG applies
  them as a view transform (the SVG bitmap is drawn oversized so it stays sharp); Skia
  replays the picture under a `Group` transform. Neither path re-renders React while moving.
- One tap gesture covers the canvas. The worklet turns the screen point into block
  coordinates and calls JS once; `seatAt` finds the seat in a grid index (nine cells read).
  When seats are too small to hit, the first tap zooms in instead.
- While the block screen is focused and the app is in the foreground it polls
  `?since=<marker>` every `poll_interval_seconds` and patches the cached block
  (`applyDelta`); a `kind: "full"` answer replaces it.

### Checkout

- The countdown is `expires_at` minus the phone's clock, corrected by the difference the
  hold's `remaining_seconds` reveals. It is re-read from the server when the app returns to
  the foreground and when it reaches zero.
- The total shown comes from the hold (`subtotal_cents`, `fee_cents`, `total_cents`). **No
  booking exists until the fan taps Pay.** Once it does, the booking's amounts are shown
  (`src/lib/checkout.ts`).
- The idempotency key is made once, when the hold is made, and stored with the hold ID. It
  is sent on every `POST /bookings` for that hold: the first Pay, every retry, and after a
  restart.
- What a payment means is read from three fields in order: `booking_status`, `status`,
  `failure_reason` (`paymentOutcome` in `src/lib/errors.ts`). `SUCCESS` alone is never
  "tickets"; a `TIMEOUT` is polled slowly because M-Pesa can still settle it.
- `EVENT_BUSY` (an organiser is publishing or editing) is shown as "The organiser is
  updating this event. Try again in a moment." with a Try again that keeps the selection.
  Under two minutes left on the hold (`HOLD_EXPIRED` with `hold_status: ACTIVE`) releases the
  seats and returns to the map with a line saying why.
- After a restart the app asks `GET /me/holds` (seats still held) and
  `GET /bookings/{reference}` (the last checkout's booking) and shows what the server says.

### Tickets

`GET /me/tickets` is saved to the phone on every good answer. With no network the saved copy
is shown with an "Offline copy" banner. The ticket screen raises screen brightness while it
is open and shows a ticking clock and a moving strip, so a screenshot is visibly stale.
