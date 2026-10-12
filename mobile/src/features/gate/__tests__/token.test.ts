import { describe, expect, it } from "vitest";

import { decodeBase64UrlCanonical, encodeBase64Url } from "../base64url";
import { decodePublicKey, verifyToken, warmUpVerifier } from "../token";
import { checkBinding, checkTicket, describeVerdict } from "../verdict";
import { buildPayload, EXAMPLE_TICKET_ID, EXAMPLE_TOKEN, fromHex, signToken, TEST_KEYS, TEST_PUBLIC_KEY_B64, TEST_PUBLIC_KEY_HEX } from "./helpers";

const keyFor = (id: string) => TEST_KEYS.get(id);
const toHex = (b: Uint8Array) => Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
const failure = (text: string) => {
  const r = verifyToken(text, keyFor);
  return r.ok ? "OK" : r.failure;
};

describe("canonical base64url", () => {
  it("round-trips every tail length", () => {
    for (let n = 0; n < 70; n++) {
      const bytes = Uint8Array.from({ length: n }, (_, i) => (i * 37 + n) & 0xff);
      expect(decodeBase64UrlCanonical(encodeBase64Url(bytes))).toEqual(bytes);
    }
  });

  it("refuses padding, standard-alphabet characters, whitespace and impossible lengths", () => {
    for (const bad of ["AQ==", "AQ=", "+_8", "/w", "A Q", "AQ\n", "A", "AAAAA"]) expect(decodeBase64UrlCanonical(bad)).toBeNull();
  });

  it("refuses unused bits that are not zero", () => {
    // One byte is two characters; the second carries 2 bits of it and 4 that must be zero.
    expect(decodeBase64UrlCanonical("AQ")).toEqual(Uint8Array.of(1));
    expect(decodeBase64UrlCanonical("AR")).toBeNull();
    // Two bytes are three characters; the third has 2 unused bits.
    expect(decodeBase64UrlCanonical("AQI")).toEqual(Uint8Array.of(1, 2));
    expect(decodeBase64UrlCanonical("AQJ")).toBeNull();
  });
});

describe("the worked example of docs/TICKET_FORMAT.md section 7", () => {
  const [, payloadText, signatureText] = EXAMPLE_TOKEN.split(".") as [string, string, string];

  it("decodes to the documented bytes", () => {
    expect(EXAMPLE_TOKEN).toHaveLength(160);
    expect(toHex(decodeBase64UrlCanonical(payloadText)!)).toBe("0100010b0f3a527c1e4d7e9a570c1b4d5e6f7000000000000000014e2d4c4f574552000000000000000000a1b2c3d4e5f60718");
    expect(toHex(decodeBase64UrlCanonical(signatureText)!)).toBe(
      "3b9d36e1b54597a7eafab3ca8ea275e4a365b39ab7c282eeccf106df799cd684b4c3e1528821f14178e1ce8b6712fd16a5fafbf2d4b3ccc47c2a63873329380e",
    );
    expect(toHex(decodePublicKey(TEST_PUBLIC_KEY_B64)!)).toBe(TEST_PUBLIC_KEY_HEX);
    expect(toHex(buildPayload())).toBe(toHex(decodeBase64UrlCanonical(payloadText)!));
  });

  it("verifies with the published test public key and reads the fields", () => {
    expect(verifyToken(EXAMPLE_TOKEN, keyFor)).toEqual({ ok: true, claims: { keyId: "k1", ticketId: EXAMPLE_TICKET_ID, eventId: 1n, gateGroup: "N-LOWER" } });
  });

  it("gives the same text when signed again with the test private key (Ed25519 is deterministic)", () => {
    expect(signToken()).toBe(EXAMPLE_TOKEN);
  });

  it("answers the document's table: admit, WRONG_EVENT, WRONG_GATE", () => {
    const r = verifyToken(EXAMPLE_TOKEN, keyFor);
    if (!r.ok) throw new Error("the example must verify");
    expect(checkBinding(r.claims, { eventId: 1, gateGroup: "N-LOWER" })).toBeNull();
    expect(checkTicket({ status: "V" })).toEqual({ result: "ADMITTED", reason: "OK" });
    expect(checkBinding(r.claims, { eventId: 2, gateGroup: "N-LOWER" })?.result).toBe("WRONG_EVENT");
    // The event is checked before the gate.
    expect(checkBinding(r.claims, { eventId: 2, gateGroup: "S-LOWER" })?.result).toBe("WRONG_EVENT");
    expect(checkBinding(r.claims, { eventId: 1, gateGroup: "S-LOWER" })?.result).toBe("WRONG_GATE");
    expect(checkBinding(r.claims, { eventId: 1, gateGroup: "N-UPPER" })?.result).toBe("WRONG_GATE");
  });

  it("refuses the document's three bad texts, each at its own step", () => {
    const edited = "TLT1.AQABCw86UnweTX6aVwwbTV5vcAAAAAAAAAACTi1MT1dFUgAAAAAAAAAAAKGyw9Tl9gcY.O5024bVFl6fq-rPKjqJ15KNls5q3woLuzPEG33mc1oS0w-FSiCHxQXjhzotnEv0Wpfr78tSzzMR8KmOHMyk4Dg";
    const lastCharB = "TLT1.AQABCw86UnweTX6aVwwbTV5vcAAAAAAAAAABTi1MT1dFUgAAAAAAAAAAAKGyw9Tl9gcY.O5024bVFl6fq-rPKjqJ15KNls5q3woLuzPEG33mc1oS0w-FSiCHxQXjhzotnEv0Wpfr78tSzzMR8KmOHMyk4DB";
    const keyTwo = "TLT1.AQACCw86UnweTX6aVwwbTV5vcAAAAAAAAAABTi1MT1dFUgAAAAAAAAAAAKGyw9Tl9gcY.DE9e5kBpR9W-8ITPlN1BbpwJOryzODHkdHCOMH-WjgKkwiwlALW4mxW4noPrqnYiu8SwmBlNqujm2aQyKQ9bAQ";
    expect(verifyToken(edited, keyFor)).toEqual({ ok: false, failure: "BAD_SIGNATURE", ticketId: EXAMPLE_TICKET_ID });
    expect(verifyToken(lastCharB, keyFor)).toEqual({ ok: false, failure: "BAD_ENCODING", ticketId: null });
    expect(verifyToken(keyTwo, keyFor)).toEqual({ ok: false, failure: "UNKNOWN_KEY", ticketId: EXAMPLE_TICKET_ID });
    // The third text IS correctly signed: a device that holds the key under k2 accepts it.
    expect(verifyToken(keyTwo, (id) => (id === "k2" ? fromHex(TEST_PUBLIC_KEY_HEX) : undefined)).ok).toBe(true);
  });
});

describe("one changed character", () => {
  const ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
  const other = (ch: string) => ALPHABET[(ALPHABET.indexOf(ch) + 1) % 64]!;

  it("anywhere in the payload or the signature is refused", () => {
    const outcomes = new Set<string>();
    for (let at = 5; at < EXAMPLE_TOKEN.length; at++) {
      if (EXAMPLE_TOKEN[at] === ".") continue;
      const changed = EXAMPLE_TOKEN.slice(0, at) + other(EXAMPLE_TOKEN[at]!) + EXAMPLE_TOKEN.slice(at + 1);
      const r = verifyToken(changed, keyFor);
      expect(r.ok, `position ${at}`).toBe(false);
      if (!r.ok) outcomes.add(r.failure);
    }
    // Most land on the signature; the version byte, the key number and the signature's last
    // character (unused bits) are caught earlier.
    expect(outcomes).toContain("BAD_SIGNATURE");
    expect(outcomes).toContain("UNKNOWN_VERSION");
    expect(outcomes).toContain("UNKNOWN_KEY");
    expect(outcomes).toContain("BAD_ENCODING");
  });

  it("in a fully significant signature character fails at the signature check, not earlier", () => {
    // The first signature character carries six bits of the signature.
    const at = EXAMPLE_TOKEN.lastIndexOf(".") + 1;
    const changed = EXAMPLE_TOKEN.slice(0, at) + other(EXAMPLE_TOKEN[at]!) + EXAMPLE_TOKEN.slice(at + 1);
    expect(failure(changed)).toBe("BAD_SIGNATURE");
  });

  it("the signature's last character has 16 spellings of the same two bits and exactly one is accepted", () => {
    const stem = EXAMPLE_TOKEN.slice(0, -1);
    const accepted = Array.from(ALPHABET).filter((ch) => verifyToken(stem + ch, keyFor).ok);
    expect(accepted).toEqual(["g"]);
    // The 15 others that spell the same signature bits are refused as non-canonical, before the signature is looked at.
    const sameBits = Array.from(ALPHABET).filter((ch) => ALPHABET.indexOf(ch) >> 4 === ALPHABET.indexOf("g") >> 4 && ch !== "g");
    expect(sameBits).toHaveLength(15);
    for (const ch of sameBits) expect(failure(stem + ch)).toBe("BAD_ENCODING");
  });
});

describe("each refusal has its own outcome", () => {
  const [, p, s] = EXAMPLE_TOKEN.split(".") as [string, string, string];

  it("shape and prefix: BAD_PREFIX", () => {
    for (const text of ["", "hello", `TLT2.${p}.${s}`, `tlt1.${p}.${s}`, ` TLT1.${p}.${s}`, `TLT1.${p}`, `TLT1.${p}.${s}.x`, "https://example.com/t/abc"]) expect(failure(text)).toBe("BAD_PREFIX");
  });

  it("encoding: BAD_ENCODING", () => {
    for (const text of [`TLT1.${p}=.${s}`, `TLT1.${p}.${s}=`, `TLT1.${p.replace("-", "+")}.${s.replace("-", "+")}`, `TLT1.${p}.${s}\n`, `TLT1.${p} .${s}`]) expect(failure(text)).toBe("BAD_ENCODING");
  });

  it("lengths: BAD_LENGTH", () => {
    expect(failure(`TLT1.${p}.${s.slice(0, -2)}`)).toBe("BAD_LENGTH"); // 63-byte signature
    expect(failure(`TLT1.${p}.${s}AA`)).toBe("BAD_LENGTH"); // 65-byte signature
    expect(failure(`TLT1..${s}`)).toBe("BAD_LENGTH"); // empty payload
    expect(failure(signToken({ extra: 1 }))).toBe("BAD_LENGTH"); // 52-byte payload, correctly signed
    expect(failure(`TLT1.${encodeBase64Url(buildPayload().subarray(0, 50))}.${s}`)).toBe("BAD_LENGTH");
  });

  it("version: UNKNOWN_VERSION, checked before the length and before the signature", () => {
    expect(failure(signToken({ version: 2 }))).toBe("UNKNOWN_VERSION");
    expect(failure(signToken({ version: 0 }))).toBe("UNKNOWN_VERSION");
    expect(failure(signToken({ version: 2, extra: 9 }))).toBe("UNKNOWN_VERSION");
  });

  it("key: UNKNOWN_KEY, and the ticket ID is readable", () => {
    expect(verifyToken(signToken({ keyNumber: 7 }), keyFor)).toEqual({ ok: false, failure: "UNKNOWN_KEY", ticketId: EXAMPLE_TICKET_ID });
    expect(failure(signToken({ keyNumber: 0 }))).toBe("UNKNOWN_KEY"); // k0, the server's throw-away dev key
  });

  it("signature: BAD_SIGNATURE for another key's signature and for a signature over the bare payload", () => {
    const otherSeed = fromHex("4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb");
    expect(failure(signToken({}, otherSeed))).toBe("BAD_SIGNATURE");
    expect(failure(`TLT1.${p}.${encodeBase64Url(new Uint8Array(64))}`)).toBe("BAD_SIGNATURE");
  });

  it("fields: MALFORMED_PAYLOAD for a correctly signed payload with bad fields", () => {
    expect(failure(signToken({ eventId: 0n }))).toBe("MALFORMED_PAYLOAD");
    expect(failure(signToken({ eventId: 1n << 63n }))).toBe("MALFORMED_PAYLOAD");
    expect(failure(signToken({ gateGroup: "n-lower" }))).toBe("MALFORMED_PAYLOAD");
    expect(failure(signToken({ gateGroup: "NLOWER" }))).toBe("MALFORMED_PAYLOAD");
    expect(failure(signToken({ gateGroupBytes: new Uint8Array(16) }))).toBe("MALFORMED_PAYLOAD");
    // A zero byte before the end of the text.
    expect(failure(signToken({ gateGroupBytes: Uint8Array.from([0x4e, 0x00, 0x2d, 0x4c, 0x4f, 0x57, 0x45, 0x52]) }))).toBe("MALFORMED_PAYLOAD");
    // The largest legal values pass.
    expect(verifyToken(signToken({ eventId: (1n << 63n) - 1n, gateGroup: "ABCDEFGHIJ-UPPER" }), keyFor)).toMatchObject({ ok: true, claims: { eventId: (1n << 63n) - 1n, gateGroup: "ABCDEFGHIJ-UPPER" } });
  });

  it("a text over 512 characters is refused before anything else", () => {
    expect(failure(`TLT1.${"A".repeat(600)}.${s}`)).toBe("TOO_LONG");
  });

  it("the signature must be over the labelled message, not the bare payload", () => {
    // A verifier that forgot the context label would accept a signature over the payload alone.
    const seen: number[] = [];
    verifyToken(EXAMPLE_TOKEN, keyFor, (_sig, message) => {
      seen.push(message.length);
      return true;
    });
    expect(seen).toEqual([69]);
  });
});

describe("manifest steps", () => {
  it("not in the manifest: INVALID; status U: DUPLICATE; status V: ADMITTED", () => {
    expect(checkTicket(null)).toEqual({ result: "INVALID", reason: "NOT_IN_MANIFEST" });
    expect(checkTicket({ status: "U" })).toEqual({ result: "DUPLICATE", reason: "ALREADY_USED" });
    expect(checkTicket({ status: "V" })).toEqual({ result: "ADMITTED", reason: "OK" });
  });
});

describe("the words on the result screen", () => {
  it("say what to do, and name the right gate for a wrong-gate ticket", () => {
    expect(describeVerdict("ADMITTED", "OK", "N-LOWER").title).toBe("Let them in");
    expect(describeVerdict("DUPLICATE", "ALREADY_USED", "N-LOWER").title).toBe("Already used");
    expect(describeVerdict("WRONG_GATE", "WRONG_GATE", "W-LOWER")).toEqual({ title: "Wrong gate", detail: "This ticket is for W-LOWER. Send them to that gate." });
    expect(describeVerdict("WRONG_EVENT", "WRONG_EVENT", "N-LOWER").title).toBe("Wrong event");
    for (const reason of ["NOT_IN_MANIFEST", "BAD_SIGNATURE", "BAD_PREFIX", "BAD_ENCODING", "BAD_LENGTH", "UNKNOWN_KEY", "MALFORMED_PAYLOAD", "TOO_LONG"]) {
      expect(describeVerdict("INVALID", reason, null).title).toBe("Not a valid ticket");
    }
    expect(describeVerdict("INVALID", "UNKNOWN_VERSION", null).detail).toContain("Update the app");
    // A server verdict has no device reason: the result alone gives the words.
    expect(describeVerdict("DUPLICATE", "", null).title).toBe("Already used");
    expect(describeVerdict("INVALID", "", null).title).toBe("Not a valid ticket");
  });
});

describe("warm-up", () => {
  it("verifies its built-in RFC 8032 vector, so it really exercises the verifier", () => {
    expect(warmUpVerifier()).toBe(true);
  });
});
