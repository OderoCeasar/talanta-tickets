// An in-memory GateStore for the tests. `log` records every write in order, so a test can
// assert what was stored before a result was handed back.

import type { GateStore, GateTx, ManifestKey, ManifestMeta, StoredScan, TicketRow } from "../store";

interface State {
  metas: Map<number, ManifestMeta>;
  keys: Map<number, ManifestKey[]>;
  tickets: Map<number, Map<string, TicketRow>>;
  scans: StoredScan[];
  settings: Map<string, string>;
  seq: number;
}

const copyState = (s: State): State => ({
  metas: new Map(s.metas),
  keys: new Map(s.keys),
  tickets: new Map(Array.from(s.tickets, ([k, v]) => [k, new Map(Array.from(v, ([id, row]) => [id, { ...row }]))])),
  scans: s.scans.map((x) => ({ ...x })),
  settings: new Map(s.settings),
  seq: s.seq,
});

export interface MemoryStore extends GateStore {
  /** `begin`, `commit`, `rollback` and one entry per write, in the order they happened. */
  log: string[];
  /** Committed state only. */
  state: () => State;
  /** Makes the next commit fail, as a full disk would. */
  failNextCommit: () => void;
}

export function createMemoryStore(): MemoryStore {
  let state: State = { metas: new Map(), keys: new Map(), tickets: new Map(), scans: [], settings: new Map(), seq: 0 };
  const log: string[] = [];
  let failCommit = false;
  let tail: Promise<unknown> = Promise.resolve();

  const set = (s: State, eventId: number) => {
    let m = s.tickets.get(eventId);
    if (!m) s.tickets.set(eventId, (m = new Map()));
    return m;
  };

  const tx = (s: State): GateTx => ({
    getManifestMeta: async (eventId) => s.metas.get(eventId) ?? null,
    putManifestMeta: async (meta) => {
      log.push(`meta:${meta.version}`);
      s.metas.set(meta.eventId, meta);
    },
    replaceKeys: async (eventId, keys) => {
      log.push("keys");
      s.keys.set(eventId, keys);
    },
    clearTickets: async (eventId) => {
      log.push("clear");
      s.tickets.set(eventId, new Map());
    },
    upsertTickets: async (eventId, rows) => {
      log.push(`upsert:${rows.length}`);
      for (const r of rows) set(s, eventId).set(r.ticketId, { ...r });
    },
    deleteTickets: async (eventId, ids) => {
      log.push(`delete:${ids.length}`);
      for (const id of ids) set(s, eventId).delete(id);
    },
    countTickets: async (eventId) => set(s, eventId).size,
    getTicket: async (eventId, ticketId) => {
      const row = set(s, eventId).get(ticketId);
      return row ? { ...row } : null;
    },
    setTicketStatus: async (eventId, ticketId, status) => {
      log.push(`status:${ticketId}:${status}`);
      const row = set(s, eventId).get(ticketId);
      if (row) row.status = status;
    },
    admittedTicketIds: async (eventId) => new Set(s.scans.filter((x) => x.eventId === eventId && x.result === "ADMITTED" && x.ticketId).map((x) => x.ticketId!)),
    insertScan: async (scan) => {
      log.push(`scan:${scan.clientScanId}:${scan.result}`);
      if (s.scans.some((x) => x.clientScanId === scan.clientScanId)) throw new Error("duplicate client_scan_id");
      s.scans.push({ ...scan, seq: ++s.seq, serverResult: null, overridden: false, firstAdmittedAt: null, uploadedAt: null });
    },
    answerScan: async (clientScanId, a) => {
      log.push(`answer:${clientScanId}:${a.serverResult}`);
      const scan = s.scans.find((x) => x.clientScanId === clientScanId);
      if (scan) Object.assign(scan, { upload: "DONE", serverResult: a.serverResult, overridden: a.overridden, firstAdmittedAt: a.firstAdmittedAt, uploadedAt: a.uploadedAt });
    },
  });

  function inTurn<T>(work: () => Promise<T>): Promise<T> {
    const next = tail.then(work);
    tail = next.catch(() => undefined);
    return next;
  }

  return {
    log,
    state: () => state,
    failNextCommit: () => {
      failCommit = true;
    },
    transaction: (work) =>
      inTurn(async () => {
        const draft = copyState(state);
        log.push("begin");
        try {
          const value = await work(tx(draft));
          if (failCommit) {
            failCommit = false;
            throw new Error("disk full");
          }
          state = draft;
          log.push("commit");
          return value;
        } catch (error) {
          log.push("rollback");
          throw error;
        }
      }),
    getManifestMeta: async (eventId) => state.metas.get(eventId) ?? null,
    getKeys: async (eventId) => state.keys.get(eventId) ?? [],
    getTicket: async (eventId, ticketId) => state.tickets.get(eventId)?.get(ticketId) ?? null,
    countTickets: async (eventId) => state.tickets.get(eventId)?.size ?? 0,
    pendingScans: async (limit, afterSeq) => state.scans.filter((x) => x.upload === "PENDING" && x.seq > afterSeq).slice(0, limit),
    countPending: async () => state.scans.filter((x) => x.upload === "PENDING").length,
    admittedAnswers: async (eventId, limit, afterSeq) => state.scans.filter((x) => x.eventId === eventId && x.upload === "DONE" && x.serverResult === "ADMITTED" && x.seq > afterSeq).slice(0, limit),
    listScans: async (eventId, limit, filter = "all") =>
      state.scans
        .filter((x) => x.eventId === eventId && (filter === "all" || (filter === "waiting" ? x.upload === "PENDING" : x.overridden)))
        .reverse()
        .slice(0, limit),
    scanSummary: async (eventId) => {
      const mine = state.scans.filter((x) => x.eventId === eventId);
      return { total: mine.length, admitted: mine.filter((x) => x.result === "ADMITTED").length, changed: mine.filter((x) => x.overridden).length };
    },
    getSetting: async (key) => state.settings.get(key) ?? null,
    setSetting: async (key, value) => {
      if (value === null) state.settings.delete(key);
      else state.settings.set(key, value);
    },
  };
}
