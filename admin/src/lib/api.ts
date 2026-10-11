import { ApiError, createApiClient, type components, type TokenStore } from "@talanta/shared";
import { QueryClient } from "@tanstack/react-query";

export type Schemas = components["schemas"];
export type User = Schemas["User"];
export type StaffRole = Schemas["StaffRole"];

// Where the session lives:
// - The access token (15 minutes) is kept only in this module's memory. A reload loses it,
//   which is fine: it is rebuilt from the refresh token.
// - The refresh token (30 days) is kept in sessionStorage, not localStorage. A long-lived
//   token in localStorage outlives the browser, is shared by every tab, and stays on a
//   shared office computer after the window is closed. sessionStorage is dropped when the
//   tab closes, so closing the tab ends the session, and each tab rotates its own token
//   (two tabs sharing one rotating token would trip the server's reuse check).
const REFRESH_TOKEN_KEY = "talanta.admin.refresh_token";

let accessToken: string | null = null;

// sessionStorage throws when storage is blocked. The site then still works, but a reload
// signs the user out.
function readStoredRefreshToken(): string | null {
  try {
    return window.sessionStorage.getItem(REFRESH_TOKEN_KEY);
  } catch {
    return null;
  }
}

let refreshTokenFallback: string | null = null;

export const tokenStore = {
  getAccessToken: () => accessToken,
  getRefreshToken: () => readStoredRefreshToken() ?? refreshTokenFallback,
  setTokens: (tokens: { accessToken: string; refreshToken: string }) => {
    accessToken = tokens.accessToken;
    refreshTokenFallback = tokens.refreshToken;
    try {
      window.sessionStorage.setItem(REFRESH_TOKEN_KEY, tokens.refreshToken);
    } catch {
      // Kept in memory only; see readStoredRefreshToken.
    }
  },
  clearTokens: () => {
    accessToken = null;
    refreshTokenFallback = null;
    try {
      window.sessionStorage.removeItem(REFRESH_TOKEN_KEY);
    } catch {
      // Nothing was stored.
    }
  },
} satisfies TokenStore;

type SignedOutListener = () => void;
const signedOutListeners = new Set<SignedOutListener>();

/** The session provider listens here for "the server refused our refresh token". */
export function subscribeSignedOut(listener: SignedOutListener): () => void {
  signedOutListeners.add(listener);
  return () => signedOutListeners.delete(listener);
}

// "/v1" with no origin: Vite proxies it in development and Caddy does in production, so the
// site and the API are always the same origin.
export const client = createApiClient({
  baseUrl: "/v1",
  tokenStore,
  onSignedOut: () => signedOutListeners.forEach((listener) => listener()),
});

export const api = client.api;

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: false,
      // Retry only what a retry can fix: no answer at all, or a fault on the server.
      retry: (failures, error) =>
        failures < 2 && error instanceof ApiError && (error.status === 0 || error.status >= 500),
    },
    mutations: { retry: false },
  },
});
