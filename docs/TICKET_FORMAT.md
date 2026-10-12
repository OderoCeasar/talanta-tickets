# Ticket QR format (`TLT1`)

This is the specification of the text inside a Talanta ticket's QR code, and of how a gate
device checks it with no network. The mobile gate scanner is built from this file.

The server side is `backend/internal/ticketing` (`token.go`, `keys.go`). The byte layout
table and the worked example below are **generated from that code**: the test
`TestTicketFormatDocMatchesCode` rebuilds both and fails if this file differs by one
character. If you change the format, change the code, then run

```
cd backend && TICKET_FORMAT_UPDATE=1 go test ./internal/ticketing -run TestTicketFormatDocMatchesCode
```

and tell the mobile engineer.

## 1. What the QR holds

```
TLT1.<payload>.<signature>
```

- `TLT1` is fixed, upper case. It names this text format.
- `<payload>` is 51 bytes (section 2) in base64url **without padding** (RFC 4648 section 5:
  `A-Z a-z 0-9 - _`, no `=`).
- `<signature>` is a 64-byte Ed25519 signature (section 3), encoded the same way.
- The two dots are the only separators. There is no whitespace and no line break anywhere.

The API gives this text, `TLT1.<payload>.<signature>`, as `qr_token` on every ticket (the
contract's pattern for it is `^TLT1\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`). The app draws it
as a QR code itself;
`GET /v1/tickets/{ticketId}/qr` draws the same text as a PNG.

The encoding is **canonical**: a given payload and signature have exactly one text. A
signature is 64 bytes, so its last (86th) character carries only two bits of it and the
other four bits must be zero. A decoder that ignores those bits would accept sixteen
different last characters as the same signature. A scanner must refuse such a text. The
portable way to check, whatever base64 library you have: decode, encode the bytes again,
and require the result to equal the text you were given.

## 2. Payload, version 1

Fixed size, fixed offsets. All integers are unsigned and **big-endian** (most significant
byte first).

<!-- layout:begin -->
| Offset | Size | Field | Encoding |
|---|---|---|---|
| 0 | 1 | version | unsigned integer, always `0x01` |
| 1 | 2 | key number | unsigned integer, big-endian; the key ID is the letter `k` followed by this number in decimal (`k1`) |
| 3 | 16 | ticket ID | the UUID's 16 bytes in the order its text form is written (RFC 9562) |
| 19 | 8 | event ID | unsigned integer, big-endian; never zero and below 2^63 |
| 27 | 16 | gate group | ASCII text such as `N-LOWER`, padded on the right with `0x00` bytes |
| 43 | 8 | nonce | random bytes, opaque |

Total: **51 bytes**, which is exactly 68 base64url characters. The signature is 64 bytes, 86 characters. The whole QR text is always **160 characters**.
<!-- layout:end -->

Notes on the fields:

- **version** selects this table. A scanner that sees any other value must refuse the
  ticket (and should tell the steward the app needs updating); it must not guess.
- **key number / key ID.** The server names its signing keys `k1`, `k2`, … (the gate
  manifest's `keys[].key_id`, the `key_id` stored with each ticket). The payload carries
  the number. `k0` is reserved for a throw-away key the server makes for itself in
  development; a real manifest never contains it.
- **ticket ID** is the UUID the API calls `Ticket.id` and the manifest lists. Byte 0 of the
  field is the first two hex digits of the text form.
- **event ID** is the `id` of the event, the same number as in the API.
- **gate group** is section 5. The text is 1 to 16 ASCII characters matching
  `^[A-Z0-9]+-(LOWER|UPPER)$`; the unused bytes on the right are `0x00`. To read it, drop
  the trailing zero bytes. A zero byte before the end of the text, or text that does not
  match the pattern, is malformed.
- **nonce** is 8 random bytes chosen when the ticket is issued. A scanner does not look at
  it. It is there so that two tickets can never have the same payload, and so that nothing
  about a ticket's text can be worked out from its ID.

## 3. Signature

```
message   = context label || payload
signature = Ed25519-Sign(private key, message)          (RFC 8032, "pure" Ed25519)
```

The context label is the 17 ASCII characters `talanta.ticket.v1` followed by one zero byte:
18 bytes. The message is therefore always 18 + 51 = 69 bytes. The label is not in the QR
text; signer and verifier each add it.

Why the label: the signature is made over "this is a Talanta ticket, version 1: …", not
over the bare payload, so a signature the same key might one day make for some other purpose
can never be passed off as a ticket, and a ticket signature is useless anywhere else.

Use plain Ed25519, not Ed25519ph and not Ed25519ctx. In JavaScript that is
`nacl.sign.detached.verify(message, signature, publicKey)` (tweetnacl) or
`ed25519.verify(signature, message, publicKey)` (@noble/curves). The public key is 32 bytes;
the manifest sends it as 43 characters of base64url.

The private key exists only on the server (`TICKET_SIGNING_KEY`). A gate device holds
public keys only, so a stolen device cannot make tickets.

### Key rotation

The server signs with one key at a time. When the key is changed the new one gets a new
number, and the old public key stays in the server's verify set (`TICKET_VERIFY_KEYS`) for
as long as tickets signed with it are in use. The manifest lists every public key the
server accepts (so every key that signed a ticket of the event), and a device simply looks
the key up by ID. A key ID is never re-used for a different key. The server refuses to
start with a verify key that is not a canonical point or is a small-order point.

## 4. How a gate device checks a ticket, offline

### 4.1 What the device holds

Before the gates open the device downloads the manifest
(`GET /v1/gate/events/{eventId}/manifest`, with `Accept-Encoding: gzip`: about 1.3 MB for a
full stadium) and stores, per event:

- `keys`: public key by key ID (32 bytes each, from 43 characters of base64url);
- `gate_groups`, and the one the steward picked for this device;
- the **ticket set**: for each ticket ID its gate group (the text, looked up through
  `gate_group_index` in the same answer; an index means nothing in a later answer) and its
  status letter, `V` or `U`;
- `version`, stored in the same write as the ticket set it came with.

Whenever it has a connection it asks again with `since=<the stored version>`. If the answer
says `full: true`, replace the whole ticket set. If `full: false`, for each tuple: `V` or
`U`, insert or overwrite; `X` or `T`, delete. Then store the new `version`. The same ticket
will come back in several answers in a row (the server looks back two minutes on purpose);
applying it again changes nothing. After applying, the size of the set must equal
`ticket_count`; if it does not, download in full. One rule on top: **never turn a ticket
this device has admitted back to `V`** because a manifest says so. The device's own scan is
newer than a manifest that was made before its upload arrived.

### 4.2 Checking a scan

For each scan, do these steps **in this order** and stop at the first that fails.

| # | Check | If it fails |
|---|---|---|
| 1 | Split the text at `.`. There are exactly three parts and the first is exactly `TLT1`. | `INVALID` (not a Talanta ticket) |
| 2 | Parts two and three contain only `A-Z a-z 0-9 - _` and decode as canonical base64url without padding (decode, re-encode, compare). | `INVALID` |
| 3 | The signature is exactly 64 bytes and the payload is not empty. | `INVALID` |
| 4 | Payload byte 0 is `0x01`. | `INVALID` (unknown version: update the app) |
| 5 | The payload is exactly 51 bytes. | `INVALID` |
| 6 | Read the key number (bytes 1 to 2, big-endian), form the key ID `k<number>`, and find it in the manifest's `keys`. | `INVALID` (unknown key) |
| 7 | Ed25519-verify the signature over `context label || payload` with that public key. | `INVALID` (forged or altered) |
| 8 | Read the fields. The event ID is not zero and is below 2^63; the gate group is well-formed (section 2). | `INVALID` |
| 9 | The event ID equals the event the device is scanning for. | `WRONG_EVENT` |
| 10 | The gate group equals the device's gate group, compared exactly as text. | `WRONG_GATE` |
| 11 | The ticket ID is in the manifest's ticket set. | `INVALID` (not a ticket of this event, or voided) |
| 12 | The ticket's status in the device's set is `V`. (Admitting a ticket sets it to `U` on the device, so this one check covers both "the manifest says used" and "this device already let it in".) | `DUPLICATE` |
|  | All passed. | `ADMITTED` |

Why this order matters:

- Nothing in the payload is believed before step 7. Steps 1 to 6 only decide whether there
  is something to verify and with which key.
- Step 9 comes before step 11 so that a genuine ticket for another match is reported as
  `WRONG_EVENT` ("you are at the wrong match") and not as a forgery: another event's
  tickets are not in this event's manifest.
- Step 10 comes before 11 and 12 so that a fan at the wrong stand is sent to the right gate
  without their ticket being marked used.

### 4.3 Recording and uploading

**Write before you show.** For every scan that got as far as step 6 (so a ticket ID could
be read from bytes 3 to 18, whether or not the signature was good), do this in one durable
local write **before** the steward sees green or red:

1. make a `client_scan_id` (a random UUID), once, for this scan;
2. store the scan: `client_scan_id`, `ticket_id` (as read from the payload), `event_id`
   and `gate_group` (what the device is set to, not what the ticket says), `scanned_at`
   (the device clock, UTC), `result`, and `token` (the full QR text);
3. if the result is `ADMITTED`, set the ticket to `U` in the device's set.

If the app dies between the write and the screen, the scan exists and the next scan of
that ticket is `DUPLICATE`. The other way round a person walks in on a scan that was never
recorded. A text that fails steps 1 to 5 has no ticket ID; it is shown as `INVALID` and is
not uploaded (keep a local count if you want one).

**Upload** with `POST /v1/gate/scans`: `device_id` (stable per install) and up to 500
scans, oldest first. Always send `token`. A scan leaves the upload queue only when a 200
answer lists its `client_scan_id`. On a network error, a 409 (`EVENT_BUSY`), a 429 (wait
`Retry-After`) or a 5xx, send the very same batch again later: the upload is idempotent on
`client_scan_id`, so a batch that did arrive is not stored twice. A 400 means the batch is
malformed and will never be accepted as it is; a 413 means split it.

**Reading the answer.** `results` has one entry per uploaded scan, in the same order.
`result` is the server's final word and `overridden: true` marks where it differs from the
device's:

- device `ADMITTED`, server `DUPLICATE`: another device admitted this ticket earlier
  (`first_admitted_at` says when). Both people are inside. Show it to the supervisor.
- device `ADMITTED`, server `INVALID`: the ticket was voided (refund, cancelled event) or
  the server does not know it. The device's manifest was stale; sync it.
- device `ADMITTED`, server `WRONG_EVENT` or `WRONG_GATE`: the device's checks have a bug.

`ticket_status` is the ticket's status on the server now; set the device's copy from it
(`VOID` and `TRANSFERRED`: delete), except that a ticket this device admitted stays `U`.

**A result can still change after a 200.** If another phone later uploads an earlier scan
of the same ticket, the server moves the admission to that scan and marks this device's as
`DUPLICATE`. Nothing is pushed to the device. To find out, resend the scans that were last
answered `ADMITTED` (the same `client_scan_id`s) once before closing the session: the
answer is their result as it stands then.

**Clocks.** `scanned_at` decides which of two admissions was first. A time in the future,
more than 7 days old or before the ticket was issued is replaced by the server's receive
time. Keep the device clock on network time.

The server runs the same steps 1 to 10 in `ticketing.Verifier.Verify`, `Claims.CheckEvent`
and `Claims.CheckGate`. Each failure there is a distinct error value (`ErrBadPrefix`,
`ErrBadEncoding`, `ErrBadLength`, `ErrUnknownVersion`, `ErrUnknownKey`, `ErrBadSignature`,
`ErrMalformedPayload`, `ErrWrongEvent`, `ErrWrongGate`).

## 5. The gate group rule

A ticket is bound to the gates of its own stand and tier (correction 8 in `CLAUDE.md`).

```
gate group = <stand code> "-" <tier>          for example  N-LOWER,  W-UPPER,  VIP-LOWER
```

- The stand code is the code of the stand the seat is in (`stands.code`): capital letters
  and digits. The tier is `LOWER` or `UPPER`.
- It is fixed when the ticket is issued, signed into the payload, stored in
  `tickets.gate_group`, and shown in the API as `Ticket.location.gate_group`.
- A device is set to one gate group and admits only tickets whose gate group is exactly
  that text. There is no "nearby gate" or prefix matching.
- The longest gate group is 16 characters (a stand code of up to 10). The server refuses to
  issue a ticket for a stand whose code is longer.

Why it exists: two phones that are both offline cannot see each other's scans, so the same
QR shown at two gates before either phone syncs is admitted twice. Binding a ticket to one
gate group shrinks that gap from "any two gates of the stadium" to "two devices at the same
stand and tier". It does not close it. After sync the server keeps the earliest admit and
marks the other `DUPLICATE`.

## 6. Drawing and scanning the QR

- The text is always 160 characters, in byte mode (it has lower-case letters, so the
  alphanumeric mode does not apply).
- Error correction level **M** gives QR **version 9**: 53 x 53 modules, 61 x 61 with the
  four-module quiet zone. Use level M in the app too, so the code on the phone and the PNG
  from the API are the same size. (Level L would be version 8, 49 x 49, with half the
  damage tolerance; level Q is version 11, 61 x 61, with smaller modules on the same
  screen.)
- Draw it black on white with the full quiet zone, at 2 pixels a module or more (the API's
  smallest PNG, 128 px, is exactly 2), and raise the screen brightness.
- A scanner should refuse texts longer than 512 characters before doing anything else.

## 7. Worked example

Use this to test a scanner's own code: build the payload from the fields, check you get the
same bytes, verify the signature with the test public key, and check that your code refuses
the three bad texts at the end.

<!-- example:begin -->
**Test key pair. TEST ONLY: this private key is public (it is test 1 of RFC 8032, section 7.1). The server refuses to start with it when `APP_ENV=production`. Never trust it on a real gate device.**

| | |
|---|---|
| key ID | `k1` |
| private seed, 32 bytes, hex | `9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60` |
| public key, 32 bytes, hex | `d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a` |
| public key, base64url (as `keys[].public_key` in the gate manifest) | `11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo` |

Payload fields:

| Field | Value | Bytes (hex) |
|---|---|---|
| version | 1 | `01` |
| key number | 1 (key ID `k1`) | `0001` |
| ticket ID | `0b0f3a52-7c1e-4d7e-9a57-0c1b4d5e6f70` | `0b0f3a527c1e4d7e9a570c1b4d5e6f70` |
| event ID | 1 | `0000000000000001` |
| gate group | `N-LOWER` (7 characters, then 9 zero bytes) | `4e2d4c4f574552000000000000000000` |
| nonce | | `a1b2c3d4e5f60718` |

Payload, 51 bytes, hex:

```
0100010b0f3a527c1e4d7e9a570c1b4d5e6f7000000000000000014e2d4c4f574552000000000000000000a1b2c3d4e5f60718
```

Payload, base64url, 68 characters:

```
AQABCw86UnweTX6aVwwbTV5vcAAAAAAAAAABTi1MT1dFUgAAAAAAAAAAAKGyw9Tl9gcY
```

Context label, 18 bytes, hex (the ASCII text `talanta.ticket.v1` and one zero byte):

```
74616c616e74612e7469636b65742e763100
```

Signed message = context label then payload, 69 bytes, hex:

```
74616c616e74612e7469636b65742e7631000100010b0f3a527c1e4d7e9a570c1b4d5e6f7000000000000000014e2d4c4f574552000000000000000000a1b2c3d4e5f60718
```

Signature, 64 bytes, hex:

```
3b9d36e1b54597a7eafab3ca8ea275e4a365b39ab7c282eeccf106df799cd684b4c3e1528821f14178e1ce8b6712fd16a5fafbf2d4b3ccc47c2a63873329380e
```

Signature, base64url, 86 characters:

```
O5024bVFl6fq-rPKjqJ15KNls5q3woLuzPEG33mc1oS0w-FSiCHxQXjhzotnEv0Wpfr78tSzzMR8KmOHMyk4Dg
```

The QR text, 160 characters:

```
TLT1.AQABCw86UnweTX6aVwwbTV5vcAAAAAAAAAABTi1MT1dFUgAAAAAAAAAAAKGyw9Tl9gcY.O5024bVFl6fq-rPKjqJ15KNls5q3woLuzPEG33mc1oS0w-FSiCHxQXjhzotnEv0Wpfr78tSzzMR8KmOHMyk4Dg
```

What a scanner holding the test public key under `k1` must answer for it:

| Device is scanning | Result |
|---|---|
| event 1, gate group `N-LOWER` | signature good, event good, gate good: admit (if the manifest says the ticket is `V`) |
| event 2, any gate group | `WRONG_EVENT` |
| event 1, gate group `S-LOWER` or `N-UPPER` | `WRONG_GATE` |

Three texts the same scanner must refuse as `INVALID`:

1. The payload says event 2, the signature is the one above (somebody edited the ticket). The signature does not match:

```
TLT1.AQABCw86UnweTX6aVwwbTV5vcAAAAAAAAAACTi1MT1dFUgAAAAAAAAAAAKGyw9Tl9gcY.O5024bVFl6fq-rPKjqJ15KNls5q3woLuzPEG33mc1oS0w-FSiCHxQXjhzotnEv0Wpfr78tSzzMR8KmOHMyk4Dg
```

2. The QR text above with its last character changed from `g` to `B` (the last character has unused bits set: not canonical base64url):

```
TLT1.AQABCw86UnweTX6aVwwbTV5vcAAAAAAAAAABTi1MT1dFUgAAAAAAAAAAAKGyw9Tl9gcY.O5024bVFl6fq-rPKjqJ15KNls5q3woLuzPEG33mc1oS0w-FSiCHxQXjhzotnEv0Wpfr78tSzzMR8KmOHMyk4DB
```

3. Key number 2 (`k2`), correctly signed with the test key, on a device that has only `k1`. The key ID is unknown:

```
TLT1.AQACCw86UnweTX6aVwwbTV5vcAAAAAAAAAABTi1MT1dFUgAAAAAAAAAAAKGyw9Tl9gcY.DE9e5kBpR9W-8ITPlN1BbpwJOryzODHkdHCOMH-WjgKkwiwlALW4mxW4noPrqnYiu8SwmBlNqujm2aQyKQ9bAQ
```
<!-- example:end -->

## 8. What this format does and does not protect

- **Forgery.** Nobody without the server's private key can make a text that passes step 7,
  or change any field of a real ticket. The device needs no secret.
- **Copying is not prevented.** The QR is static: a screenshot is as good as the original.
  The first scan wins. The defences are the gate group, the manifest's used marks, and the
  server's "earliest admit wins" after sync. A rotating code is future work
  (`docs/ARCHITECTURE.md` section 9.3).
- **Voided tickets** (refund, cancelled event) still carry a valid signature. Only the
  manifest knows they are void: a device must refresh its manifest (`since=<version>`)
  whenever it has a connection, and step 11 is not optional.
- **The web ticket link** (`/v1/t/<64 hex characters>`) is a separate secret, per booking,
  unrelated to this format and to the booking reference.

## 9. Changing the format

- A new payload layout gets a new version byte and a new context label
  (`talanta.ticket.v2`); scanners that do not know it refuse it at step 4.
- A change to the text form itself (other separators, another encoding) gets a new prefix.
- Tickets already issued keep the text they were issued with: the server stores it
  (`tickets.qr_token`) and never signs a ticket a second time.
