import type { components } from "@talanta/shared";
import { describe, expect, it } from "vitest";

import { applyManifest, clockSkewMs, ManifestError, syncManifest, type FetchManifest } from "../manifest";
import { createScanner } from "../scanner";
import { signToken, TEST_KEYS, TEST_PUBLIC_KEY_B64, ticketIdOf } from "./helpers";
import { createMemoryStore } from "./memoryStore";

type Manifest = components["schemas"]["GateManifest"];
type Tuple = components["schemas"]["ManifestTicketTuple"];

const EVENT = { id: 1, title: "Harambee Stars vs Uganda Cranes", kickoff_at: "2026-11-14T13:00:00Z", gates_open_at: "2026-11-14T10:00:00Z", status: "ON_SALE", venue_name: "Talanta Stadium" } as const;
const GROUPS = ["N-LOWER", "N-UPPER", "W-LOWER"];

function manifest(over: Partial<Manifest> & { tickets: Tuple[] }): Manifest {
  return {
    event: EVENT,
    version: "v1",
    full: true,
    generated_at: "2026-11-14T09:00:00Z",
    keys: [{ key_id: "k1", algorithm: "Ed25519", public_key: TEST_PUBLIC_KEY_B64 }],
    gate_groups: GROUPS,
    ticket_count: over.tickets.length,
    ...over,
  };
}
const opts = { receivedAt: "2026-11-14T09:00:01.000Z", clockSkewMs: 0 };
const statuses = async (store: ReturnType<typeof createMemoryStore>, ids: number[]) => Promise.all(ids.map(async (n) => (await store.getTicket(1, ticketIdOf(n)))?.status ?? null));

describe("applying a manifest", () => {
  it("full: stores tickets with their gate group text, the keys and the version in one write", async () => {
    const store = createMemoryStore();
    const r = await applyManifest(store, 1, manifest({ tickets: [[ticketIdOf(1), 0, "V"], [ticketIdOf(2), 2, "U"], [ticketIdOf(3).toUpperCase(), 1, "V"]] }), opts);
    expect(r).toMatchObject({ applied: true, full: true, count: 3 });
    expect(await store.getTicket(1, ticketIdOf(2))).toEqual({ ticketId: ticketIdOf(2), gateGroup: "W-LOWER", status: "U" });
    expect((await store.getTicket(1, ticketIdOf(3)))?.gateGroup).toBe("N-UPPER");
    expect(await store.getManifestMeta(1)).toMatchObject({ version: "v1", ticketCount: 3, gateGroups: GROUPS, updatedAt: opts.receivedAt });
    expect(await store.getKeys(1)).toEqual([{ keyId: "k1", publicKey: TEST_PUBLIC_KEY_B64 }]);
    // One transaction, the version written last, inside it.
    expect(store.log.filter((l) => l === "begin")).toHaveLength(1);
    expect(store.log.indexOf("meta:v1")).toBeGreaterThan(store.log.indexOf("upsert:3"));
    expect(store.log.indexOf("commit")).toBeGreaterThan(store.log.indexOf("meta:v1"));
  });

  it("full: replaces the whole set", async () => {
    const store = createMemoryStore();
    await applyManifest(store, 1, manifest({ tickets: [[ticketIdOf(1), 0, "V"], [ticketIdOf(2), 0, "V"]] }), opts);
    await applyManifest(store, 1, manifest({ version: "v2", tickets: [[ticketIdOf(2), 0, "U"], [ticketIdOf(3), 0, "V"]] }), opts);
    expect(await statuses(store, [1, 2, 3])).toEqual([null, "U", "V"]);
    expect((await store.getManifestMeta(1))?.version).toBe("v2");
  });

  it("changes only: V and U are written over, X and T are removed, and a repeat changes nothing", async () => {
    const store = createMemoryStore();
    await applyManifest(store, 1, manifest({ tickets: [1, 2, 3, 4].map((n): Tuple => [ticketIdOf(n), 0, "V"]) }), opts);
    const delta = manifest({ version: "v2", full: false, ticket_count: 3, tickets: [[ticketIdOf(1), 0, "U"], [ticketIdOf(2), 0, "X"], [ticketIdOf(3), 0, "T"], [ticketIdOf(5), 1, "V"]] });
    expect(await applyManifest(store, 1, delta, opts)).toMatchObject({ applied: true, full: false, count: 3 });
    expect(await statuses(store, [1, 2, 3, 4, 5])).toEqual(["U", null, null, "V", "V"]);
    // The server looks back two minutes, so the same tuples come again.
    expect(await applyManifest(store, 1, { ...delta, version: "v3" }, opts)).toMatchObject({ applied: true, count: 3 });
    expect(await statuses(store, [1, 2, 3, 4, 5])).toEqual(["U", null, null, "V", "V"]);
    expect((await store.getManifestMeta(1))?.version).toBe("v3");
  });

  it("changes only with the wrong count: nothing is written, and sync downloads in full", async () => {
    const store = createMemoryStore();
    await applyManifest(store, 1, manifest({ tickets: [[ticketIdOf(1), 0, "V"], [ticketIdOf(2), 0, "V"]] }), opts);
    const calls: (string | undefined)[] = [];
    const fetch: FetchManifest = async (_eventId, since) => {
      calls.push(since);
      // The device somehow lost ticket 3: the delta has nothing about it, but the count says 3.
      if (since) return { manifest: manifest({ version: "v2", full: false, ticket_count: 3, tickets: [[ticketIdOf(1), 0, "U"]] }), receivedAt: opts.receivedAt, clockSkewMs: 0 };
      return { manifest: manifest({ version: "v3", tickets: [[ticketIdOf(1), 0, "U"], [ticketIdOf(2), 0, "V"], [ticketIdOf(3), 0, "V"]] }), receivedAt: opts.receivedAt, clockSkewMs: 0 };
    };
    const r = await syncManifest(store, fetch, 1);
    // First with the stored version, then with none.
    expect(calls).toEqual(["v1", undefined]);
    expect(r).toMatchObject({ applied: true, full: true, count: 3, refetchedInFull: true });
    expect(store.log).toContain("rollback");
    expect(await statuses(store, [1, 2, 3])).toEqual(["U", "V", "V"]);
    expect((await store.getManifestMeta(1))?.version).toBe("v3");
  });

  it("a rolled-back answer leaves the old set and the old version", async () => {
    const store = createMemoryStore();
    await applyManifest(store, 1, manifest({ tickets: [[ticketIdOf(1), 0, "V"]] }), opts);
    const r = await applyManifest(store, 1, manifest({ version: "v2", full: false, ticket_count: 9, tickets: [[ticketIdOf(1), 0, "U"]] }), opts);
    expect(r.applied).toBe(false);
    expect(await statuses(store, [1])).toEqual(["V"]);
    expect((await store.getManifestMeta(1))?.version).toBe("v1");
  });

  it("sync sends the stored version as `since`, and nothing the first time", async () => {
    const store = createMemoryStore();
    const calls: (string | undefined)[] = [];
    const fetch: FetchManifest = async (_e, since) => {
      calls.push(since);
      return { manifest: manifest({ version: since ? "v2" : "v1", full: !since, ticket_count: 1, tickets: since ? [] : [[ticketIdOf(1), 0, "V"]] }), receivedAt: opts.receivedAt, clockSkewMs: 0 };
    };
    await syncManifest(store, fetch, 1);
    await syncManifest(store, fetch, 1);
    await syncManifest(store, fetch, 1);
    expect(calls).toEqual([undefined, "v1", "v2"]);
  });

  it("never turns a ticket this device admitted back to V, in a full or a changes-only answer", async () => {
    const store = createMemoryStore();
    await applyManifest(store, 1, manifest({ tickets: [[ticketIdOf(1), 0, "V"], [ticketIdOf(2), 0, "V"]] }), opts);
    const scanner = createScanner({ store, keys: TEST_KEYS, now: () => new Date("2026-11-14T10:30:00Z"), newId: () => "6f1c2d3e-4a5b-4c6d-8e7f-809a1b2c3d4e" }, { eventId: 1, gateGroup: "N-LOWER" });
    expect((await scanner.scan(signToken({ ticketId: ticketIdOf(1) }))).result).toBe("ADMITTED");

    // Both manifests were made before the scan reached the server: they still say V.
    await applyManifest(store, 1, manifest({ version: "v2", full: false, ticket_count: 2, tickets: [[ticketIdOf(1), 0, "V"]] }), opts);
    expect(await statuses(store, [1, 2])).toEqual(["U", "V"]);
    await applyManifest(store, 1, manifest({ version: "v3", tickets: [[ticketIdOf(1), 0, "V"], [ticketIdOf(2), 0, "V"]] }), opts);
    expect(await statuses(store, [1, 2])).toEqual(["U", "V"]);
    // But a void still removes it.
    await applyManifest(store, 1, manifest({ version: "v4", full: false, ticket_count: 1, tickets: [[ticketIdOf(1), 0, "X"]] }), opts);
    expect(await statuses(store, [1, 2])).toEqual([null, "V"]);
  });

  it("refuses a manifest of another event, with a bad key, or with a gate group index out of range, writing nothing", async () => {
    const store = createMemoryStore();
    await expect(applyManifest(store, 2, manifest({ tickets: [] }), opts)).rejects.toBeInstanceOf(ManifestError);
    await expect(applyManifest(store, 1, manifest({ tickets: [], keys: [{ key_id: "k1", algorithm: "Ed25519", public_key: "short" }] }), opts)).rejects.toBeInstanceOf(ManifestError);
    await expect(applyManifest(store, 1, manifest({ tickets: [[ticketIdOf(1), 3, "V"]] }), opts)).rejects.toBeInstanceOf(ManifestError);
    await expect(applyManifest(store, 1, manifest({ ticket_count: 5, tickets: [[ticketIdOf(1), 0, "V"]] }), opts)).rejects.toBeInstanceOf(ManifestError);
    expect(await store.getManifestMeta(1)).toBeNull();
    expect(await store.countTickets(1)).toBe(0);
  });

  it("reports progress in chunks for a full stadium", async () => {
    const store = createMemoryStore();
    const tickets = Array.from({ length: 12_000 }, (_, n): Tuple => [ticketIdOf(n), n % 3, "V"]);
    const seen: number[] = [];
    await applyManifest(store, 1, manifest({ tickets }), { ...opts, onProgress: (done) => seen.push(done) });
    expect(seen).toEqual([5000, 10_000, 12_000]);
    expect(await store.countTickets(1)).toBe(12_000);
  });
});

describe("clock check", () => {
  it("is device time (middle of the request) minus the server's Date header", () => {
    const server = Date.parse("2026-11-14T09:00:00Z");
    expect(clockSkewMs("Sat, 14 Nov 2026 09:00:00 GMT", null, server + 180_000, server + 182_000)).toBe(181_000);
    expect(clockSkewMs("Sat, 14 Nov 2026 09:00:00 GMT", null, server - 300_000, server - 300_000)).toBe(-300_000);
  });
  it("falls back to generated_at, and is null when neither can be read", () => {
    expect(clockSkewMs(null, "2026-11-14T09:00:00Z", Date.parse("2026-11-14T09:00:10Z"), Date.parse("2026-11-14T09:00:10Z"))).toBe(10_000);
    expect(clockSkewMs("nonsense", null, 0, 0)).toBeNull();
  });
});
