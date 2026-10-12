// The `TLT1` ticket text: steps 1 to 8 of docs/TICKET_FORMAT.md section 4.2, in that order.
// Pure: no React, no storage, no clock. Everything here runs with the network off.

import { ed25519 } from "@noble/curves/ed25519.js";

import { decodeBase64UrlCanonical } from "./base64url";

export const TOKEN_PREFIX = "TLT1";
export const TOKEN_MAX_LENGTH = 512;
const VERSION = 0x01;
const PAYLOAD_LENGTH = 51;
const SIGNATURE_LENGTH = 64;
const PUBLIC_KEY_LENGTH = 32;
const GATE_GROUP = /^[A-Z0-9]+-(LOWER|UPPER)$/;

// "talanta.ticket.v1" and one zero byte. Signer and verifier each put it in front of the
// payload, so a signature made for anything else can never pass as a ticket.
const CONTEXT_LABEL = Uint8Array.from([...Array.from("talanta.ticket.v1", (c) => c.charCodeAt(0)), 0]);

/** Why a text is not a ticket. One value per failing step, as the server's verifier has. */
export type TokenFailure =
  | "TOO_LONG" // section 6: over 512 characters, refused before anything else
  | "BAD_PREFIX" // step 1
  | "BAD_ENCODING" // step 2
  | "BAD_LENGTH" // steps 3 and 5
  | "UNKNOWN_VERSION" // step 4
  | "UNKNOWN_KEY" // step 6
  | "BAD_SIGNATURE" // step 7
  | "MALFORMED_PAYLOAD"; // step 8

export interface TicketClaims {
  keyId: string;
  /** Lower-case UUID text. */
  ticketId: string;
  eventId: bigint;
  gateGroup: string;
}

export type TokenCheck =
  | { ok: true; claims: TicketClaims }
  /**
   * `ticketId` is set once the text got as far as step 6: the ID could be read, whether or
   * not it can be believed. Such a scan is recorded and uploaded; one without is not.
   */
  | { ok: false; failure: TokenFailure; ticketId: string | null };

export type SignatureVerifier = (signature: Uint8Array, message: Uint8Array, publicKey: Uint8Array) => boolean;

/**
 * Plain Ed25519 (RFC 8032), not the pre-hashed or context variants. `zip215: false` keeps
 * to the RFC's strict decoding, the closest match to the server's Go verifier.
 */
export const verifyEd25519: SignatureVerifier = (signature, message, publicKey) => {
  try {
    return ed25519.verify(signature, message, publicKey, { zip215: false });
  } catch {
    // A public key or signature that is not even a point: not valid, never a crash.
    return false;
  }
};

const fromHex = (text: string) => Uint8Array.from(text.match(/../g) ?? [], (h) => parseInt(h, 16));

/**
 * Does one real verification so the first ticket of the day is not the slow one: the
 * library builds its tables on first use, which on a slow phone takes far longer than a
 * scan should. The input is test 1 of RFC 8032 section 7.1 (an empty message), public
 * numbers that have nothing to do with any ticket. Returns whether it verified.
 */
export function warmUpVerifier(): boolean {
  return verifyEd25519(
    fromHex("e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b"),
    new Uint8Array(0),
    fromHex("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"),
  );
}

/** A manifest's `public_key` as bytes, or null when it is not 32 canonical base64url bytes. */
export function decodePublicKey(text: string): Uint8Array | null {
  const bytes = decodeBase64UrlCanonical(text);
  return bytes && bytes.length === PUBLIC_KEY_LENGTH ? bytes : null;
}

const hex = (bytes: Uint8Array) => Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");

function readTicketId(payload: Uint8Array): string {
  const h = hex(payload.subarray(3, 19));
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

/** The gate group text of bytes 27 to 42, or null when it is malformed (section 2). */
function readGateGroup(payload: Uint8Array): string | null {
  const field = payload.subarray(27, 43);
  let end = field.length;
  while (end > 0 && field[end - 1] === 0) end--;
  let text = "";
  for (let i = 0; i < end; i++) {
    // A zero byte before the end of the text, or anything outside ASCII, is malformed.
    if (field[i] === 0 || field[i]! > 0x7f) return null;
    text += String.fromCharCode(field[i]!);
  }
  return GATE_GROUP.test(text) ? text : null;
}

/**
 * Checks a scanned text and reads its fields. `keyFor` is the manifest's key set: the
 * public key for a key ID such as `k1`, or undefined.
 *
 * Nothing in the payload is believed before the signature check; the steps before it only
 * decide whether there is something to verify, and with which key.
 */
export function verifyToken(text: string, keyFor: (keyId: string) => Uint8Array | undefined, verifySignature: SignatureVerifier = verifyEd25519): TokenCheck {
  const fail = (failure: TokenFailure, ticketId: string | null = null): TokenCheck => ({ ok: false, failure, ticketId });

  if (text.length > TOKEN_MAX_LENGTH) return fail("TOO_LONG");

  // 1. Exactly three parts, the first exactly `TLT1`.
  const parts = text.split(".");
  if (parts.length !== 3 || parts[0] !== TOKEN_PREFIX) return fail("BAD_PREFIX");

  // 2. Canonical base64url without padding.
  const payload = decodeBase64UrlCanonical(parts[1]!);
  const signature = decodeBase64UrlCanonical(parts[2]!);
  if (!payload || !signature) return fail("BAD_ENCODING");

  // 3. A 64-byte signature and a payload that is not empty.
  if (signature.length !== SIGNATURE_LENGTH || payload.length === 0) return fail("BAD_LENGTH");

  // 4. The version byte, before the length: another version may have another length.
  if (payload[0] !== VERSION) return fail("UNKNOWN_VERSION");

  // 5. Exactly 51 bytes.
  if (payload.length !== PAYLOAD_LENGTH) return fail("BAD_LENGTH");

  // From here on a ticket ID can be read (not yet believed).
  const ticketId = readTicketId(payload);

  // 6. The key the text names.
  const keyId = `k${(payload[1]! << 8) | payload[2]!}`;
  const publicKey = keyFor(keyId);
  if (!publicKey) return fail("UNKNOWN_KEY", ticketId);

  // 7. The signature over `context label || payload`.
  const message = new Uint8Array(CONTEXT_LABEL.length + payload.length);
  message.set(CONTEXT_LABEL, 0);
  message.set(payload, CONTEXT_LABEL.length);
  if (!verifySignature(signature, message, publicKey)) return fail("BAD_SIGNATURE", ticketId);

  // 8. The fields. Event ID: 8 bytes big-endian, not zero and below 2^63.
  let eventId = 0n;
  for (let i = 19; i < 27; i++) eventId = (eventId << 8n) | BigInt(payload[i]!);
  if (eventId === 0n || (payload[19]! & 0x80) !== 0) return fail("MALFORMED_PAYLOAD", ticketId);
  const gateGroup = readGateGroup(payload);
  if (gateGroup === null) return fail("MALFORMED_PAYLOAD", ticketId);

  return { ok: true, claims: { keyId, ticketId, eventId, gateGroup } };
}
