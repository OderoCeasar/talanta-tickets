// The phone's side of gate mode: the SQLite file and the three API calls. Everything that
// decides anything is in the pure modules next to this one.

import { ApiError, unwrap, type components } from "@talanta/shared";
import * as SQLite from "expo-sqlite";

import { api } from "../../api/client";
import { clockSkewMs, type FetchManifest } from "./manifest";
import { openSqlStore, type SqlValue } from "./sqlStore";
import type { GateStore } from "./store";
import { outcomeForStatus, type SendBatch } from "./upload";

type Schemas = components["schemas"];

let opened: Promise<GateStore> | null = null;

/** The gate database, opened once. It belongs to the phone, not to whoever is signed in: scans waiting to upload survive a sign-out. */
export function openGateStore(): Promise<GateStore> {
  if (!opened) {
    opened = (async () => {
      const db = await SQLite.openDatabaseAsync("talanta-gate.db");
      return openSqlStore({
        exec: (sql) => db.execAsync(sql),
        run: async (sql, params) => {
          await db.runAsync(sql, params);
        },
        all: (sql, params) => db.getAllAsync<Record<string, SqlValue>>(sql, params),
      });
    })();
    // A failed open is tried again next time, not remembered.
    opened.catch(() => {
      opened = null;
    });
  }
  return opened;
}

export const fetchGateEvents = () => unwrap(api.GET("/gate/events"));

/**
 * One manifest request. The server's `Date` header is read here for the clock check. gzip
 * is asked for and undone by the platform's HTTP stack.
 */
export const fetchManifest: FetchManifest = async (eventId, since) => {
  const sentAt = Date.now();
  let result;
  try {
    result = await api.GET("/gate/events/{eventId}/manifest", { params: { path: { eventId }, query: since ? { since } : undefined } });
  } catch (cause) {
    throw cause instanceof ApiError ? cause : ApiError.network(cause);
  }
  const receivedAt = Date.now();
  if (!result.response.ok || !result.data) throw ApiError.fromResponse(result.error, result.response);
  // openapi-fetch widens the contract's ticket tuples to loose arrays; the JSON is the tuple.
  const manifest = result.data as unknown as Schemas["GateManifest"];
  return {
    manifest,
    receivedAt: new Date(receivedAt).toISOString(),
    clockSkewMs: clockSkewMs(result.response.headers.get("Date"), manifest.generated_at, sentAt, receivedAt),
  };
};

export const sendScanBatch: SendBatch = async (batch) => {
  let result;
  try {
    result = await api.POST("/gate/scans", { body: batch });
  } catch {
    return { kind: "retry", why: "network", retryAfterSeconds: null };
  }
  if (result.response.status === 200 && result.data) return { kind: "ok", results: result.data.results };
  const error = ApiError.fromResponse(result.error, result.response);
  return outcomeForStatus(result.response.status, error.retryAfterSeconds, error.message);
};
