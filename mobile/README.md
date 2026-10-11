# Talanta Tickets — mobile app

The Expo app fans use to pick seats, pay with M-Pesa and show their tickets. Expo SDK 57,
React Native 0.86, expo-router, TypeScript strict.

Gate staff sign in to the same app; scanning mode is not built yet (milestone M10), so a
`GATE_STAFF` user sees a "gate mode is coming" screen instead of the fan tabs.

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

### Signing in without SMS

When the API is not in production, `POST /auth/otp/request` returns the code as `dev_code`.
The app shows it in a box marked "Development only" with a "Use this code" link. In
production the field is never sent and the box never appears.

### Test payments

Development builds (`__DEV__`) show a "mock outcome" picker on checkout. It sends the
`X-Mock-Outcome` header with the STK push request. Production builds never send the header.

## Checks

```sh
pnpm --filter @talanta/mobile typecheck
pnpm --filter @talanta/mobile test        # vitest, pure logic only
pnpm --filter @talanta/mobile exec expo-doctor
pnpm --filter @talanta/mobile exec expo export --platform android --output-dir /tmp/talanta-export
```

The unit tests cover the code with no React Native imports: phone normalisation, money and
Nairobi-time formatting, the hold countdown, error wording, the seat geometry and grid
index (tap hit-testing on rotated and curved blocks), applying a poll delta to the cached
block, how each seat is drawn, pan/zoom limits, and the QR matrix.

## How it is put together

```
app/                        expo-router screens
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
  gate.tsx                  placeholder for gate staff
src/api/                    the shared typed client, tokens, TanStack Query hooks, small local storage
src/features/seatmap/       geometry + grid index, draw batches, SVG and Skia canvases, gestures, polling
src/features/booking/       resume banner
src/features/tickets/       QR drawing, ticket row
src/lib/                    phone, money, time, countdown, error wording
src/store/bookingFlow.ts    zustand: the seat selection and the hold expiry, nothing else
src/ui/                     buttons, cards, banners, states, icons
```

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
- The idempotency key is made once, when the hold is made, and stored with the hold ID. It
  is sent on every `POST /bookings` for that hold, including after a restart.
- After a restart the app asks `GET /me/holds` (seats still held) and
  `GET /bookings/{reference}` (the last checkout's booking) and shows what the server says.

### Tickets

`GET /me/tickets` is saved to the phone on every good answer. With no network the saved copy
is shown with an "Offline copy" banner. The ticket screen raises screen brightness while it
is open and shows a ticking clock and a moving strip, so a screenshot is visibly stale.
