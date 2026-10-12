// The upload queue (docs/TICKET_FORMAT.md section 4.3). A scan leaves the queue only when a
// 200 answer lists its `client_scan_id`; everything else keeps it. Pure: the request is passed in.

import type { components } from "@talanta/shared";

import type { GateStore, StoredScan } from "./store";

type Schemas = components["schemas"];
export type ScanBatch = Schemas["ScanBatch"];
type BatchResult = Schemas["ScanBatchResult"]["results"][number];

/** The contract's limit on scans per call. */
export const MAX_BATCH = 500;

/** What one `POST /gate/scans` came to. */
export type SendOutcome =
  | { kind: "ok"; results: BatchResult[] }
  /** Network error, 409 `EVENT_BUSY`, 429 or 5xx: the very same batch goes again later. */
  | { kind: "retry"; why: "network" | "busy" | "rate_limited" | "server" | "signed_out"; retryAfterSeconds: number | null }
  /** 413: split the batch. */
  | { kind: "too_large" }
  /** 400 (or another refusal): the batch will never be accepted as it is. The scans are kept. */
  | { kind: "rejected"; message: string };

/**
 * What an HTTP answer other than 200 means for the batch. One place, so the rule "a scan
 * leaves the queue only on a 200 that lists it" cannot be bent by a caller.
 */
export function outcomeForStatus(status: number, retryAfterSeconds: number | null, message: string): Exclude<SendOutcome, { kind: "ok" }> {
  if (status === 413) return { kind: "too_large" };
  if (status === 409) return { kind: "retry", why: "busy", retryAfterSeconds };
  if (status === 429) return { kind: "retry", why: "rate_limited", retryAfterSeconds };
  // The session ended or the account lost its gate role: nothing is wrong with the scans.
  if (status === 401 || status === 403) return { kind: "retry", why: "signed_out", retryAfterSeconds: null };
  if (status >= 400 && status < 500) return { kind: "rejected", message };
  // 5xx, and anything that is not an answer of this API at all (a proxy's page, a redirect).
  return { kind: "retry", why: "server", retryAfterSeconds };
}

export type SendBatch = (batch: ScanBatch) => Promise<SendOutcome>;

export interface UploadDeps {
  store: GateStore;
  send: SendBatch;
  deviceId: string;
  now: () => Date;
}

export interface UploadReport {
  /** Scans a 200 answer listed. */
  answered: number;
  /** Of those, how many the server decided differently from the device. */
  overridden: number;
  /** Why the run stopped early; null when it got through everything it could. */
  stopped: Exclude<SendOutcome, { kind: "ok" }> | null;
}

function toWire(scan: StoredScan): Schemas["Scan"] {
  if (scan.ticketId === null) throw new Error("A scan with no ticket ID cannot be uploaded.");
  return {
    client_scan_id: scan.clientScanId,
    ticket_id: scan.ticketId,
    event_id: scan.eventId,
    gate_group: scan.gateGroup,
    scanned_at: scan.scannedAt,
    result: scan.result,
    // Always sent: without it the server would rely on the ticket ID alone.
    ...(scan.token !== null ? { token: scan.token } : {}),
  };
}

/** Stores the answers of one 200 in one write. Returns how many scans of the batch it listed. */
async function storeAnswers(deps: UploadDeps, batch: StoredScan[], results: BatchResult[]): Promise<{ answered: number; overridden: number }> {
  const sent = new Map(batch.map((s) => [s.clientScanId, s]));
  const uploadedAt = deps.now().toISOString();
  return deps.store.transaction(async (tx) => {
    let answered = 0;
    let overridden = 0;
    const admittedHere = new Map<number, Set<string>>();
    for (const r of results) {
      const scan = sent.get(r.client_scan_id);
      // Not one of ours (or listed twice): nothing to take from it.
      if (!scan) continue;
      sent.delete(r.client_scan_id);
      answered++;
      // `overridden` is recomputed here against what the steward saw, so a resent scan whose
      // result changed later is caught whatever the server compares it with.
      const changed = r.overridden || r.result !== scan.result;
      if (changed) overridden++;
      await tx.answerScan(scan.clientScanId, { serverResult: r.result, overridden: changed, firstAdmittedAt: r.first_admitted_at, uploadedAt });

      // The ticket's status on the server now. The answer is about the ticket the scan was
      // stored with; a ticket of another event is simply not in this event's set.
      const ticketId = r.ticket_id.toLowerCase();
      if (r.ticket_status === "VOID" || r.ticket_status === "TRANSFERRED") {
        await tx.deleteTickets(scan.eventId, [ticketId]);
      } else if (r.ticket_status === "USED") {
        await tx.setTicketStatus(scan.eventId, ticketId, "U");
      } else if (r.ticket_status === "VALID") {
        let mine = admittedHere.get(scan.eventId);
        if (!mine) admittedHere.set(scan.eventId, (mine = await tx.admittedTicketIds(scan.eventId)));
        // A ticket this device let in stays used, whatever the server says.
        if (!mine.has(ticketId)) await tx.setTicketStatus(scan.eventId, ticketId, "V");
      }
    }
    return { answered, overridden };
  });
}

type Page = (limit: number, afterSeq: number) => Promise<StoredScan[]>;

async function sendAll(deps: UploadDeps, page: Page): Promise<UploadReport> {
  const report: UploadReport = { answered: 0, overridden: 0, stopped: null };
  let size = MAX_BATCH;
  let afterSeq = 0;
  for (;;) {
    const batch = await page(size, afterSeq);
    if (batch.length === 0) return report;

    const outcome = await deps.send({ device_id: deps.deviceId, scans: batch.map(toWire) });
    if (outcome.kind === "too_large") {
      // One scan cannot be split: stop and say so, keeping it.
      if (batch.length === 1) return { ...report, stopped: { kind: "rejected", message: "The server says one scan is too large to upload." } };
      size = Math.ceil(batch.length / 2);
      continue;
    }
    if (outcome.kind !== "ok") return { ...report, stopped: outcome };

    const stored = await storeAnswers(deps, batch, outcome.results);
    report.answered += stored.answered;
    report.overridden += stored.overridden;
    // Move past this batch either way: a scan the answer did not list stays in the queue for
    // the next run, and this run cannot loop on it.
    afterSeq = batch[batch.length - 1]!.seq;
  }
}

/** Uploads the queue, oldest first, up to 500 scans a call. */
export function uploadPending(deps: UploadDeps): Promise<UploadReport> {
  return sendAll(deps, (limit, afterSeq) => deps.store.pendingScans(limit, afterSeq));
}

/**
 * Sends the scans last answered ADMITTED once more. Nothing is pushed to a device when
 * another phone later uploads an earlier scan of the same ticket; asking again, before the
 * session is closed, is the only way to learn that one of them became DUPLICATE.
 */
export function recheckAdmitted(deps: UploadDeps, eventId: number): Promise<UploadReport> {
  return sendAll(deps, (limit, afterSeq) => deps.store.admittedAnswers(eventId, limit, afterSeq));
}

/** One line for the steward about why uploading stopped. */
export function describeStop(stopped: UploadReport["stopped"]): string | null {
  if (!stopped) return null;
  if (stopped.kind === "rejected") return `The server refused the scans: ${stopped.message} They are kept on this phone. Tell your supervisor.`;
  if (stopped.kind === "too_large") return "The scans could not be uploaded. They are kept on this phone.";
  switch (stopped.why) {
    case "network":
      return "No connection. Scans are kept on this phone and upload when it returns.";
    case "busy":
      return "The organiser is updating this event. Scans will upload in a moment.";
    case "rate_limited":
      return "Uploading too fast. Scans will upload in a moment.";
    case "signed_out":
      return "Sign in again to upload the scans. They are kept on this phone.";
    default:
      return "The server had a problem. Scans are kept on this phone and will be sent again.";
  }
}
