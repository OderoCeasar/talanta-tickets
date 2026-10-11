// Types and API client shared by the Expo app and the admin web app.

/** Every API route sits under this prefix (see backend/internal/httpapi/router.go). */
export const API_PREFIX = "/v1";

export interface Health {
  status: "ok" | "degraded";
}

/**
 * `baseUrl` is the API origin with no trailing slash. The admin passes "" because it is served
 * from the same origin as the API; the mobile app passes EXPO_PUBLIC_API_URL.
 */
export async function getHealth(baseUrl: string): Promise<Health> {
  const res = await fetch(`${baseUrl}${API_PREFIX}/healthz`);
  return (await res.json()) as Health;
}

export { colors } from "./theme";

export { createApiClient } from "./api/client";
export type { Api, ApiClient, ApiClientOptions, AuthSession, TokenStore } from "./api/client";
export { ApiError, fieldErrors, isApiErrorBody, unwrap } from "./api/errors";
export type {
  ApiErrorBody,
  ApiErrorCode,
  ApiErrorDetails,
  ApiFieldError,
  ClientErrorCode,
  FieldErrors,
} from "./api/errors";
export type { components, operations, paths } from "./api/schema";
