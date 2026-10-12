// Test-only: makes `TLT1` texts with the published TEST key of docs/TICKET_FORMAT.md
// section 7 (RFC 8032 test 1). The app itself never signs anything.

import { ed25519 } from "@noble/curves/ed25519.js";

import { encodeBase64Url } from "../base64url";

export const fromHex = (hex: string) => Uint8Array.from(hex.match(/../g) ?? [], (h) => parseInt(h, 16));

export const TEST_SEED = fromHex("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60");
export const TEST_PUBLIC_KEY_HEX = "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a";
export const TEST_PUBLIC_KEY_B64 = "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo";
export const TEST_KEYS = new Map([["k1", fromHex(TEST_PUBLIC_KEY_HEX)]]);

/** The worked example's QR text, copied from the document character for character. */
export const EXAMPLE_TOKEN =
  "TLT1.AQABCw86UnweTX6aVwwbTV5vcAAAAAAAAAABTi1MT1dFUgAAAAAAAAAAAKGyw9Tl9gcY.O5024bVFl6fq-rPKjqJ15KNls5q3woLuzPEG33mc1oS0w-FSiCHxQXjhzotnEv0Wpfr78tSzzMR8KmOHMyk4Dg";
export const EXAMPLE_TICKET_ID = "0b0f3a52-7c1e-4d7e-9a57-0c1b4d5e6f70";

const LABEL = Uint8Array.from([...Array.from("talanta.ticket.v1", (c) => c.charCodeAt(0)), 0]);

export interface Fields {
  version?: number;
  keyNumber?: number;
  ticketId?: string;
  eventId?: bigint;
  /** Raw 16 bytes of the gate group field; overrides `gateGroup`. */
  gateGroupBytes?: Uint8Array;
  gateGroup?: string;
  /** Extra bytes appended to the payload (to make a wrong length). */
  extra?: number;
}

export function buildPayload(f: Fields = {}): Uint8Array {
  const payload = new Uint8Array(51 + (f.extra ?? 0));
  payload[0] = f.version ?? 1;
  const key = f.keyNumber ?? 1;
  payload[1] = key >> 8;
  payload[2] = key & 0xff;
  payload.set(fromHex((f.ticketId ?? EXAMPLE_TICKET_ID).replace(/-/g, "")), 3);
  let event = f.eventId ?? 1n;
  for (let i = 26; i >= 19; i--) {
    payload[i] = Number(event & 0xffn);
    event >>= 8n;
  }
  const group = f.gateGroupBytes ?? Uint8Array.from(Array.from(f.gateGroup ?? "N-LOWER", (c) => c.charCodeAt(0)));
  payload.set(group, 27);
  payload.set(fromHex("a1b2c3d4e5f60718"), 43);
  return payload;
}

/** A correctly signed text for the fields. */
export function signToken(f: Fields = {}, seed: Uint8Array = TEST_SEED): string {
  const payload = buildPayload(f);
  const message = new Uint8Array(LABEL.length + payload.length);
  message.set(LABEL);
  message.set(payload, LABEL.length);
  return `TLT1.${encodeBase64Url(payload)}.${encodeBase64Url(ed25519.sign(message, seed))}`;
}

/** A random-looking but fixed UUID for test ticket number `n`. */
export const ticketIdOf = (n: number) => `00000000-0000-4000-8000-${n.toString(16).padStart(12, "0")}`;

/** The fields with an all-zero signature: for tests that replace the signature check. */
export function unsignedToken(f: Fields = {}): string {
  return `TLT1.${encodeBase64Url(buildPayload(f))}.${encodeBase64Url(new Uint8Array(64))}`;
}
