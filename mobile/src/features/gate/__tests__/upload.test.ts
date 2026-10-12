import { describe, expect, it } from "vitest";

import { createScanner } from "../scanner";
import { outcomeForStatus, recheckAdmitted, uploadPending, type ScanBatch, type SendOutcome } from "../upload";
import { signToken, TEST_KEYS, ticketIdOf, unsignedToken } from "./helpers";
import { createMemoryStore, type MemoryStore } from "./memoryStore";

type Results = Extract<SendOutcome, { kind: "ok" }>["results"];

/** A store with `n` tickets, each admitted once on this device and waiting to upload. */
async function scanned(n: number) {
  const store = createMemoryStore();
  // Written directly, in one transaction: scanning itself is tested in scanner.test.ts.
  await store.transaction(async (tx) => {
    await tx.upsertTickets(1, Array.from({ length: n }, (_, i) => ({ ticketId: ticketIdOf(i), gateGroup: "N-LOWER", status: "U" as const })));
    for (let i = 0; i < n; i++) {
      await tx.insertScan({
        clientScanId: `10000000-0000-4000-8000-${(i + 1).toString(16).padStart(12, "0")}`,
        ticketId: ticketIdOf(i),
        eventId: 1,
        gateGroup: "N-LOWER",
        scannedAt: "2026-11-14T11:00:00.000Z",
        result: "ADMITTED",
        reason: "OK",
        token: unsignedToken({ ticketId: ticketIdOf(i) }),
        upload: "PENDING",
      });
    }
  });
  return store;
}

const echo = (batch: ScanBatch, over: Partial<Results[number]> = {}): Results =>
  batch.scans.map((s) => ({ client_scan_id: s.client_scan_id, ticket_id: s.ticket_id, result: s.result, overridden: false, ticket_status: "USED", first_admitted_at: s.scanned_at, ...over }));

function deps(store: MemoryStore, send: (batch: ScanBatch, call: number) => SendOutcome | Promise<SendOutcome>) {
  const batches: ScanBatch[] = [];
  return {
    batches,
    deps: {
      store,
      deviceId: "gate-test",
      now: () => new Date("2026-11-14T12:00:00Z"),
      send: async (batch: ScanBatch) => {
        batches.push(batch);
        return send(batch, batches.length);
      },
    },
  };
}

describe("uploading", () => {
  it("sends oldest first, with the token and the device ID, and empties the queue on a 200 that lists every scan", async () => {
    const store = await scanned(3);
    const { deps: d, batches } = deps(store, (b) => ({ kind: "ok", results: echo(b) }));
    const report = await uploadPending(d);
    expect(report).toEqual({ answered: 3, overridden: 0, stopped: null });
    expect(batches).toHaveLength(1);
    expect(batches[0]!.device_id).toBe("gate-test");
    expect(batches[0]!.scans.map((s) => s.ticket_id)).toEqual([ticketIdOf(0), ticketIdOf(1), ticketIdOf(2)]);
    expect(batches[0]!.scans[0]).toEqual({
      client_scan_id: "10000000-0000-4000-8000-000000000001",
      ticket_id: ticketIdOf(0),
      event_id: 1,
      gate_group: "N-LOWER",
      scanned_at: "2026-11-14T11:00:00.000Z",
      result: "ADMITTED",
      token: unsignedToken({ ticketId: ticketIdOf(0) }),
    });
    expect(await store.countPending()).toBe(0);
    expect(store.state().scans.every((s) => s.upload === "DONE" && s.serverResult === "ADMITTED" && s.uploadedAt === "2026-11-14T12:00:00.000Z")).toBe(true);
  });

  it("a scan leaves the queue ONLY when the 200 lists its client_scan_id", async () => {
    const store = await scanned(3);
    // The answer lists the first and the third, and one ID nobody sent.
    const first = deps(store, (b) => ({ kind: "ok", results: [...echo(b).filter((_, i) => i !== 1), { ...echo(b)[0]!, client_scan_id: "99999999-0000-4000-8000-000000000000" }] }));
    expect(await uploadPending(first.deps)).toMatchObject({ answered: 2 });
    expect((await store.pendingScans(10, 0)).map((s) => s.ticketId)).toEqual([ticketIdOf(1)]);
    // It did not spin on the unlisted scan: one call.
    expect(first.batches).toHaveLength(1);
    // The next run sends it again, with the same ID.
    const second = deps(store, (b) => ({ kind: "ok", results: echo(b) }));
    await uploadPending(second.deps);
    expect(second.batches[0]!.scans.map((s) => s.client_scan_id)).toEqual(["10000000-0000-4000-8000-000000000002"]);
    expect(await store.countPending()).toBe(0);
  });

  it("an empty 200 removes nothing", async () => {
    const store = await scanned(2);
    const { deps: d } = deps(store, () => ({ kind: "ok", results: [] }));
    expect(await uploadPending(d)).toMatchObject({ answered: 0 });
    expect(await store.countPending()).toBe(2);
  });

  it.each([
    ["a network error", { kind: "retry", why: "network", retryAfterSeconds: null }],
    ["409 EVENT_BUSY", { kind: "retry", why: "busy", retryAfterSeconds: null }],
    ["429", { kind: "retry", why: "rate_limited", retryAfterSeconds: 7 }],
    ["a 5xx", { kind: "retry", why: "server", retryAfterSeconds: null }],
  ] as const)("survives %s: the queue is untouched and the very same batch goes again", async (_name, outcome) => {
    const store = await scanned(3);
    const failing = deps(store, () => outcome);
    const report = await uploadPending(failing.deps);
    expect(report).toEqual({ answered: 0, overridden: 0, stopped: outcome });
    expect(failing.batches).toHaveLength(1);
    expect(await store.countPending()).toBe(3);

    const working = deps(store, (b) => ({ kind: "ok", results: echo(b) }));
    await uploadPending(working.deps);
    expect(working.batches[0]).toEqual(failing.batches[0]);
    expect(await store.countPending()).toBe(0);
  });

  it("sends at most 500 a call", async () => {
    const store = await scanned(1200);
    const { deps: d, batches } = deps(store, (b) => ({ kind: "ok", results: echo(b) }));
    expect(await uploadPending(d)).toMatchObject({ answered: 1200, stopped: null });
    expect(batches.map((b) => b.scans.length)).toEqual([500, 500, 200]);
    expect(batches[1]!.scans[0]!.ticket_id).toBe(ticketIdOf(500));
  });

  it("splits on 413 until the batch is accepted", async () => {
    const store = await scanned(40);
    const { deps: d, batches } = deps(store, (b) => (b.scans.length > 10 ? { kind: "too_large" } : { kind: "ok", results: echo(b) }));
    expect(await uploadPending(d)).toMatchObject({ answered: 40, stopped: null });
    expect(batches.map((b) => b.scans.length)).toEqual([40, 20, 10, 10, 10, 10]);
    expect(await store.countPending()).toBe(0);
  });

  it("stops when a single scan is still too large, keeping it", async () => {
    const store = await scanned(2);
    const { deps: d, batches } = deps(store, () => ({ kind: "too_large" }));
    const report = await uploadPending(d);
    expect(report.stopped?.kind).toBe("rejected");
    expect(batches.map((b) => b.scans.length)).toEqual([2, 1]);
    expect(await store.countPending()).toBe(2);
  });

  it("keeps the scans on 400 and reports it", async () => {
    const store = await scanned(3);
    const { deps: d, batches } = deps(store, () => ({ kind: "rejected", message: "scans[0].event_id: unknown event" }));
    const report = await uploadPending(d);
    expect(report.stopped).toEqual({ kind: "rejected", message: "scans[0].event_id: unknown event" });
    expect(batches).toHaveLength(1);
    expect(await store.countPending()).toBe(3);
  });

  it("a failure after some batches went through keeps only what was not answered", async () => {
    const store = await scanned(700);
    const { deps: d } = deps(store, (b, call) => (call === 1 ? { kind: "ok", results: echo(b) } : { kind: "retry", why: "network", retryAfterSeconds: null }));
    expect(await uploadPending(d)).toMatchObject({ answered: 500, stopped: { kind: "retry" } });
    expect(await store.countPending()).toBe(200);
  });
});

describe("reading the answer", () => {
  it("stores the server's result and marks where it differs from what the steward saw", async () => {
    const store = await scanned(2);
    const { deps: d } = deps(store, (b) => ({ kind: "ok", results: [{ ...echo(b)[0]! }, { ...echo(b)[1]!, result: "DUPLICATE", overridden: true, first_admitted_at: "2026-11-14T10:58:03Z" }] }));
    expect(await uploadPending(d)).toMatchObject({ answered: 2, overridden: 1 });
    const [a, b] = store.state().scans;
    expect(a).toMatchObject({ result: "ADMITTED", serverResult: "ADMITTED", overridden: false });
    expect(b).toMatchObject({ result: "ADMITTED", serverResult: "DUPLICATE", overridden: true, firstAdmittedAt: "2026-11-14T10:58:03Z" });
  });

  it("sets the local ticket from ticket_status: VOID and TRANSFERRED remove it, and an admitted ticket never goes back to V", async () => {
    const store = await scanned(3);
    const { deps: d } = deps(store, (b) => ({
      kind: "ok",
      results: [
        { ...echo(b)[0]!, result: "INVALID", overridden: true, ticket_status: "VOID" },
        { ...echo(b)[1]!, result: "INVALID", overridden: true, ticket_status: "VALID" },
        { ...echo(b)[2]!, ticket_status: null },
      ],
    }));
    await uploadPending(d);
    expect(await store.getTicket(1, ticketIdOf(0))).toBeNull();
    expect((await store.getTicket(1, ticketIdOf(1)))?.status).toBe("U");
    expect((await store.getTicket(1, ticketIdOf(2)))?.status).toBe("U");
  });

  it("a refused scan's ticket follows the server: USED marks it used here too", async () => {
    const store = createMemoryStore();
    await store.transaction((tx) => tx.upsertTickets(1, [{ ticketId: ticketIdOf(5), gateGroup: "W-LOWER", status: "V" }]));
    const scanner = createScanner({ store, keys: TEST_KEYS, now: () => new Date("2026-11-14T11:00:00Z"), newId: () => "10000000-0000-4000-8000-000000000001" }, { eventId: 1, gateGroup: "N-LOWER" });
    expect((await scanner.scan(signToken({ ticketId: ticketIdOf(5), gateGroup: "W-LOWER" }))).result).toBe("WRONG_GATE");
    const { deps: d } = deps(store, (b) => ({ kind: "ok", results: echo(b) }));
    await uploadPending(d);
    expect((await store.getTicket(1, ticketIdOf(5)))?.status).toBe("U");
  });
});

describe("before ending a session", () => {
  it("resends the scans last answered ADMITTED, with the same IDs, and learns one became DUPLICATE", async () => {
    const store = await scanned(3);
    await uploadPending(deps(store, (b) => ({ kind: "ok", results: echo(b) })).deps);

    // Meanwhile another phone uploaded an earlier scan of ticket 1.
    const again = deps(store, (b) => ({ kind: "ok", results: echo(b).map((r) => (r.ticket_id === ticketIdOf(1) ? { ...r, result: "DUPLICATE" as const, overridden: true, first_admitted_at: "2026-11-14T10:59:00Z" } : r)) }));
    expect(await recheckAdmitted(again.deps, 1)).toEqual({ answered: 3, overridden: 1, stopped: null });
    expect(again.batches[0]!.scans.map((s) => s.client_scan_id)).toEqual(["10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002", "10000000-0000-4000-8000-000000000003"]);
    expect(store.state().scans.map((s) => [s.serverResult, s.overridden])).toEqual([["ADMITTED", false], ["DUPLICATE", true], ["ADMITTED", false]]);

    // A third time, only the two still ADMITTED are asked about.
    const third = deps(store, (b) => ({ kind: "ok", results: echo(b) }));
    await recheckAdmitted(third.deps, 1);
    expect(third.batches[0]!.scans).toHaveLength(2);
  });

  it("a failed recheck changes nothing", async () => {
    const store = await scanned(2);
    await uploadPending(deps(store, (b) => ({ kind: "ok", results: echo(b) })).deps);
    const report = await recheckAdmitted(deps(store, () => ({ kind: "retry", why: "network", retryAfterSeconds: null })).deps, 1);
    expect(report.stopped).toMatchObject({ kind: "retry" });
    expect(store.state().scans.every((s) => s.serverResult === "ADMITTED" && !s.overridden)).toBe(true);
  });
});

describe("what an HTTP status means for a batch", () => {
  it("409, 429 and 5xx resend; 413 splits; 400 keeps and reports; 401/403 wait for a sign-in", () => {
    expect(outcomeForStatus(409, null, "busy")).toEqual({ kind: "retry", why: "busy", retryAfterSeconds: null });
    expect(outcomeForStatus(429, 12, "slow down")).toEqual({ kind: "retry", why: "rate_limited", retryAfterSeconds: 12 });
    for (const status of [500, 502, 503, 504]) expect(outcomeForStatus(status, null, "x")).toMatchObject({ kind: "retry", why: "server" });
    expect(outcomeForStatus(413, null, "too big")).toEqual({ kind: "too_large" });
    expect(outcomeForStatus(400, null, "scans[0].event_id is not an event")).toEqual({ kind: "rejected", message: "scans[0].event_id is not an event" });
    expect(outcomeForStatus(401, null, "x")).toMatchObject({ kind: "retry", why: "signed_out" });
    expect(outcomeForStatus(403, null, "x")).toMatchObject({ kind: "retry", why: "signed_out" });
    // Never "ok": only a 200 with a body can take scans out of the queue.
    for (const status of [204, 301, 302, 404]) expect(outcomeForStatus(status, null, "x").kind).not.toBe("ok");
  });
});
