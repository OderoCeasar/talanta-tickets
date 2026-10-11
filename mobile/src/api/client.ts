import { ApiError, createApiClient, type components } from "@talanta/shared";
import { QueryClient } from "@tanstack/react-query";

import { API_BASE_URL } from "./config";
import { tokenStore } from "./tokens";

export type Schemas = components["schemas"];

type SignedOutListener = () => void;
const signedOutListeners = new Set<SignedOutListener>();

/** The session provider listens here for "the server refused our refresh token". */
export function subscribeSignedOut(listener: SignedOutListener): () => void {
  signedOutListeners.add(listener);
  return () => {
    signedOutListeners.delete(listener);
  };
}

// The shared client does the work: bearer token on every request, one quiet refresh on a 401
// (single flight, because refresh tokens rotate), one retry, then `onSignedOut`.
export const { api, refreshSession } = createApiClient({
  baseUrl: API_BASE_URL,
  tokenStore,
  onSignedOut: () => {
    for (const listener of signedOutListeners) listener();
  },
});

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      // A wrong request stays wrong: only connection trouble and server faults are retried.
      retry: (failureCount, error) => {
        if (error instanceof ApiError && error.status !== 0 && error.status < 500) return false;
        return failureCount < 2;
      },
      retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 8000),
    },
    mutations: { retry: false },
  },
});
