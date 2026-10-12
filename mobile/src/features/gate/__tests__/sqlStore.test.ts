// The real SQL of the gate store, run on Node's built-in SQLite. The app runs the same
// statements through expo-sqlite; only the three-method adapter differs.

import type { components } from "@talanta/shared";
import { DatabaseSync } from "node:sqlite";
import { describe, expect, it } from "vitest";

import { applyManifest } from "../manifest";
import { createScanner } from "../scanner";
import { openSqlStore, type SqlDb, type SqlValue } from "../sqlStore";
import { uploadPending } from "../upload";
import { EXAMPLE_TICKET_ID, EXAMPLE_TOKEN, signToken, TEST_KEYS, TEST_PUBLIC_KEY_B64, ticketIdOf } from "./helpers";

type Manifest = components["schemas"]["GateManifest"];
type Tuple = components["schemas"]["ManifestTicketTuple"];

function nodeDb(): SqlDb {
  const db = new DatabaseSync(":memory:");
  return {
    exec: async (sql) => void db.exec(sql),
    run: async (sql, params) => void db.prepare(sql).run(...params),
    all: async (sql, params) => db.prepare(sql).all(...params) as Record<string, SqlValue>[],
  };
}

const GROUPS = ["E-LOWER", "E-UPPER", "N-LOWER", "N-UPPER", "S-LOWER", "S-UPPER", "W-LOWER", "W-UPPER"];
function manifest(over: Partial<Manifest> & { tickets: Tuple[] }): Manifest {
  return {
    event: { id: 1, title: "Harambee Stars vs Uganda Cranes", kickoff_at: "2026-11-14T13:00:00Z", gates_open_at: null, status: "ON_SALE", venue_name: "Talanta Stadium" },
    version: "v1",
    full: true,
    generated_at: "2026-11-14T09:00:00Z",
    keys: [{ key_id: "k1", algorithm: "Ed25519", public_key: TEST_PUBLIC_KEY_B64 }],
    gate_groups: GROUPS,
    ticket_count: over.tickets.length,
    ...over,
  };
}
const opts = { receivedAt: "2026-11-14T09:00:01.000Z", clockSkewMs: 1500 };

describe("the SQLite gate store", () => {
  it("holds a manifest: meta, keys, tickets, changes, and a rolled-back answer", async () => {
    const store = await openSqlStore(nodeDb());
    expect(await store.getManifestMeta(1)).toBeNull();
    await applyManifest(store, 1, manifest({ tickets: [[EXAMPLE_TICKET_ID, 2, "V"], [ticketIdOf(2), 6, "U"], [ticketIdOf(3), 2, "V"]] }), opts);
    expect(await store.getManifestMeta(1)).toEqual({ eventId: 1, version: "v1", ticketCount: 3, updatedAt: opts.receivedAt, clockSkewMs: 1500, gateGroups: GROUPS, event: manifest({ tickets: [] }).event });
    expect(await store.getKeys(1)).toEqual([{ keyId: "k1", publicKey: TEST_PUBLIC_KEY_B64 }]);
    expect(await store.getTicket(1, ticketIdOf(2))).toEqual({ ticketId: ticketIdOf(2), gateGroup: "W-LOWER", status: "U" });
    expect(await store.getTicket(2, ticketIdOf(2))).toBeNull();

    const delta = manifest({ version: "v2", full: false, ticket_count: 3, tickets: [[ticketIdOf(2), 6, "X"], [ticketIdOf(3), 2, "U"], [ticketIdOf(4), 0, "V"]] });
    expect(await applyManifest(store, 1, delta, opts)).toMatchObject({ applied: true, count: 3 });
    expect(await store.getTicket(1, ticketIdOf(2))).toBeNull();
    expect((await store.getTicket(1, ticketIdOf(3)))?.status).toBe("U");
    expect(await store.getTicket(1, ticketIdOf(4))).toEqual({ ticketId: ticketIdOf(4), gateGroup: "E-LOWER", status: "V" });

    // A changes-only answer that does not add up is rolled back whole.
    const bad = manifest({ version: "v3", full: false, ticket_count: 99, tickets: [[ticketIdOf(4), 0, "X"]] });
    expect((await applyManifest(store, 1, bad, opts)).applied).toBe(false);
    expect(await store.getTicket(1, ticketIdOf(4))).not.toBeNull();
    expect((await store.getManifestMeta(1))?.version).toBe("v2");
  });

  it("a scan is one write; the second scan of the ticket is DUPLICATE; upload answers are stored", async () => {
    const store = await openSqlStore(nodeDb());
    await applyManifest(store, 1, manifest({ tickets: [[EXAMPLE_TICKET_ID, 2, "V"], [ticketIdOf(2), 2, "V"]] }), opts);
    let n = 0;
    const scanner = createScanner({ store, keys: TEST_KEYS, now: () => new Date("2026-11-14T11:02:17Z"), newId: () => `00000000-0000-4000-8000-00000000000${++n}` }, { eventId: 1, gateGroup: "N-LOWER" });
    expect((await scanner.scan(EXAMPLE_TOKEN)).result).toBe("ADMITTED");
    expect((await scanner.scan(EXAMPLE_TOKEN)).result).toBe("DUPLICATE");
    expect((await scanner.scan("not a ticket")).queued).toBe(false);
    expect((await scanner.scan(signToken({ ticketId: ticketIdOf(2) }))).result).toBe("ADMITTED");
    expect(await store.countPending()).toBe(3);
    expect((await store.pendingScans(2, 0)).map((s) => [s.seq, s.result, s.token === EXAMPLE_TOKEN])).toEqual([[1, "ADMITTED", true], [2, "DUPLICATE", true]]);
    expect((await store.pendingScans(10, 2)).map((s) => s.seq)).toEqual([4]);
    expect((await store.listScans(1, 10)).map((s) => s.upload)).toEqual(["PENDING", "LOCAL", "PENDING", "PENDING"]);

    // A manifest made before the upload still says V: the admitted tickets stay U.
    await applyManifest(store, 1, manifest({ version: "v2", tickets: [[EXAMPLE_TICKET_ID, 2, "V"], [ticketIdOf(2), 2, "V"]] }), opts);
    expect((await store.getTicket(1, EXAMPLE_TICKET_ID))?.status).toBe("U");

    const report = await uploadPending({
      store,
      deviceId: "d",
      now: () => new Date("2026-11-14T12:00:00Z"),
      send: async (b) => ({
        kind: "ok",
        results: b.scans.map((s, i) => ({ client_scan_id: s.client_scan_id, ticket_id: s.ticket_id, result: i === 2 ? "DUPLICATE" : s.result, overridden: i === 2, ticket_status: "USED", first_admitted_at: "2026-11-14T11:00:00Z" })),
      }),
    });
    expect(report).toEqual({ answered: 3, overridden: 1, stopped: null });
    expect(await store.countPending()).toBe(0);
    expect((await store.admittedAnswers(1, 10, 0)).map((s) => s.seq)).toEqual([1]);
    expect(await store.scanSummary(1)).toEqual({ total: 4, admitted: 2, changed: 1 });
    expect((await store.listScans(1, 10, "changed")).map((x) => x.seq)).toEqual([4]);
    expect(await store.listScans(1, 10, "waiting")).toEqual([]);
    expect(await store.scanSummary(2)).toEqual({ total: 0, admitted: 0, changed: 0 });
    expect((await store.listScans(1, 1))[0]).toMatchObject({ seq: 4, result: "ADMITTED", serverResult: "DUPLICATE", overridden: true, upload: "DONE", uploadedAt: "2026-11-14T12:00:00.000Z" });
  });

  it("a transaction that throws stores nothing, and the next one works", async () => {
    const store = await openSqlStore(nodeDb());
    await expect(
      store.transaction(async (tx) => {
        await tx.upsertTickets(1, [{ ticketId: ticketIdOf(1), gateGroup: "N-LOWER", status: "V" }]);
        throw new Error("boom");
      }),
    ).rejects.toThrow("boom");
    expect(await store.countTickets(1)).toBe(0);
    await store.transaction((tx) => tx.upsertTickets(1, [{ ticketId: ticketIdOf(1), gateGroup: "N-LOWER", status: "V" }]));
    expect(await store.countTickets(1)).toBe(1);
  });

  it("keeps settings", async () => {
    const store = await openSqlStore(nodeDb());
    expect(await store.getSetting("device_id")).toBeNull();
    await store.setSetting("device_id", "a");
    await store.setSetting("device_id", "b");
    expect(await store.getSetting("device_id")).toBe("b");
    await store.setSetting("device_id", null);
    expect(await store.getSetting("device_id")).toBeNull();
  });

  it("loads a full stadium (60,000 tickets) and finds one ticket by ID", async () => {
    const store = await openSqlStore(nodeDb());
    const tickets = Array.from({ length: 60_000 }, (_, n): Tuple => [ticketIdOf(n * 7919), n % 8, n % 5 === 0 ? "U" : "V"]);
    const t0 = performance.now();
    const r = await applyManifest(store, 1, manifest({ tickets }), opts);
    const insertMs = performance.now() - t0;
    expect(r).toMatchObject({ applied: true, count: 60_000 });

    const t1 = performance.now();
    const lookups = 1000;
    for (let i = 0; i < lookups; i++) {
      const row = await store.getTicket(1, ticketIdOf(((i * 59) % 60_000) * 7919));
      if (!row) throw new Error("a ticket went missing");
    }
    const lookupMs = (performance.now() - t1) / lookups;
    expect(await store.getTicket(1, ticketIdOf(1))).toBeNull();
    console.log(`[gate store, Node SQLite] 60,000 tickets inserted in ${insertMs.toFixed(0)} ms; one lookup ${lookupMs.toFixed(3)} ms`);
    // Generous bounds: these catch a missing index or a per-row statement, not a slow laptop.
    expect(insertMs).toBeLessThan(10_000);
    expect(lookupMs).toBeLessThan(2);
  });
});
