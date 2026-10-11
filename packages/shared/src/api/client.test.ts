import { describe, expect, it, vi } from "vitest";
import { createApiClient, type AuthSession, type TokenStore } from "./client";
import { ApiError, fieldErrors, unwrap, type ApiErrorBody } from "./errors";
import type { components } from "./schema";

const BASE = "http://api.test/v1";

const user: components["schemas"]["User"] = {
  id: 2,
  phone: null,
  email: "admin@talanta.test",
  name: "Site Admin",
  role: "ADMIN",
  totp_enabled: false,
  created_at: "2026-10-01T08:00:00Z",
};

function session(n: number): AuthSession {
  return {
    access_token: `access-${n}`,
    token_type: "Bearer",
    expires_in_seconds: 900,
    refresh_token: `refresh-${n}`,
    refresh_expires_at: "2026-11-10T09:00:00Z",
    user,
  };
}

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

function errorBody(code: ApiErrorBody["error"]["code"], details: ApiErrorBody["error"]["details"] = null): ApiErrorBody {
  return { error: { code, message: code, details } };
}

function memoryStore(initial: { access: string | null; refresh: string | null }) {
  const state = { ...initial };
  const store: TokenStore = {
    getAccessToken: () => state.access,
    getRefreshToken: () => state.refresh,
    setTokens: ({ accessToken, refreshToken }) => {
      state.access = accessToken;
      state.refresh = refreshToken;
    },
    clearTokens: () => {
      state.access = null;
      state.refresh = null;
    },
  };
  return { state, store };
}

interface Seen {
  method: string;
  path: string;
  auth: string | null;
  body: string;
}

/**
 * A fake server. Protected routes accept only `validAccess`; `/auth/refresh` accepts only
 * `validRefresh` and rotates both, the way the real API does.
 */
function fakeServer(opts: { validAccess: string; validRefresh: string; refreshDelayMs?: number }) {
  const seen: Seen[] = [];
  let validAccess = opts.validAccess;
  let validRefresh = opts.validRefresh;
  let generation = 1;

  const fetch = async (request: Request): Promise<Response> => {
    const path = new URL(request.url).pathname.replace("/v1", "");
    const body = await request.text();
    const auth = request.headers.get("Authorization");
    seen.push({ method: request.method, path, auth, body });

    if (path === "/auth/refresh") {
      if (opts.refreshDelayMs) await new Promise((r) => setTimeout(r, opts.refreshDelayMs));
      const sent = (JSON.parse(body) as { refresh_token: string }).refresh_token;
      if (sent !== validRefresh) return json(401, errorBody("REFRESH_TOKEN_INVALID"));
      generation += 1;
      const next = session(generation);
      validAccess = next.access_token;
      validRefresh = next.refresh_token;
      return json(200, next);
    }
    if (path === "/auth/login") return json(401, errorBody("INVALID_CREDENTIALS"));

    if (auth !== `Bearer ${validAccess}`) return json(401, errorBody("TOKEN_EXPIRED"));
    if (path === "/me" && request.method === "GET") return json(200, user);
    if (path === "/me" && request.method === "PATCH") {
      return json(200, { ...user, name: (JSON.parse(body) as { name: string }).name });
    }
    return json(404, errorBody("NOT_FOUND"));
  };

  const count = (path: string) => seen.filter((s) => s.path === path).length;
  return { fetch, seen, count };
}

describe("createApiClient", () => {
  it("attaches the stored access token as a bearer token", async () => {
    const server = fakeServer({ validAccess: "access-1", validRefresh: "refresh-1" });
    const { store } = memoryStore({ access: "access-1", refresh: "refresh-1" });
    const { api } = createApiClient({ baseUrl: BASE, tokenStore: store, onSignedOut: vi.fn(), fetch: server.fetch });

    const { data } = await api.GET("/me");

    expect(data?.email).toBe("admin@talanta.test");
    expect(server.seen).toHaveLength(1);
    expect(server.seen[0].auth).toBe("Bearer access-1");
  });

  it("sends no bearer token to /auth routes and does not refresh on their 401", async () => {
    const server = fakeServer({ validAccess: "access-1", validRefresh: "refresh-1" });
    const { store, state } = memoryStore({ access: "access-1", refresh: "refresh-1" });
    const onSignedOut = vi.fn();
    const { api } = createApiClient({ baseUrl: BASE, tokenStore: store, onSignedOut, fetch: server.fetch });

    const { error, response } = await api.POST("/auth/login", { body: { email: "a@b.test", password: "nope" } });

    expect(response.status).toBe(401);
    expect(error?.error.code).toBe("INVALID_CREDENTIALS");
    expect(server.seen[0].auth).toBeNull();
    expect(server.count("/auth/refresh")).toBe(0);
    expect(onSignedOut).not.toHaveBeenCalled();
    expect(state.refresh).toBe("refresh-1");
  });

  it("refreshes after a 401, stores the new pair and retries the request with its body", async () => {
    const server = fakeServer({ validAccess: "access-1", validRefresh: "refresh-1" });
    const { store, state } = memoryStore({ access: "stale", refresh: "refresh-1" });
    const { api } = createApiClient({ baseUrl: BASE, tokenStore: store, onSignedOut: vi.fn(), fetch: server.fetch });

    const { data, response } = await api.PATCH("/me", { body: { name: "Achieng Odhiambo" } });

    expect(response.status).toBe(200);
    expect(data?.name).toBe("Achieng Odhiambo");
    expect(state).toEqual({ access: "access-2", refresh: "refresh-2" });
    expect(server.seen.map((s) => `${s.method} ${s.path} ${s.auth ?? "-"}`)).toEqual([
      "PATCH /me Bearer stale",
      "POST /auth/refresh -",
      "PATCH /me Bearer access-2",
    ]);
    // The retry must carry the same body as the first attempt.
    expect(server.seen[2].body).toBe(server.seen[0].body);
    expect(JSON.parse(server.seen[1].body)).toEqual({ refresh_token: "refresh-1" });
  });

  it("shares one refresh between requests that fail with 401 at the same time", async () => {
    const server = fakeServer({ validAccess: "access-1", validRefresh: "refresh-1", refreshDelayMs: 20 });
    const { store } = memoryStore({ access: "stale", refresh: "refresh-1" });
    const onSignedOut = vi.fn();
    const { api } = createApiClient({ baseUrl: BASE, tokenStore: store, onSignedOut, fetch: server.fetch });

    const results = await Promise.all([api.GET("/me"), api.GET("/me"), api.GET("/me"), api.GET("/me")]);

    expect(results.map((r) => r.response.status)).toEqual([200, 200, 200, 200]);
    expect(server.count("/auth/refresh")).toBe(1);
    expect(server.seen.filter((s) => s.auth === "Bearer access-2")).toHaveLength(4);
    expect(onSignedOut).not.toHaveBeenCalled();
  });

  it("does not refresh again when another request already replaced the token", async () => {
    const server = fakeServer({ validAccess: "access-1", validRefresh: "refresh-1" });
    const { store, state } = memoryStore({ access: "stale", refresh: "refresh-1" });

    // The slow request leaves with the stale token and gets its 401 only after a faster
    // request has refreshed. It must reuse the new token, not spend the rotated one again.
    let releaseSlow!: () => void;
    const slowGate = new Promise<void>((resolve) => (releaseSlow = resolve));
    let first = true;
    const fetch = async (request: Request) => {
      const isSlow = first && new URL(request.url).pathname.endsWith("/me");
      first = false;
      const response = await server.fetch(request);
      if (isSlow) await slowGate;
      return response;
    };
    const { api } = createApiClient({ baseUrl: BASE, tokenStore: store, onSignedOut: vi.fn(), fetch });

    const slow = api.GET("/me");
    const fast = await api.GET("/me");
    expect(fast.response.status).toBe(200);
    expect(state.access).toBe("access-2");
    releaseSlow();

    expect((await slow).response.status).toBe(200);
    expect(server.count("/auth/refresh")).toBe(1);
  });

  it("retries only once: a second 401 goes back to the caller", async () => {
    const seen: string[] = [];
    const fetch = async (request: Request) => {
      const path = new URL(request.url).pathname;
      seen.push(path);
      if (path.endsWith("/auth/refresh")) return json(200, session(2));
      return json(401, errorBody("UNAUTHENTICATED"));
    };
    const { store } = memoryStore({ access: "access-1", refresh: "refresh-1" });
    const { api } = createApiClient({ baseUrl: BASE, tokenStore: store, onSignedOut: vi.fn(), fetch });

    const { response, error } = await api.GET("/me");

    expect(response.status).toBe(401);
    expect(error?.error.code).toBe("UNAUTHENTICATED");
    expect(seen).toEqual(["/v1/me", "/v1/auth/refresh", "/v1/me"]);
  });

  it("clears the tokens and signs out once when the refresh token is refused", async () => {
    const server = fakeServer({ validAccess: "access-1", validRefresh: "refresh-1" });
    const { store, state } = memoryStore({ access: "stale", refresh: "revoked" });
    const onSignedOut = vi.fn();
    const { api } = createApiClient({ baseUrl: BASE, tokenStore: store, onSignedOut, fetch: server.fetch });

    const results = await Promise.all([api.GET("/me"), api.GET("/me")]);

    expect(results.map((r) => r.response.status)).toEqual([401, 401]);
    expect(results[0].error?.error.code).toBe("TOKEN_EXPIRED");
    expect(state).toEqual({ access: null, refresh: null });
    expect(onSignedOut).toHaveBeenCalledTimes(1);
    expect(server.count("/auth/refresh")).toBe(1);
    // Nothing was retried: two first attempts and one refresh.
    expect(server.seen).toHaveLength(3);
  });

  it("keeps the session when the refresh call fails for a reason that is not the token", async () => {
    const fetch = async (request: Request) => {
      if (new URL(request.url).pathname.endsWith("/auth/refresh")) return json(500, errorBody("INTERNAL"));
      return json(401, errorBody("TOKEN_EXPIRED"));
    };
    const { store, state } = memoryStore({ access: "stale", refresh: "refresh-1" });
    const onSignedOut = vi.fn();
    const client = createApiClient({ baseUrl: BASE, tokenStore: store, onSignedOut, fetch });

    const { response } = await client.api.GET("/me");

    expect(response.status).toBe(401);
    expect(state.refresh).toBe("refresh-1");
    expect(onSignedOut).not.toHaveBeenCalled();
    await expect(client.refreshSession()).rejects.toMatchObject({ code: "INTERNAL", status: 500 });
  });

  it("refreshSession answers null without calling the server when there is no refresh token", async () => {
    const server = fakeServer({ validAccess: "access-1", validRefresh: "refresh-1" });
    const { store } = memoryStore({ access: null, refresh: null });
    const onSignedOut = vi.fn();
    const client = createApiClient({ baseUrl: BASE, tokenStore: store, onSignedOut, fetch: server.fetch });

    await expect(client.refreshSession()).resolves.toBeNull();
    expect(server.seen).toHaveLength(0);
    expect(onSignedOut).not.toHaveBeenCalled();
  });
});

describe("fieldErrors", () => {
  const body: ApiErrorBody = {
    error: {
      code: "VALIDATION_FAILED",
      message: "Some fields are not valid.",
      details: {
        fields: [
          { field: "email", rule: "email", message: "Enter a valid email address." },
          { field: "password", rule: "min", message: "Use at least 10 characters." },
          { field: "password", rule: "max", message: "Second message for the same field." },
        ],
      },
    },
  };

  it("maps each field to its first message, from a raw body or an ApiError", () => {
    const expected = { email: "Enter a valid email address.", password: "Use at least 10 characters." };
    expect(fieldErrors(body)).toEqual(expected);
    expect(fieldErrors(ApiError.fromResponse(body, json(400, body)))).toEqual(expected);
  });

  it("gives nothing for errors without field details", () => {
    expect(fieldErrors(errorBody("INVALID_CREDENTIALS"))).toEqual({});
    expect(fieldErrors(errorBody("RATE_LIMITED", { retry_after_seconds: 30 }))).toEqual({});
    expect(fieldErrors(new Error("boom"))).toEqual({});
    expect(fieldErrors(undefined)).toEqual({});
  });
});

describe("unwrap", () => {
  it("returns the data of a successful call", async () => {
    const server = fakeServer({ validAccess: "access-1", validRefresh: "refresh-1" });
    const { store } = memoryStore({ access: "access-1", refresh: "refresh-1" });
    const { api } = createApiClient({ baseUrl: BASE, tokenStore: store, onSignedOut: vi.fn(), fetch: server.fetch });

    await expect(unwrap(api.GET("/me"))).resolves.toMatchObject({ role: "ADMIN" });
  });

  it("throws an ApiError carrying the code and the retry time of a 429", async () => {
    const fetch = async () =>
      json(429, errorBody("RATE_LIMITED", { retry_after_seconds: 42 }), { "Retry-After": "42" });
    const { store } = memoryStore({ access: null, refresh: null });
    const { api } = createApiClient({ baseUrl: BASE, tokenStore: store, onSignedOut: vi.fn(), fetch });

    const call = unwrap(api.POST("/auth/login", { body: { email: "a@b.test", password: "x" } }));

    await expect(call).rejects.toBeInstanceOf(ApiError);
    await expect(call).rejects.toMatchObject({ status: 429, code: "RATE_LIMITED", retryAfterSeconds: 42 });
  });

  it("reports a request that never got an answer, and an answer that is not our envelope", async () => {
    const { store } = memoryStore({ access: null, refresh: null });
    const down = createApiClient({
      baseUrl: BASE,
      tokenStore: store,
      onSignedOut: vi.fn(),
      fetch: async () => {
        throw new TypeError("fetch failed");
      },
    });
    await expect(unwrap(down.api.GET("/me"))).rejects.toMatchObject({ status: 0, code: "NETWORK_ERROR" });

    const proxy = createApiClient({
      baseUrl: BASE,
      tokenStore: store,
      onSignedOut: vi.fn(),
      fetch: async () => new Response("Bad Gateway", { status: 502 }),
    });
    await expect(unwrap(proxy.api.GET("/me"))).rejects.toMatchObject({ status: 502, code: "UNEXPECTED_RESPONSE" });
  });
});
