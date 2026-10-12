package ticketing

import (
	"crypto/ed25519"
	"errors"
	"math/big"
)

// Why a public key is refused before it is trusted. Match with errors.Is.
var (
	// ErrPublicKeyNotCanonical: the 32 bytes are not the one canonical encoding of a point
	// of the curve (y is not below the field prime, the point is not on the curve, or it is
	// "x = 0 with the sign bit set").
	ErrPublicKeyNotCanonical = errors.New("public key is not a canonical Ed25519 point")
	// ErrPublicKeySmallOrder: the point is one of the eight whose order divides 8.
	ErrPublicKeySmallOrder = errors.New("public key is a small-order Ed25519 point")
)

var (
	fieldP = func() *big.Int {
		p := new(big.Int).Lsh(big.NewInt(1), 255)
		return p.Sub(p, big.NewInt(19))
	}()
	// curveD is -121665/121666 mod p.
	curveD = func() *big.Int {
		d := new(big.Int).ModInverse(big.NewInt(121666), fieldP)
		d.Mul(d, big.NewInt(-121665))
		return d.Mod(d, fieldP)
	}()
	// sqrtMinusOne is 2^((p-1)/4) mod p.
	sqrtMinusOne = func() *big.Int {
		e := new(big.Int).Sub(fieldP, big.NewInt(1))
		e.Rsh(e, 2)
		return new(big.Int).Exp(big.NewInt(2), e, fieldP)
	}()
)

// CheckPublicKey refuses a public key that must never be trusted to verify tickets.
//
// Why: Ed25519 verification accepts a signature (R, S) when [S]B = R + [k]A. For a public
// key A of small order (the identity is the plainest case) [k]A takes at most eight values
// whatever the message, so a FIXED signature verifies any payload at all under it, for one
// message in eight at worst and for every message under the identity. Such a key in
// TICKET_VERIFY_KEYS would let anyone mint tickets. A key derived from a real seed is never
// one of these; an entry that is can only be a mistake or an attack on the configuration.
// Non-canonical encodings are refused as well: they are other spellings of the same points.
//
// It runs once per key at startup, so it is written with math/big for clarity, not speed,
// and adds no dependency.
func CheckPublicKey(key ed25519.PublicKey) error {
	if len(key) != ed25519.PublicKeySize {
		return ErrPublicKeyNotCanonical
	}
	x, y, err := decompress(key)
	if err != nil {
		return err
	}
	// [8]A by three doublings. The identity is (0, 1).
	for i := 0; i < 3; i++ {
		x, y = addPoints(x, y, x, y)
	}
	if x.Sign() == 0 && y.Cmp(big.NewInt(1)) == 0 {
		return ErrPublicKeySmallOrder
	}
	return nil
}

// decompress reads the RFC 8032 encoding: y little-endian in 255 bits, the low bit of x in
// the top bit.
func decompress(key []byte) (x, y *big.Int, err error) {
	be := make([]byte, len(key))
	for i, b := range key {
		be[len(key)-1-i] = b
	}
	sign := uint(be[0] >> 7)
	be[0] &= 0x7f
	y = new(big.Int).SetBytes(be)
	if y.Cmp(fieldP) >= 0 {
		return nil, nil, ErrPublicKeyNotCanonical
	}
	// x^2 = (y^2 - 1) / (d*y^2 + 1)
	yy := new(big.Int).Mul(y, y)
	num := new(big.Int).Sub(yy, big.NewInt(1))
	den := new(big.Int).Mul(curveD, yy)
	den.Add(den, big.NewInt(1)).Mod(den, fieldP)
	den.ModInverse(den, fieldP)
	xx := num.Mul(num, den)
	xx.Mod(xx, fieldP)

	// A square root in a field with p = 5 mod 8.
	exp := new(big.Int).Add(fieldP, big.NewInt(3))
	exp.Rsh(exp, 3)
	x = new(big.Int).Exp(xx, exp, fieldP)
	if check := new(big.Int).Mul(x, x); check.Mod(check, fieldP).Cmp(xx) != 0 {
		x.Mul(x, sqrtMinusOne).Mod(x, fieldP)
		if check = new(big.Int).Mul(x, x); check.Mod(check, fieldP).Cmp(xx) != 0 {
			return nil, nil, ErrPublicKeyNotCanonical // not on the curve
		}
	}
	if x.Sign() == 0 && sign == 1 {
		return nil, nil, ErrPublicKeyNotCanonical
	}
	if x.Bit(0) != sign {
		x.Sub(fieldP, x)
	}
	return x, y, nil
}

// addPoints adds two points of -x^2 + y^2 = 1 + d*x^2*y^2 in affine coordinates. The
// formula is complete on this curve: it also doubles and handles the identity.
func addPoints(x1, y1, x2, y2 *big.Int) (x3, y3 *big.Int) {
	x1y2 := new(big.Int).Mul(x1, y2)
	x2y1 := new(big.Int).Mul(x2, y1)
	y1y2 := new(big.Int).Mul(y1, y2)
	x1x2 := new(big.Int).Mul(x1, x2)
	t := new(big.Int).Mul(x1x2, y1y2)
	t.Mul(t, curveD).Mod(t, fieldP)

	one := big.NewInt(1)
	denX := new(big.Int).Add(one, t)
	denX.Mod(denX, fieldP).ModInverse(denX, fieldP)
	denY := new(big.Int).Sub(one, t)
	denY.Mod(denY, fieldP).ModInverse(denY, fieldP)

	x3 = x1y2.Add(x1y2, x2y1)
	x3.Mul(x3, denX).Mod(x3, fieldP)
	y3 = y1y2.Add(y1y2, x1x2)
	y3.Mul(y3, denY).Mod(y3, fieldP)
	return x3, y3
}
