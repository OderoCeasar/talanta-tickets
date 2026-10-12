// The gate store on SQLite. Written against a three-method database interface, so the very
// same SQL runs on expo-sqlite in the app and on Node's SQLite in the tests.
//
// Why SQLite and not AsyncStorage: a full stadium is about 60,000 tickets. A scan needs one
// of them by ID and changes one; a JSON blob would be parsed and rewritten whole each time.

import type { GateStore, GateTx, HeldStatus, ManifestKey, ManifestMeta, NewScan, ScanAnswer, StoredScan, TicketRow } from "./store";

export type SqlValue = string | number | null;

export interface SqlDb {
  /** Runs statements that take no parameters and return nothing. */
  exec(sql: string): Promise<void>;
  run(sql: string, params: SqlValue[]): Promise<void>;
  all(sql: string, params: SqlValue[]): Promise<Record<string, SqlValue>[]>;
}

// WAL with synchronous=FULL: a commit is on disk when it returns, also across a power cut.
// A scan costs one fsync; losing one would cost more.
const SETUP = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS manifests (
  event_id INTEGER PRIMARY KEY,
  version TEXT NOT NULL,
  ticket_count INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  clock_skew_ms INTEGER,
  gate_groups TEXT NOT NULL,
  event TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS manifest_keys (
  event_id INTEGER NOT NULL,
  key_id TEXT NOT NULL,
  public_key TEXT NOT NULL,
  PRIMARY KEY (event_id, key_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS tickets (
  event_id INTEGER NOT NULL,
  ticket_id TEXT NOT NULL,
  gate_group TEXT NOT NULL,
  status TEXT NOT NULL,
  PRIMARY KEY (event_id, ticket_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS scans (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  client_scan_id TEXT NOT NULL UNIQUE,
  ticket_id TEXT,
  event_id INTEGER NOT NULL,
  gate_group TEXT NOT NULL,
  scanned_at TEXT NOT NULL,
  result TEXT NOT NULL,
  reason TEXT NOT NULL,
  token TEXT,
  upload TEXT NOT NULL,
  server_result TEXT,
  overridden INTEGER NOT NULL DEFAULT 0,
  first_admitted_at TEXT,
  uploaded_at TEXT
);
CREATE INDEX IF NOT EXISTS scans_upload_idx ON scans (upload, seq);
CREATE INDEX IF NOT EXISTS scans_event_idx ON scans (event_id, seq);
`;

const SCAN_COLUMNS = "seq, client_scan_id, ticket_id, event_id, gate_group, scanned_at, result, reason, token, upload, server_result, overridden, first_admitted_at, uploaded_at";

function toScan(row: Record<string, SqlValue>): StoredScan {
  return {
    seq: row.seq as number,
    clientScanId: row.client_scan_id as string,
    ticketId: row.ticket_id as string | null,
    eventId: row.event_id as number,
    gateGroup: row.gate_group as string,
    scannedAt: row.scanned_at as string,
    result: row.result as StoredScan["result"],
    reason: row.reason as string,
    token: row.token as string | null,
    upload: row.upload as StoredScan["upload"],
    serverResult: row.server_result as StoredScan["serverResult"],
    overridden: row.overridden === 1,
    firstAdmittedAt: row.first_admitted_at as string | null,
    uploadedAt: row.uploaded_at as string | null,
  };
}

function toMeta(row: Record<string, SqlValue>): ManifestMeta {
  return {
    eventId: row.event_id as number,
    version: row.version as string,
    ticketCount: row.ticket_count as number,
    updatedAt: row.updated_at as string,
    clockSkewMs: row.clock_skew_ms as number | null,
    gateGroups: JSON.parse(row.gate_groups as string) as string[],
    event: JSON.parse(row.event as string) as ManifestMeta["event"],
  };
}

/** The statements. Used inside a transaction and, for the reads, outside one. */
function queries(db: SqlDb): GateTx & Pick<GateStore, "getKeys" | "pendingScans" | "countPending" | "admittedAnswers" | "listScans" | "scanSummary" | "getSetting" | "setSetting"> {
  return {
    async getManifestMeta(eventId) {
      const rows = await db.all("SELECT * FROM manifests WHERE event_id = ?", [eventId]);
      return rows[0] ? toMeta(rows[0]) : null;
    },
    async putManifestMeta(m) {
      await db.run(
        `INSERT INTO manifests (event_id, version, ticket_count, updated_at, clock_skew_ms, gate_groups, event) VALUES (?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT (event_id) DO UPDATE SET version = excluded.version, ticket_count = excluded.ticket_count, updated_at = excluded.updated_at,
           clock_skew_ms = excluded.clock_skew_ms, gate_groups = excluded.gate_groups, event = excluded.event`,
        [m.eventId, m.version, m.ticketCount, m.updatedAt, m.clockSkewMs, JSON.stringify(m.gateGroups), JSON.stringify(m.event)],
      );
    },
    async replaceKeys(eventId, keys) {
      await db.run("DELETE FROM manifest_keys WHERE event_id = ?", [eventId]);
      for (const k of keys) await db.run("INSERT INTO manifest_keys (event_id, key_id, public_key) VALUES (?, ?, ?)", [eventId, k.keyId, k.publicKey]);
    },
    async getKeys(eventId) {
      const rows = await db.all("SELECT key_id, public_key FROM manifest_keys WHERE event_id = ?", [eventId]);
      return rows.map((r): ManifestKey => ({ keyId: r.key_id as string, publicKey: r.public_key as string }));
    },
    async clearTickets(eventId) {
      await db.run("DELETE FROM tickets WHERE event_id = ?", [eventId]);
    },
    async upsertTickets(eventId, rows) {
      if (rows.length === 0) return;
      // The whole chunk travels as one JSON parameter and SQLite unpacks it: one statement
      // for thousands of rows, instead of thousands of trips across the native bridge.
      await db.run(
        `INSERT INTO tickets (event_id, ticket_id, gate_group, status)
         SELECT ?, json_extract(value, '$[0]'), json_extract(value, '$[1]'), json_extract(value, '$[2]') FROM json_each(?) WHERE true
         ON CONFLICT (event_id, ticket_id) DO UPDATE SET gate_group = excluded.gate_group, status = excluded.status`,
        [eventId, JSON.stringify(rows.map((r) => [r.ticketId, r.gateGroup, r.status]))],
      );
    },
    async deleteTickets(eventId, ticketIds) {
      if (ticketIds.length === 0) return;
      await db.run("DELETE FROM tickets WHERE event_id = ? AND ticket_id IN (SELECT value FROM json_each(?))", [eventId, JSON.stringify(ticketIds)]);
    },
    async countTickets(eventId) {
      const rows = await db.all("SELECT count(*) AS n FROM tickets WHERE event_id = ?", [eventId]);
      return rows[0]!.n as number;
    },
    async getTicket(eventId, ticketId) {
      const rows = await db.all("SELECT ticket_id, gate_group, status FROM tickets WHERE event_id = ? AND ticket_id = ?", [eventId, ticketId]);
      const r = rows[0];
      return r ? ({ ticketId: r.ticket_id as string, gateGroup: r.gate_group as string, status: r.status as HeldStatus } satisfies TicketRow) : null;
    },
    async setTicketStatus(eventId, ticketId, status) {
      await db.run("UPDATE tickets SET status = ? WHERE event_id = ? AND ticket_id = ?", [status, eventId, ticketId]);
    },
    async admittedTicketIds(eventId) {
      const rows = await db.all("SELECT DISTINCT ticket_id FROM scans WHERE event_id = ? AND result = 'ADMITTED' AND ticket_id IS NOT NULL", [eventId]);
      return new Set(rows.map((r) => r.ticket_id as string));
    },
    async insertScan(s: NewScan) {
      await db.run(
        "INSERT INTO scans (client_scan_id, ticket_id, event_id, gate_group, scanned_at, result, reason, token, upload) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
        [s.clientScanId, s.ticketId, s.eventId, s.gateGroup, s.scannedAt, s.result, s.reason, s.token, s.upload],
      );
    },
    async answerScan(clientScanId, a: ScanAnswer) {
      await db.run("UPDATE scans SET upload = 'DONE', server_result = ?, overridden = ?, first_admitted_at = ?, uploaded_at = ? WHERE client_scan_id = ?", [
        a.serverResult,
        a.overridden ? 1 : 0,
        a.firstAdmittedAt,
        a.uploadedAt,
        clientScanId,
      ]);
    },
    async pendingScans(limit, afterSeq) {
      const rows = await db.all(`SELECT ${SCAN_COLUMNS} FROM scans WHERE upload = 'PENDING' AND seq > ? ORDER BY seq LIMIT ?`, [afterSeq, limit]);
      return rows.map(toScan);
    },
    async countPending() {
      const rows = await db.all("SELECT count(*) AS n FROM scans WHERE upload = 'PENDING'", []);
      return rows[0]!.n as number;
    },
    async admittedAnswers(eventId, limit, afterSeq) {
      const rows = await db.all(`SELECT ${SCAN_COLUMNS} FROM scans WHERE event_id = ? AND upload = 'DONE' AND server_result = 'ADMITTED' AND seq > ? ORDER BY seq LIMIT ?`, [eventId, afterSeq, limit]);
      return rows.map(toScan);
    },
    async listScans(eventId, limit, filter = "all") {
      const only = filter === "waiting" ? "AND upload = 'PENDING'" : filter === "changed" ? "AND overridden = 1" : "";
      const rows = await db.all(`SELECT ${SCAN_COLUMNS} FROM scans WHERE event_id = ? ${only} ORDER BY seq DESC LIMIT ?`, [eventId, limit]);
      return rows.map(toScan);
    },
    async scanSummary(eventId) {
      const rows = await db.all(
        "SELECT count(*) AS total, coalesce(sum(result = 'ADMITTED'), 0) AS admitted, coalesce(sum(overridden), 0) AS changed FROM scans WHERE event_id = ?",
        [eventId],
      );
      const r = rows[0]!;
      return { total: r.total as number, admitted: r.admitted as number, changed: r.changed as number };
    },
    async getSetting(key) {
      const rows = await db.all("SELECT value FROM settings WHERE key = ?", [key]);
      return rows[0] ? (rows[0].value as string) : null;
    },
    async setSetting(key, value) {
      if (value === null) await db.run("DELETE FROM settings WHERE key = ?", [key]);
      else await db.run("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", [key, value]);
    },
  };
}

/** Creates the tables if they are missing and returns the store. */
export async function openSqlStore(db: SqlDb): Promise<GateStore> {
  await db.exec(SETUP);
  const q = queries(db);

  // Everything goes through one queue. The database has one connection, so a read that
  // slipped in between a transaction's statements would see its half-finished state.
  let tail: Promise<unknown> = Promise.resolve();
  function inTurn<T>(work: () => Promise<T>): Promise<T> {
    const next = tail.then(work);
    tail = next.catch(() => undefined);
    return next;
  }

  return {
    transaction: (work) =>
      inTurn(async () => {
        // IMMEDIATE takes the write lock now, so the transaction cannot fail half way on it.
        await db.exec("BEGIN IMMEDIATE");
        try {
          const value = await work(q);
          await db.exec("COMMIT");
          return value;
        } catch (error) {
          await db.exec("ROLLBACK").catch(() => undefined);
          throw error;
        }
      }),
    getManifestMeta: (eventId) => inTurn(() => q.getManifestMeta(eventId)),
    getKeys: (eventId) => inTurn(() => q.getKeys(eventId)),
    getTicket: (eventId, ticketId) => inTurn(() => q.getTicket(eventId, ticketId)),
    countTickets: (eventId) => inTurn(() => q.countTickets(eventId)),
    pendingScans: (limit, afterSeq) => inTurn(() => q.pendingScans(limit, afterSeq)),
    countPending: () => inTurn(() => q.countPending()),
    admittedAnswers: (eventId, limit, afterSeq) => inTurn(() => q.admittedAnswers(eventId, limit, afterSeq)),
    listScans: (eventId, limit, filter) => inTurn(() => q.listScans(eventId, limit, filter)),
    scanSummary: (eventId) => inTurn(() => q.scanSummary(eventId)),
    getSetting: (key) => inTurn(() => q.getSetting(key)),
    setSetting: (key, value) => inTurn(() => q.setSetting(key, value)),
  };
}
