// What gate mode keeps on the phone, as an interface: the pure logic (manifest, scanning,
// upload) is written against this, the app passes the SQLite store, the tests an in-memory one.

import type { components } from "@talanta/shared";

type Schemas = components["schemas"];

export type ScanResult = Schemas["ScanResult"];
/** A ticket the device holds is valid or used. Void and transferred tickets are not held. */
export type HeldStatus = "V" | "U";

export interface TicketRow {
  ticketId: string;
  gateGroup: string;
  status: HeldStatus;
}

export interface ManifestMeta {
  eventId: number;
  /** The manifest's `version`: the only thing ever sent back as `since`. */
  version: string;
  ticketCount: number;
  /** Device time (UTC ISO) the manifest was last brought up to date. */
  updatedAt: string;
  /** Device clock minus server clock at that moment, in milliseconds. Null if it could not be read. */
  clockSkewMs: number | null;
  gateGroups: string[];
  event: Schemas["GateEvent"];
}

export interface ManifestKey {
  keyId: string;
  /** 43 characters of base64url, as the manifest sends it. */
  publicKey: string;
}

/**
 * `PENDING`: waiting to be uploaded. `DONE`: a 200 answer listed it. `LOCAL`: a text that
 * could not be read as far as a ticket ID; shown in the history, never uploaded.
 */
export type UploadState = "PENDING" | "DONE" | "LOCAL";

export interface StoredScan {
  /** Order on this device, set by the store. */
  seq: number;
  clientScanId: string;
  ticketId: string | null;
  /** What the device was set to, not what the ticket says. */
  eventId: number;
  gateGroup: string;
  scannedAt: string;
  /** What the steward was shown. */
  result: ScanResult;
  /** The step that decided it, for the wording in the history. */
  reason: string;
  token: string | null;
  upload: UploadState;
  /** The server's final word, once uploaded. */
  serverResult: ScanResult | null;
  overridden: boolean;
  firstAdmittedAt: string | null;
  uploadedAt: string | null;
}

export type NewScan = Omit<StoredScan, "seq" | "serverResult" | "overridden" | "firstAdmittedAt" | "uploadedAt">;

export interface ScanAnswer {
  serverResult: ScanResult;
  overridden: boolean;
  firstAdmittedAt: string | null;
  uploadedAt: string;
}

/** Reads and writes inside one transaction. */
export interface GateTx {
  getManifestMeta(eventId: number): Promise<ManifestMeta | null>;
  putManifestMeta(meta: ManifestMeta): Promise<void>;
  replaceKeys(eventId: number, keys: ManifestKey[]): Promise<void>;
  clearTickets(eventId: number): Promise<void>;
  upsertTickets(eventId: number, rows: TicketRow[]): Promise<void>;
  deleteTickets(eventId: number, ticketIds: string[]): Promise<void>;
  countTickets(eventId: number): Promise<number>;
  getTicket(eventId: number, ticketId: string): Promise<TicketRow | null>;
  /** Changes the status of a ticket the device holds; does nothing for one it does not. */
  setTicketStatus(eventId: number, ticketId: string, status: HeldStatus): Promise<void>;
  /** Every ticket this device itself let in for the event (its own verdict was ADMITTED). */
  admittedTicketIds(eventId: number): Promise<Set<string>>;
  insertScan(scan: NewScan): Promise<void>;
  /** Stores the server's answer for a scan and takes it out of the upload queue. */
  answerScan(clientScanId: string, answer: ScanAnswer): Promise<void>;
}

/** Which of the event's scans the history shows. */
export type ScanFilter = "all" | "waiting" | "changed";

export interface ScanSummary {
  total: number;
  admitted: number;
  /** Uploaded scans the server decided differently from what the steward saw. */
  changed: number;
}

export interface GateStore {
  /**
   * Runs `work` as ONE write: everything in it is stored, or (if it throws) nothing is.
   * When the promise resolves the data is on disk. Transactions never overlap.
   */
  transaction<T>(work: (tx: GateTx) => Promise<T>): Promise<T>;

  getManifestMeta(eventId: number): Promise<ManifestMeta | null>;
  getKeys(eventId: number): Promise<ManifestKey[]>;
  getTicket(eventId: number, ticketId: string): Promise<TicketRow | null>;
  countTickets(eventId: number): Promise<number>;

  /** The upload queue, oldest first, starting after `afterSeq`. */
  pendingScans(limit: number, afterSeq: number): Promise<StoredScan[]>;
  /** Scans waiting to be uploaded, for every event: the queue is the device's, not one session's. */
  countPending(): Promise<number>;
  /** Uploaded scans of the event whose last answer was ADMITTED, oldest first. */
  admittedAnswers(eventId: number, limit: number, afterSeq: number): Promise<StoredScan[]>;
  /** This device's scans for the event, newest first. */
  listScans(eventId: number, limit: number, filter?: ScanFilter): Promise<StoredScan[]>;
  scanSummary(eventId: number): Promise<ScanSummary>;

  getSetting(key: string): Promise<string | null>;
  setSetting(key: string, value: string | null): Promise<void>;
}
