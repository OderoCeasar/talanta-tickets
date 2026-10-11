// Package ticketing issues tickets and makes and checks the signed text inside their QR
// codes (docs/ARCHITECTURE.md §9, correction 8 in CLAUDE.md).
//
// The token format is specified in docs/TICKET_FORMAT.md, which the mobile gate scanner is
// built from. A test regenerates that document's layout table and worked example from this
// file, so the two cannot drift apart.
package ticketing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// The text form is "TLT1.<payload>.<signature>": both parts base64url without padding.
const (
	// TokenPrefix names the text format. A different layout of the text (not of the
	// payload) would get a new prefix.
	TokenPrefix = "TLT1"
	// PayloadVersion is the value of the payload's first byte for the layout below.
	PayloadVersion byte = 1
	// SignatureContext is prepended to the payload before signing. It makes a ticket
	// signature useless as a signature over anything else this key might ever sign, and the
	// other way round. The trailing zero byte ends the label so no longer label can share
	// this prefix.
	SignatureContext = "talanta.ticket.v1\x00"
)

// The version 1 payload: fixed size, fixed offsets, integers big-endian.
const (
	offVersion   = 0
	sizeVersion  = 1
	offKeyNumber = offVersion + sizeVersion
	sizeKeyNum   = 2
	offTicketID  = offKeyNumber + sizeKeyNum
	sizeTicketID = 16
	offEventID   = offTicketID + sizeTicketID
	sizeEventID  = 8
	offGateGroup = offEventID + sizeEventID
	sizeGate     = domain.MaxGateGroupLength
	offNonce     = offGateGroup + sizeGate
	// NonceSize is the length of tickets.token_nonce.
	NonceSize = 8

	// PayloadSize is the whole version 1 payload: 51 bytes, which is 68 base64url
	// characters with no bits left over.
	PayloadSize = offNonce + NonceSize
	// SignatureSize is an Ed25519 signature.
	SignatureSize = ed25519.SignatureSize
)

// Field is one field of the payload, for the layout table in docs/TICKET_FORMAT.md.
type Field struct {
	Name     string
	Offset   int
	Size     int
	Encoding string
}

// PayloadLayout is the version 1 payload in order. EncodePayload and DecodePayload use the
// same constants.
var PayloadLayout = []Field{
	{"version", offVersion, sizeVersion, "unsigned integer, always `0x01`"},
	{"key number", offKeyNumber, sizeKeyNum, "unsigned integer, big-endian; the key ID is the letter `k` followed by this number in decimal (`k1`)"},
	{"ticket ID", offTicketID, sizeTicketID, "the UUID's 16 bytes in the order its text form is written (RFC 9562)"},
	{"event ID", offEventID, sizeEventID, "unsigned integer, big-endian; never zero and below 2^63"},
	{"gate group", offGateGroup, sizeGate, "ASCII text such as `N-LOWER`, padded on the right with `0x00` bytes"},
	{"nonce", offNonce, NonceSize, "random bytes, opaque"},
}

// Why a token was refused. Each is a distinct value so a caller (the scan upload, a test,
// the gate app's own port of this code) can tell them apart with errors.Is. At the gate the
// first seven all mean INVALID; ErrWrongEvent means WRONG_EVENT and ErrWrongGate WRONG_GATE.
var (
	ErrBadPrefix        = errors.New("ticket token: not a TLT1 token")
	ErrBadEncoding      = errors.New("ticket token: a part is not canonical base64url without padding")
	ErrBadLength        = errors.New("ticket token: wrong payload or signature length")
	ErrUnknownVersion   = errors.New("ticket token: unknown payload version")
	ErrMalformedPayload = errors.New("ticket token: a payload field holds a value that cannot be issued")
	ErrUnknownKey       = errors.New("ticket token: signed with a key ID this verifier does not have")
	ErrBadSignature     = errors.New("ticket token: signature does not match")
	ErrWrongEvent       = errors.New("ticket token: ticket is for another event (WRONG_EVENT)")
	ErrWrongGate        = errors.New("ticket token: ticket is for another gate group (WRONG_GATE)")
)

// Claims is what a ticket token says.
type Claims struct {
	// KeyID names the key that signed the token: "k1", "k2", …
	KeyID string
	// TicketID is tickets.id.
	TicketID [16]byte
	EventID  int64
	// GateGroup is "<stand code>-<tier>", for example "N-LOWER".
	GateGroup string
	Nonce     [NonceSize]byte
}

// TicketUUID is the ticket ID in its text form.
func (c Claims) TicketUUID() string { return FormatUUID(c.TicketID) }

// CheckEvent returns ErrWrongEvent unless the ticket is for the given event. A gate device
// calls it after Verify with the event it is scanning for.
func (c Claims) CheckEvent(eventID int64) error {
	if c.EventID != eventID {
		return ErrWrongEvent
	}
	return nil
}

// CheckGate returns ErrWrongGate unless the ticket is for the given gate group (correction
// 8: a ticket enters only by the gates of its own stand and tier). The comparison is exact
// and case-sensitive.
func (c Claims) CheckGate(gateGroup string) error {
	if c.GateGroup != gateGroup {
		return ErrWrongGate
	}
	return nil
}

// maxKeyNumber is the largest key number the two payload bytes hold.
const maxKeyNumber = math.MaxUint16

// EphemeralKeyID is the key ID of a key generated at startup in development. It is reserved:
// a configured key may not use it, so a real verifier never trusts it.
const EphemeralKeyID = "k0"

// KeyID is the text form of a key number.
func KeyID(number uint16) string { return "k" + strconv.FormatUint(uint64(number), 10) }

// ParseKeyID reads "k<number>" with the number from 0 to 65535 and no leading zeros.
func ParseKeyID(id string) (uint16, error) {
	digits, ok := strings.CutPrefix(id, "k")
	if !ok || digits == "" || len(digits) > 5 || (len(digits) > 1 && digits[0] == '0') {
		return 0, fmt.Errorf("key ID %q is not the letter k followed by a number from 1 to %d", id, maxKeyNumber)
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("key ID %q is not the letter k followed by a number from 1 to %d", id, maxKeyNumber)
		}
	}
	n, err := strconv.ParseUint(digits, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("key ID %q is not the letter k followed by a number from 1 to %d", id, maxKeyNumber)
	}
	return uint16(n), nil
}

// validGateGroup reports whether s can go in the payload's gate group field: the same rule
// as the tickets.gate_group check and the contract's GateGroup pattern, and short enough.
func validGateGroup(s string) bool {
	if len(s) == 0 || len(s) > sizeGate {
		return false
	}
	code, tier, ok := strings.Cut(s, "-")
	if !ok || code == "" || (tier != "LOWER" && tier != "UPPER") {
		return false
	}
	for _, c := range code {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// EncodePayload lays the claims out as the version 1 payload.
func EncodePayload(c Claims) ([]byte, error) {
	number, err := ParseKeyID(c.KeyID)
	if err != nil {
		return nil, err
	}
	if c.EventID < 1 {
		return nil, fmt.Errorf("event ID %d cannot go in a ticket", c.EventID)
	}
	if !validGateGroup(c.GateGroup) {
		return nil, fmt.Errorf("gate group %q cannot go in a ticket: it must be <stand code>-LOWER or -UPPER and at most %d characters", c.GateGroup, sizeGate)
	}
	p := make([]byte, PayloadSize)
	p[offVersion] = PayloadVersion
	binary.BigEndian.PutUint16(p[offKeyNumber:], number)
	copy(p[offTicketID:], c.TicketID[:])
	binary.BigEndian.PutUint64(p[offEventID:], uint64(c.EventID))
	// The rest of the field is already zero.
	copy(p[offGateGroup:offGateGroup+sizeGate], c.GateGroup)
	copy(p[offNonce:], c.Nonce[:])
	return p, nil
}

// DecodePayload reads a payload. It checks the shape only, not who signed it: use
// Verifier.Verify for anything that came from outside.
func DecodePayload(p []byte) (Claims, error) {
	if len(p) == 0 {
		return Claims{}, ErrBadLength
	}
	if p[offVersion] != PayloadVersion {
		return Claims{}, ErrUnknownVersion
	}
	if len(p) != PayloadSize {
		return Claims{}, ErrBadLength
	}
	var c Claims
	c.KeyID = KeyID(binary.BigEndian.Uint16(p[offKeyNumber:]))
	copy(c.TicketID[:], p[offTicketID:])
	event := binary.BigEndian.Uint64(p[offEventID:])
	if event == 0 || event > math.MaxInt64 {
		return Claims{}, ErrMalformedPayload
	}
	c.EventID = int64(event)

	gate := p[offGateGroup : offGateGroup+sizeGate]
	end := len(gate)
	for end > 0 && gate[end-1] == 0 {
		end--
	}
	c.GateGroup = string(gate[:end])
	// Also refuses a zero byte before the end of the text: the padding is on the right only.
	if !validGateGroup(c.GateGroup) {
		return Claims{}, ErrMalformedPayload
	}
	copy(c.Nonce[:], p[offNonce:])
	return c, nil
}

// SigningMessage is the exact byte string a ticket signature is made over: the context
// label, then the payload.
func SigningMessage(payload []byte) []byte {
	msg := make([]byte, 0, len(SignatureContext)+len(payload))
	msg = append(msg, SignatureContext...)
	return append(msg, payload...)
}

// tokenEncoding is base64url without padding, strict: the unused low bits of a last
// character must be zero. A signature is 64 bytes, so its 86th character carries only two
// bits of it; without strict decoding sixteen different characters there would all read as
// the same signature and one ticket would have many valid texts.
var tokenEncoding = base64.RawURLEncoding.Strict()

// EncodeToken joins a payload and its signature into the text that goes in the QR code.
func EncodeToken(payload, signature []byte) string {
	return TokenPrefix + "." + tokenEncoding.EncodeToString(payload) + "." + tokenEncoding.EncodeToString(signature)
}

// splitToken undoes EncodeToken as far as the two byte strings; it checks prefix, encoding
// and the signature's length.
func splitToken(token string) (payload, signature []byte, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != TokenPrefix {
		return nil, nil, ErrBadPrefix
	}
	if payload, err = decodePart(parts[1]); err != nil {
		return nil, nil, err
	}
	if signature, err = decodePart(parts[2]); err != nil {
		return nil, nil, err
	}
	if len(signature) != SignatureSize {
		return nil, nil, ErrBadLength
	}
	return payload, signature, nil
}

// decodePart decodes one base64url part. The alphabet is checked here first because Go's
// decoder, even in strict mode, skips carriage returns and line feeds: without this a token
// with a line break pasted into it would still verify, and one ticket would again have
// more than one valid text.
func decodePart(part string) ([]byte, error) {
	for i := 0; i < len(part); i++ {
		c := part[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return nil, ErrBadEncoding
		}
	}
	b, err := tokenEncoding.DecodeString(part)
	if err != nil {
		return nil, ErrBadEncoding
	}
	return b, nil
}

// FormatUUID writes 16 bytes as a lower-case UUID.
func FormatUUID(id [16]byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, b := range id {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	return string(out)
}

// ParseUUID reads the 8-4-4-4-12 text form of a UUID, in either case.
func ParseUUID(s string) ([16]byte, error) {
	var id [16]byte
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return id, fmt.Errorf("%q is not a UUID", s)
	}
	n := 0
	for i := 0; i < len(s); {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			i++
			continue
		}
		hi, ok1 := unhex(s[i])
		lo, ok2 := unhex(s[i+1])
		if !ok1 || !ok2 {
			return [16]byte{}, fmt.Errorf("%q is not a UUID", s)
		}
		id[n] = hi<<4 | lo
		n++
		i += 2
	}
	return id, nil
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
