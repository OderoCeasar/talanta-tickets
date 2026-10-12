import { describe, expect, it } from "vitest";

import { createScanner } from "../scanner";
import { EXAMPLE_TICKET_ID, EXAMPLE_TOKEN, signToken, TEST_KEYS, ticketIdOf } from "./helpers";
import { createMemoryStore, type MemoryStore } from "./memoryStore";

const DEVICE = { eventId: 1, gateGroup: "N-LOWER" };

async function setup(tickets: [string, string, "V" | "U"][] = [[EXAMPLE_TICKET_ID, "N-LOWER", "V"]]) {
  const store = createMemoryStore();
  await store.transaction((tx) => tx.upsertTickets(1, tickets.map(([ticketId, gateGroup, status]) => ({ ticketId, gateGroup, status }))));
  store.log.length = 0;
  let n = 0;
  const scanner = createScanner({ store, keys: TEST_KEYS, now: () => new Date("2026-11-14T11:02:17.000Z"), newId: () => `00000000-0000-4000-8000-00000000000${++n}` }, DEVICE);
  return { store, scanner };
}
const scans = (store: MemoryStore) => store.state().scans;

describe("scanning", () => {
  it("a valid ticket is admitted, stored with everything the upload needs, and marked used", async () => {
    const { store, scanner } = await setup();
    const out = await scanner.scan(EXAMPLE_TOKEN);
    expect(out).toMatchObject({ result: "ADMITTED", reason: "OK", ticketId: EXAMPLE_TICKET_ID, ticketGateGroup: "N-LOWER", queued: true });
    expect(scans(store)).toEqual([
      expect.objectContaining({
        clientScanId: "00000000-0000-4000-8000-000000000001",
        ticketId: EXAMPLE_TICKET_ID,
        eventId: 1,
        gateGroup: "N-LOWER",
        scannedAt: "2026-11-14T11:02:17.000Z",
        result: "ADMITTED",
        token: EXAMPLE_TOKEN,
        upload: "PENDING",
      }),
    ]);
    expect((await store.getTicket(1, EXAMPLE_TICKET_ID))?.status).toBe("U");
  });

  it("the second scan of an admitted ticket on this device is DUPLICATE", async () => {
    const { store, scanner } = await setup();
    expect((await scanner.scan(EXAMPLE_TOKEN)).result).toBe("ADMITTED");
    expect(await scanner.scan(EXAMPLE_TOKEN)).toMatchObject({ result: "DUPLICATE", reason: "ALREADY_USED" });
    expect(scans(store).map((s) => s.result)).toEqual(["ADMITTED", "DUPLICATE"]);
    // Two scans, two IDs.
    expect(new Set(scans(store).map((s) => s.clientScanId)).size).toBe(2);
  });

  it("two reads of the same code fired at once admit only one", async () => {
    const { scanner } = await setup();
    const results = await Promise.all([scanner.scan(EXAMPLE_TOKEN), scanner.scan(EXAMPLE_TOKEN), scanner.scan(EXAMPLE_TOKEN)]);
    expect(results.map((r) => r.result)).toEqual(["ADMITTED", "DUPLICATE", "DUPLICATE"]);
  });

  it("a ticket the manifest says is used is DUPLICATE", async () => {
    const { scanner } = await setup([[EXAMPLE_TICKET_ID, "N-LOWER", "U"]]);
    expect((await scanner.scan(EXAMPLE_TOKEN)).result).toBe("DUPLICATE");
  });

  it("a forged code is INVALID, is uploaded with its token, and does not touch the ticket", async () => {
    const { store, scanner } = await setup();
    // The document's first bad text: the payload edited to say event 2, under the old signature.
    const forged = EXAMPLE_TOKEN.replace("ABTi1M", "ACTi1M");
    expect(forged).not.toBe(EXAMPLE_TOKEN);
    expect(await scanner.scan(forged)).toMatchObject({ result: "INVALID", reason: "BAD_SIGNATURE", ticketId: EXAMPLE_TICKET_ID, ticketGateGroup: null, queued: true });
    expect(scans(store)[0]).toMatchObject({ result: "INVALID", token: forged, upload: "PENDING", ticketId: EXAMPLE_TICKET_ID });
    expect((await store.getTicket(1, EXAMPLE_TICKET_ID))?.status).toBe("V");
  });

  it("a ticket for another stand is WRONG_GATE and stays valid", async () => {
    const id = ticketIdOf(7);
    const { store, scanner } = await setup([[id, "W-LOWER", "V"]]);
    expect(await scanner.scan(signToken({ ticketId: id, gateGroup: "W-LOWER" }))).toMatchObject({ result: "WRONG_GATE", ticketGateGroup: "W-LOWER" });
    expect((await store.getTicket(1, id))?.status).toBe("V");
    // Stored with what the DEVICE is set to, not what the ticket says.
    expect(scans(store)[0]).toMatchObject({ gateGroup: "N-LOWER", eventId: 1, result: "WRONG_GATE" });
  });

  it("a ticket for another event is WRONG_EVENT, not INVALID, though it is not in the manifest", async () => {
    const { scanner } = await setup();
    expect(await scanner.scan(signToken({ ticketId: ticketIdOf(8), eventId: 2n }))).toMatchObject({ result: "WRONG_EVENT", reason: "WRONG_EVENT" });
    // Event before gate.
    expect((await scanner.scan(signToken({ ticketId: ticketIdOf(8), eventId: 2n, gateGroup: "W-LOWER" }))).result).toBe("WRONG_EVENT");
  });

  it("a genuine ticket that is not in the manifest (voided) is INVALID", async () => {
    const { scanner } = await setup();
    expect(await scanner.scan(signToken({ ticketId: ticketIdOf(9) }))).toMatchObject({ result: "INVALID", reason: "NOT_IN_MANIFEST" });
  });

  it("a text that cannot be read as far as a ticket ID is INVALID and never queued for upload", async () => {
    const { store, scanner } = await setup();
    for (const text of ["https://example.com", "TLT1.abc", `${EXAMPLE_TOKEN.slice(0, -1)}B`, signToken({ version: 2 })]) {
      expect(await scanner.scan(text)).toMatchObject({ result: "INVALID", ticketId: null, queued: false });
    }
    expect(await store.countPending()).toBe(0);
    expect(scans(store).every((s) => s.upload === "LOCAL" && s.token === null && s.ticketId === null)).toBe(true);
  });

  it("an unknown key is INVALID but is uploaded: the ticket ID could be read", async () => {
    const { store, scanner } = await setup();
    expect(await scanner.scan(signToken({ keyNumber: 2 }))).toMatchObject({ result: "INVALID", reason: "UNKNOWN_KEY", queued: true });
    expect(await store.countPending()).toBe(1);
  });
});

describe("write before you show", () => {
  it("the scan and the used mark are committed in one write before the result is handed back", async () => {
    const { store, scanner } = await setup();
    const out = scanner.scan(EXAMPLE_TOKEN).then((r) => {
      store.log.push(`shown:${r.result}`);
      return r;
    });
    await out;
    expect(store.log).toEqual(["begin", "scan:00000000-0000-4000-8000-000000000001:ADMITTED", `status:${EXAMPLE_TICKET_ID}:U`, "commit", "shown:ADMITTED"]);
  });

  it("the same holds for a refusal", async () => {
    const { store, scanner } = await setup([]);
    await scanner.scan(EXAMPLE_TOKEN).then((r) => store.log.push(`shown:${r.result}`));
    expect(store.log).toEqual(["begin", "scan:00000000-0000-4000-8000-000000000001:INVALID", "commit", "shown:INVALID"]);
  });

  it("if the write fails there is no result at all, and the ticket is still valid for the next scan", async () => {
    const { store, scanner } = await setup();
    store.failNextCommit();
    await expect(scanner.scan(EXAMPLE_TOKEN)).rejects.toThrow("disk full");
    expect(scans(store)).toHaveLength(0);
    expect((await store.getTicket(1, EXAMPLE_TICKET_ID))?.status).toBe("V");
    // The scanner still works afterwards.
    expect((await scanner.scan(EXAMPLE_TOKEN)).result).toBe("ADMITTED");
  });
});
