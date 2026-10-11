// Typed API client shared by the admin site and the Expo app. It knows nothing about the
// browser or React Native: where tokens live and what "signed out" means are passed in.

import createClient, { type Client } from "openapi-fetch";
import { ApiError } from "./errors";
import type { components, paths } from "./schema";

export type AuthSession = components["schemas"]["AuthSession"];

type MaybePromise<T> = T | Promise<T>;

/**
 * Where the two tokens are kept. Methods may be async so the mobile app can back this with
 * the device's secure storage.
 */
export interface TokenStore {
  getAccessToken(): MaybePromise<string | null>;
  getRefreshToken(): MaybePromise<string | null>;
  setTokens(tokens: { accessToken: string; refreshToken: string }): MaybePromise<void>;
  clearTokens(): MaybePromise<void>;
}

export interface ApiClientOptions {
  /** API root including the `/v1` prefix, no trailing slash. The admin site passes `"/v1"`. */
  baseUrl: string;
  tokenStore: TokenStore;
  /** Called once the server has refused the refresh token and the stored tokens are gone. */
  onSignedOut: () => void;
  /** For tests. Defaults to the global `fetch`. */
  fetch?: (input: Request) => Promise<Response>;
}

export type Api = Client<paths>;

export interface ApiClient {
  /** `openapi-fetch` client: `api.GET("/me")`, `api.POST("/auth/login", { body })`, … */
  api: Api;
  /**
   * Swaps the stored refresh token for a new pair and stores it. Calls made while one is
   * already running share it.
   *
   * Resolves with the new session; with `null` when there is no refresh token, or the
   * server refused it (the tokens are then cleared and `onSignedOut` has been called).
   * Rejects with an `ApiError` when the server could not be reached or failed, in which
   * case the tokens are kept so a later attempt can still succeed.
   */
  refreshSession(): Promise<AuthSession | null>;
}

/**
 * Everything under `/auth/` is public in the contract (`security: []`): sign-in, refresh,
 * logout and invitations. Those requests carry no bearer token, and a 401 from them is an
 * answer for the caller ("wrong password"), never a reason to refresh.
 */
function isPublicRequest(request: Request, baseUrl: string): boolean {
  // The request URL is absolute even when baseUrl is relative, so compare from the base on.
  const url = request.url;
  const at = baseUrl ? url.indexOf(baseUrl) : -1;
  const path = at >= 0 ? url.slice(at + baseUrl.length) : new URL(url).pathname;
  return path.startsWith("/auth/");
}

export function createApiClient(options: ApiClientOptions): ApiClient {
  const { tokenStore, onSignedOut } = options;
  const baseUrl = options.baseUrl.replace(/\/+$/, "");
  const send = options.fetch ?? ((input: Request) => globalThis.fetch(input));

  // Used only for the refresh call itself, so that it can never recurse into authFetch.
  const plain = createClient<paths>({ baseUrl, fetch: send });

  let refreshInFlight: Promise<AuthSession | null> | null = null;

  async function doRefresh(): Promise<AuthSession | null> {
    const refreshToken = await tokenStore.getRefreshToken();
    if (!refreshToken) return null;

    let result;
    try {
      result = await plain.POST("/auth/refresh", { body: { refresh_token: refreshToken } });
    } catch (cause) {
      throw ApiError.network(cause);
    }

    if (result.data) {
      await tokenStore.setTokens({
        accessToken: result.data.access_token,
        refreshToken: result.data.refresh_token,
      });
      return result.data;
    }

    // 400 and 401 mean the server will never accept this token. Anything else (429, 5xx,
    // a proxy error page) says nothing about the token, so the session is left alone.
    const status = result.response.status;
    if (status !== 400 && status !== 401) throw ApiError.fromResponse(result.error, result.response);

    await tokenStore.clearTokens();
    onSignedOut();
    return null;
  }

  // Single flight. Refresh tokens rotate and the server treats a second use of one as
  // theft (it revokes the whole chain), so two refreshes must never run side by side.
  function refreshSession(): Promise<AuthSession | null> {
    if (!refreshInFlight) {
      refreshInFlight = doRefresh().finally(() => {
        refreshInFlight = null;
      });
    }
    return refreshInFlight;
  }

  function withBearer(request: Request, token: string | null): Request {
    if (token) request.headers.set("Authorization", `Bearer ${token}`);
    return request;
  }

  async function authFetch(request: Request): Promise<Response> {
    if (isPublicRequest(request, baseUrl)) return send(request);

    // A request body can be read only once, so keep a copy for the retry.
    const retry = request.clone();
    const sentWith = await tokenStore.getAccessToken();
    const first = await send(withBearer(request, sentWith));
    if (first.status !== 401) return first;

    // If the stored token is no longer the one this request went out with, another request
    // has already refreshed (or the session has ended) while this one was in the air.
    let current = await tokenStore.getAccessToken();
    if (current === sentWith) {
      const session = await refreshSession().catch(() => null);
      if (!session) return first;
      current = session.access_token;
    }
    if (!current) return first;

    // One retry only: if this also answers 401 the caller gets that 401.
    return send(withBearer(retry, current));
  }

  return {
    api: createClient<paths>({ baseUrl, fetch: authFetch }),
    refreshSession,
  };
}
