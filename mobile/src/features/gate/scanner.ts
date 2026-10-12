// One scan, start to finish: check the text, WRITE the scan, and only then hand back what to
// show (docs/TICKET_FORMAT.md section 4.3). Pure: store, clock and ID maker are passed in.

import type { GateStore, ScanResult } from "./store";
import { verifyToken, type SignatureVerifier } from "./token";
import { checkBinding, checkTicket, type GateDevice, type ScanReason, type Verdict } from "./verdict";

export interface ScanOutcome {
  result: ScanResult;
  reason: ScanReason;
  /** Null when the text could not be read as far as a ticket ID. */
  ticketId: string | null;
  /** The gate group signed into the ticket; null unless the signature was good. */
  ticketGateGroup: string | null;
  clientScanId: string;
  scannedAt: string;
  /** Whether the scan went into the upload queue. */
  queued: boolean;
}

export interface ScannerDeps {
  store: GateStore;
  /** The manifest's public keys by key ID (`k1`). */
  keys: ReadonlyMap<string, Uint8Array>;
  now: () => Date;
  /** A new random UUID. */
  newId: () => string;
  verifySignature?: SignatureVerifier;
}

export interface Scanner {
  /** Resolves only after the scan is stored. A rejection means it was NOT stored: show an error, not a result. */
  scan(text: string): Promise<ScanOutcome>;
}

export function createScanner(deps: ScannerDeps, device: GateDevice): Scanner {
  const { store, keys, now, newId } = deps;

  async function scanOne(text: string): Promise<ScanOutcome> {
    const check = verifyToken(text, (keyId) => keys.get(keyId), deps.verifySignature);
    const clientScanId = newId();
    const scannedAt = now().toISOString();
    const base = { clientScanId, eventId: device.eventId, gateGroup: device.gateGroup, scannedAt };

    // Steps 1 to 5 failed: there is no ticket ID, so nothing the server could take. Kept for
    // this phone's history only, without the text (it could be any QR code at all).
    if (!check.ok && check.ticketId === null) {
      await store.transaction((tx) => tx.insertScan({ ...base, ticketId: null, result: "INVALID", reason: check.failure, token: null, upload: "LOCAL" }));
      return { result: "INVALID", reason: check.failure, ticketId: null, ticketGateGroup: null, clientScanId, scannedAt, queued: false };
    }

    const ticketId = check.ok ? check.claims.ticketId : check.ticketId!;
    // One write: read the ticket, decide, store the scan and (if admitted) mark the ticket
    // used. Reading inside the write is what makes a second scan of the same ticket see `U`.
    const verdict = await store.transaction(async (tx): Promise<Verdict> => {
      let v: Verdict;
      if (!check.ok) v = { result: "INVALID", reason: check.failure };
      else v = checkBinding(check.claims, device) ?? checkTicket(await tx.getTicket(device.eventId, ticketId));
      await tx.insertScan({ ...base, ticketId, result: v.result, reason: v.reason, token: text, upload: "PENDING" });
      if (v.result === "ADMITTED") await tx.setTicketStatus(device.eventId, ticketId, "U");
      return v;
    });

    return { ...verdict, ticketId, ticketGateGroup: check.ok ? check.claims.gateGroup : null, clientScanId, scannedAt, queued: true };
  }

  // Scans are taken one at a time, in the order they were read.
  let last: Promise<unknown> = Promise.resolve();
  return {
    scan(text) {
      const next = last.then(() => scanOne(text));
      last = next.catch(() => undefined);
      return next;
    },
  };
}
