// base64url without padding (RFC 4648 section 5), strict: a text is accepted only when it is
// THE encoding of its bytes. No React Native import: the ticket checks and their tests use it.

const ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
const VALUE = new Map<string, number>(Array.from(ALPHABET, (ch, i) => [ch, i]));

export function encodeBase64Url(bytes: Uint8Array): string {
  let out = "";
  for (let i = 0; i < bytes.length; i += 3) {
    const b0 = bytes[i]!;
    const b1 = bytes[i + 1];
    const b2 = bytes[i + 2];
    out += ALPHABET[b0 >> 2];
    out += ALPHABET[((b0 & 0x03) << 4) | ((b1 ?? 0) >> 4)];
    if (b1 !== undefined) out += ALPHABET[((b1 & 0x0f) << 2) | ((b2 ?? 0) >> 6)];
    if (b2 !== undefined) out += ALPHABET[b2 & 0x3f];
  }
  return out;
}

/**
 * The bytes of a canonical base64url text, or null for anything else: a character outside
 * the alphabet (padding and whitespace included), an impossible length, or unused bits that
 * are not zero. The last is checked the way docs/TICKET_FORMAT.md section 1 prescribes:
 * decode, encode again, and require the same text back.
 */
export function decodeBase64UrlCanonical(text: string): Uint8Array | null {
  if (text.length % 4 === 1) return null;
  const out = new Uint8Array(Math.floor((text.length * 3) / 4));
  let buffer = 0;
  let bits = 0;
  let at = 0;
  for (const ch of text) {
    const v = VALUE.get(ch);
    if (v === undefined) return null;
    buffer = ((buffer << 6) | v) & 0xffff;
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      out[at++] = (buffer >> bits) & 0xff;
    }
  }
  return encodeBase64Url(out) === text ? out : null;
}
