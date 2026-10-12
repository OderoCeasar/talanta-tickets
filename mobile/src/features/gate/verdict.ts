// Steps 9 to 12 of docs/TICKET_FORMAT.md section 4.2, and the words the steward reads.
// Pure. The order is part of the format: event, then gate, then the manifest, then "used".

import type { HeldStatus, ScanResult, TicketRow } from "./store";
import type { TicketClaims, TokenFailure } from "./token";

/** The event and gate group the steward set this device to. */
export interface GateDevice {
  eventId: number;
  gateGroup: string;
}

export type ScanReason = TokenFailure | "WRONG_EVENT" | "WRONG_GATE" | "NOT_IN_MANIFEST" | "ALREADY_USED" | "OK";

export interface Verdict {
  result: ScanResult;
  reason: ScanReason;
}

/**
 * Steps 9 and 10, from the signed fields alone. Null when both pass.
 *
 * The event comes first so a real ticket for another match reads "wrong match", not
 * "forgery" (it is not in this event's manifest). The gate comes before the manifest so a
 * fan at the wrong stand is sent on without the ticket being marked used.
 */
export function checkBinding(claims: TicketClaims, device: GateDevice): Verdict | null {
  if (claims.eventId !== BigInt(device.eventId)) return { result: "WRONG_EVENT", reason: "WRONG_EVENT" };
  if (claims.gateGroup !== device.gateGroup) return { result: "WRONG_GATE", reason: "WRONG_GATE" };
  return null;
}

/**
 * Steps 11 and 12, from the device's ticket set. A ticket this device let in is `U` there,
 * so the one status check covers "the manifest says used" and "already scanned here".
 */
export function checkTicket(ticket: Pick<TicketRow, "status"> | null): Verdict {
  if (!ticket) return { result: "INVALID", reason: "NOT_IN_MANIFEST" };
  const status: HeldStatus = ticket.status;
  if (status !== "V") return { result: "DUPLICATE", reason: "ALREADY_USED" };
  return { result: "ADMITTED", reason: "OK" };
}

export interface VerdictText {
  title: string;
  detail: string;
}

/**
 * Plain words for a result. `ticketGateGroup` is the gate group signed into the ticket,
 * known only when the signature was good.
 */
export function describeVerdict(result: ScanResult, reason: string, ticketGateGroup: string | null): VerdictText {
  switch (reason as ScanReason) {
    case "OK":
      return { title: "Let them in", detail: "Valid ticket." };
    case "ALREADY_USED":
      return { title: "Already used", detail: "This ticket has already been scanned. Do not let them in." };
    case "WRONG_GATE":
      return { title: "Wrong gate", detail: ticketGateGroup ? `This ticket is for ${ticketGateGroup}. Send them to that gate.` : "This ticket is for another gate." };
    case "WRONG_EVENT":
      return { title: "Wrong event", detail: "This ticket is for a different match." };
    case "NOT_IN_MANIFEST":
      return { title: "Not a valid ticket", detail: "This ticket is not on the list for this event. It may have been refunded or cancelled." };
    case "UNKNOWN_VERSION":
      return { title: "Not a valid ticket", detail: "This app cannot read this kind of ticket. Update the app, then try again." };
    case "UNKNOWN_KEY":
      return { title: "Not a valid ticket", detail: "This ticket's signing key is not on this phone. Sync, then try again." };
    case "BAD_SIGNATURE":
      return { title: "Not a valid ticket", detail: "This code has been changed or forged." };
    case "MALFORMED_PAYLOAD":
      return { title: "Not a valid ticket", detail: "This code is damaged." };
    case "TOO_LONG":
    case "BAD_PREFIX":
    case "BAD_ENCODING":
    case "BAD_LENGTH":
      return { title: "Not a valid ticket", detail: "This is not a Talanta ticket code." };
    default:
      break;
  }
  // A result with no known reason: the server's verdict, shown in the history.
  switch (result) {
    case "ADMITTED":
      return { title: "Let in", detail: "Valid ticket." };
    case "DUPLICATE":
      return { title: "Already used", detail: "Another phone scanned this ticket first." };
    case "WRONG_GATE":
      return { title: "Wrong gate", detail: "This ticket is for another gate." };
    case "WRONG_EVENT":
      return { title: "Wrong event", detail: "This ticket is for a different match." };
    default:
      return { title: "Not a valid ticket", detail: "The server does not accept this ticket. It may have been refunded or cancelled." };
  }
}
