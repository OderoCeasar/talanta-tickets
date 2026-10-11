# Decisions

One line each: what was chosen and why. Newest at the bottom.

- **Keep `/v1` prefix on every route.** The admin site is served from the same origin and its
  client routes would collide with API paths; production Caddy already proxies `/v1/*`.
- **HTTP package is `internal/httpapi`, not `internal/http`.** A package named `http` shadows
  `net/http` in every file that imports both.
- **Migration `000001` is never edited.** The deploy workflow applies migrations on every push
  to `main`, so it may already be applied in production. Corrections go in later migrations.
- **Statuses are checked text, not Postgres enums.** `000001` already uses `CHECK` constraints,
  and adding a value to a check is a plain, reversible migration.
- **Gate group is `<stand code>-<tier>`, for example `W-LOWER`.** It follows directly from the
  stand a seat is in, which is what correction 8 binds a ticket to.
- **Booking fee is zero unless `BOOKING_FEE_BPS` is set.** The architecture has a `fee_cents`
  column but names no fee; basis points keep the sum in integers.
- **`make lint` runs `gofmt` and `go vet` only.** staticcheck v0.8.1 (the newest release)
  cannot type-check the Go 1.27 standard library ("method must have no type parameters" in
  `math/rand/v2`), and golangci-lint embeds the same checker. Add staticcheck back when it
  supports 1.27.
- **API clients come from `openapi-typescript` + `openapi-fetch`, generated into
  `packages/shared`.** Types only, no generated runtime, and one copy serves both apps.
- **The worker is a second binary (`cmd/worker`), built for local use by
  `backend/Dockerfile.dev`.** The sweeper and reconciliation never share a process with request
  handling. The production `Dockerfile`, `deploy/` and `.github/workflows/` are owned by the
  project owner and are not changed by this build; running the worker in production is listed
  in `docs/STATUS.md` as a step for them.
- **Deployment files are off limits.** `deploy/`, `.github/workflows/`, `backend/Dockerfile`
  and `mobile/eas.json` are already wired to production, and a push to `main` deploys. This
  build does not edit them and does not push.
