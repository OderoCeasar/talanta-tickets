// The API's error envelope and helpers for reading it. Every type here is derived from the
// generated schema; nothing about the wire format is written by hand.

import type { components } from "./schema";

type Schemas = components["schemas"];

/** `{ error: { code, message, details } }` — the body of every failed request. */
export type ApiErrorBody = Schemas["Error"];
export type ApiErrorCode = Schemas["ErrorCode"];
export type ApiErrorDetails = ApiErrorBody["error"]["details"];
/** One entry of `details.fields` on `VALIDATION_FAILED` (and `ALREADY_EXISTS`, `VENUE_MISMATCH`). */
export type ApiFieldError = Schemas["ValidationDetails"]["fields"][number];

/**
 * Codes the client makes up for failures that never produced an API error body:
 * the request did not reach the server, or what came back was not our envelope
 * (for example a proxy's 502 page while the API restarts).
 */
export type ClientErrorCode = "NETWORK_ERROR" | "UNEXPECTED_RESPONSE";

/** Field name (as the server names it) to the message to show next to that field. */
export type FieldErrors = Record<string, string>;

export function isApiErrorBody(value: unknown): value is ApiErrorBody {
  if (typeof value !== "object" || value === null) return false;
  const error = (value as { error?: unknown }).error;
  if (typeof error !== "object" || error === null) return false;
  const { code, message } = error as { code?: unknown; message?: unknown };
  return typeof code === "string" && typeof message === "string";
}

/** A failed API call, in a shape that is easy to branch on and safe to throw. */
export class ApiError extends Error {
  /** HTTP status, or 0 when there was no response at all. */
  readonly status: number;
  readonly code: ApiErrorCode | ClientErrorCode;
  readonly details: ApiErrorDetails;
  /** From `details.retry_after_seconds`, else the `Retry-After` header. Null when neither is set. */
  readonly retryAfterSeconds: number | null;

  constructor(init: {
    status: number;
    code: ApiErrorCode | ClientErrorCode;
    message: string;
    details?: ApiErrorDetails;
    retryAfterSeconds?: number | null;
  }) {
    super(init.message);
    this.name = "ApiError";
    this.status = init.status;
    this.code = init.code;
    this.details = init.details ?? null;
    this.retryAfterSeconds = init.retryAfterSeconds ?? null;
  }

  /** Builds the error from whatever `openapi-fetch` put in `error`, plus the response. */
  static fromResponse(body: unknown, response: Response): ApiError {
    if (!isApiErrorBody(body)) {
      return new ApiError({
        status: response.status,
        code: "UNEXPECTED_RESPONSE",
        message: `The server answered ${response.status} without an error message.`,
      });
    }
    return new ApiError({
      status: response.status,
      code: body.error.code,
      message: body.error.message,
      details: body.error.details,
      retryAfterSeconds: readRetryAfter(body.error.details, response),
    });
  }

  static network(cause: unknown): ApiError {
    const error = new ApiError({
      status: 0,
      code: "NETWORK_ERROR",
      message: "Could not reach the server.",
    });
    (error as { cause?: unknown }).cause = cause;
    return error;
  }
}

function readRetryAfter(details: ApiErrorDetails, response: Response): number | null {
  if (details && "retry_after_seconds" in details) return details.retry_after_seconds;
  const header = response.headers.get("Retry-After");
  if (header === null) return null;
  const seconds = Number(header);
  return Number.isFinite(seconds) && seconds >= 0 ? seconds : null;
}

/**
 * Per-field messages from a `VALIDATION_FAILED` answer, keyed by the server's field name.
 * Accepts an `ApiError`, a raw error body, or anything else (which gives `{}`), so callers
 * can pass a mutation's error straight in. The first message for a field wins.
 */
export function fieldErrors(error: unknown): FieldErrors {
  const details = error instanceof ApiError ? error.details : isApiErrorBody(error) ? error.error.details : null;
  const out: FieldErrors = {};
  if (!details || !("fields" in details) || !Array.isArray(details.fields)) return out;
  for (const entry of details.fields) {
    if (!(entry.field in out)) out[entry.field] = entry.message;
  }
  return out;
}

/** What `openapi-fetch` resolves with: exactly one of `data` and `error` is set. */
interface FetchResult {
  data?: unknown;
  error?: unknown;
  response: Response;
}

/** The `data` type of the success half of an `openapi-fetch` result. */
type DataOf<R> = R extends { data: infer D; error?: never } ? D : never;

/**
 * Turns an `openapi-fetch` call into "the data, or a thrown `ApiError`", which is the shape
 * TanStack Query wants. A request that never got an answer is thrown as `NETWORK_ERROR`.
 */
export async function unwrap<R extends FetchResult>(call: Promise<R>): Promise<DataOf<R>> {
  let result: R;
  try {
    result = await call;
  } catch (cause) {
    throw cause instanceof ApiError ? cause : ApiError.network(cause);
  }
  if (!result.response.ok) throw ApiError.fromResponse(result.error, result.response);
  return result.data as DataOf<R>;
}
