package ticketing

import (
	"fmt"

	qrcode "github.com/skip2/go-qrcode"
)

// QR image sizes the API serves, in pixels (the contract's `size` parameter).
const (
	MinQRSize     = 128
	MaxQRSize     = 1024
	DefaultQRSize = 512
)

// qrLevel is the error correction every ticket QR is drawn with: M, about 15% of the code
// may be unreadable. A 160-character token fits version 9 (53 x 53 modules) at this level.
// Level Q would need version 11 (61 x 61): smaller modules on the same screen, which costs
// more at a gate than the extra correction buys on a cracked one.
const qrLevel = qrcode.Medium

// QRPNG draws a ticket token as a black-on-white PNG of size x size pixels, with the
// standard quiet zone of four modules on every side. Modules are a whole number of pixels,
// so the code is centred with a little extra white when size is not a multiple.
func QRPNG(token string, size int) ([]byte, error) {
	if size < MinQRSize || size > MaxQRSize {
		return nil, fmt.Errorf("qr size %d is outside %d to %d", size, MinQRSize, MaxQRSize)
	}
	q, err := qrcode.New(token, qrLevel)
	if err != nil {
		return nil, fmt.Errorf("encode qr: %w", err)
	}
	png, err := q.PNG(size)
	if err != nil {
		return nil, fmt.Errorf("draw qr: %w", err)
	}
	return png, nil
}

// QRSize reports the QR version a token needs at the level QRPNG uses and how many modules
// wide that code is without its quiet zone.
func QRSize(token string) (version, modules int, err error) {
	q, err := qrcode.New(token, qrLevel)
	if err != nil {
		return 0, 0, fmt.Errorf("encode qr: %w", err)
	}
	return q.VersionNumber, 17 + 4*q.VersionNumber, nil
}
