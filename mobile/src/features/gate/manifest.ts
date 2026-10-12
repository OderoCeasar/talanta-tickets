// Bringing the device's ticket set up to date from a manifest answer
// (docs/TICKET_FORMAT.md section 4.1). Pure: the store and the request are passed in.

import type { components } from "@talanta/shared";

import type { GateStore, GateTx, HeldStatus, ManifestKey, TicketRow } from "./store";
import { decodePublicKey } from "./token";

type Manifest = components["schemas"]["GateManifest"];

/** Tickets written per statement, so progress can be shown and no statement is huge. */
export const MANIFEST_CHUNK = 5000;

export class ManifestError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ManifestError";
  }
}

/** Thrown inside the write so it is rolled back; never leaves this module. */
class CountMismatch extends Error {}

export interface ApplyOptions {
  /** Device time the answer arrived, UTC ISO. */
  receivedAt: string;
  clockSkewMs: number | null;
  onProgress?: (done: number, total: number) => void;
}

export interface ApplyResult {
  /** False: the answer did not add up and nothing was written. Download in full. */
  applied: boolean;
  full: boolean;
  /** Tickets the device holds for the event now. */
  count: number;
  /** Tickets in the answer. */
  changes: number;
}

function checkedKeys(manifest: Manifest): ManifestKey[] {
  if (manifest.keys.length === 0) throw new ManifestError("The manifest has no signing key.");
  return manifest.keys.map((k) => {
    if (k.algorithm !== "Ed25519" || !decodePublicKey(k.public_key)) throw new ManifestError(`The manifest's key ${k.key_id} is not a usable Ed25519 key.`);
    return { keyId: k.key_id, publicKey: k.public_key };
  });
}

async function inChunks<T>(items: T[], each: (chunk: T[]) => Promise<void>, onProgress: ((done: number, total: number) => void) | undefined, total: number, doneBefore: number): Promise<void> {
  for (let at = 0; at < items.length; at += MANIFEST_CHUNK) {
    const chunk = items.slice(at, at + MANIFEST_CHUNK);
    await each(chunk);
    onProgress?.(doneBefore + at + chunk.length, total);
  }
}

/**
 * Applies one manifest answer as ONE write: tickets, keys and version together, so a crash
 * leaves either the old manifest or the new one, never half of each.
 *
 * `full: true` replaces the set. Otherwise `V`/`U` are written over what is there and
 * `X`/`T` are removed; a repeat changes nothing. A ticket this device itself let in is
 * never turned back to `V`: the device's scan is newer than a manifest made before its
 * upload arrived. Afterwards the set must hold exactly `ticket_count` tickets; if a
 * changes-only answer does not get there, nothing of it is kept.
 */
export async function applyManifest(store: GateStore, eventId: number, manifest: Manifest, options: ApplyOptions): Promise<ApplyResult> {
  if (manifest.event.id !== eventId) throw new ManifestError("The server sent the manifest of another event.");
  const keys = checkedKeys(manifest);

  const write = async (tx: GateTx): Promise<number> => {
    const admittedHere = await tx.admittedTicketIds(eventId);
    const upserts: TicketRow[] = [];
    const removals: string[] = [];
    for (const [id, groupIndex, code] of manifest.tickets) {
      const ticketId = id.toLowerCase();
      if (code === "X" || code === "T") {
        removals.push(ticketId);
        continue;
      }
      // An index is only good for the answer it came in: store the text.
      const gateGroup = manifest.gate_groups[groupIndex];
      if (gateGroup === undefined) throw new ManifestError("The manifest names a gate group it does not list.");
      const status: HeldStatus = code === "U" || admittedHere.has(ticketId) ? "U" : "V";
      upserts.push({ ticketId, gateGroup, status });
    }

    const total = manifest.tickets.length;
    if (manifest.full) await tx.clearTickets(eventId);
    await inChunks(upserts, (chunk) => tx.upsertTickets(eventId, chunk), options.onProgress, total, 0);
    // A full answer lists only V and U; removals there would be a no-op on an emptied set.
    await inChunks(removals, (chunk) => tx.deleteTickets(eventId, chunk), options.onProgress, total, upserts.length);

    const count = await tx.countTickets(eventId);
    if (count !== manifest.ticket_count) {
      // A full answer is the whole truth the server has; there is nothing better to fetch.
      if (manifest.full) throw new ManifestError(`The manifest says ${manifest.ticket_count} tickets but lists ${count}.`);
      throw new CountMismatch();
    }

    await tx.replaceKeys(eventId, keys);
    await tx.putManifestMeta({
      eventId,
      version: manifest.version,
      ticketCount: count,
      updatedAt: options.receivedAt,
      clockSkewMs: options.clockSkewMs,
      gateGroups: manifest.gate_groups,
      event: manifest.event,
    });
    return count;
  };

  try {
    const count = await store.transaction(write);
    return { applied: true, full: manifest.full, count, changes: manifest.tickets.length };
  } catch (error) {
    if (error instanceof CountMismatch) return { applied: false, full: false, count: await store.countTickets(eventId), changes: manifest.tickets.length };
    throw error;
  }
}

export interface ManifestAnswer {
  manifest: Manifest;
  receivedAt: string;
  clockSkewMs: number | null;
}

/** Asks the server for the manifest; with `since`, for what changed after that version. */
export type FetchManifest = (eventId: number, since: string | undefined) => Promise<ManifestAnswer>;

export interface SyncResult extends ApplyResult {
  /** A changes-only answer did not add up, so the whole manifest was downloaded again. */
  refetchedInFull: boolean;
}

/**
 * One sync: `since=<stored version>` when the device has a manifest (never the listing's
 * `manifest_version`, which is only a change marker), the whole thing when it has none.
 */
export async function syncManifest(store: GateStore, fetchManifest: FetchManifest, eventId: number, onProgress?: (done: number, total: number) => void): Promise<SyncResult> {
  const stored = await store.getManifestMeta(eventId);
  const first = await fetchManifest(eventId, stored?.version);
  const applied = await applyManifest(store, eventId, first.manifest, { receivedAt: first.receivedAt, clockSkewMs: first.clockSkewMs, onProgress });
  if (applied.applied) return { ...applied, refetchedInFull: false };

  const again = await fetchManifest(eventId, undefined);
  if (!again.manifest.full) throw new ManifestError("The server did not send the full manifest when asked for it.");
  const full = await applyManifest(store, eventId, again.manifest, { receivedAt: again.receivedAt, clockSkewMs: again.clockSkewMs, onProgress });
  return { ...full, refetchedInFull: true };
}

/** More than this between the device clock and the server's is shown as a warning. */
export const CLOCK_WARNING_MS = 2 * 60 * 1000;

/**
 * Device clock minus server clock, from the answer's `Date` header (whole seconds), falling
 * back to the manifest's `generated_at`. The device time is taken as the middle of the
 * request, so a slow download does not read as a wrong clock.
 */
export function clockSkewMs(serverDate: string | null, generatedAt: string | null, sentAtMs: number, receivedAtMs: number): number | null {
  const server = Date.parse(serverDate ?? "");
  const fallback = Date.parse(generatedAt ?? "");
  const serverMs = Number.isNaN(server) ? fallback : server;
  if (Number.isNaN(serverMs)) return null;
  return Math.round((sentAtMs + receivedAtMs) / 2 - serverMs);
}
