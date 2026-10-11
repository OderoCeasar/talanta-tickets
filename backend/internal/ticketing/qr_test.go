package ticketing

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/makiuchi-d/gozxing"
	zxingqr "github.com/makiuchi-d/gozxing/qrcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DecodeQRForTest reads a QR code out of a PNG with a decoder that shares no code with the
// library that drew it.
func decodeQR(t testing.TB, pngBytes []byte) string {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(pngBytes))
	require.NoError(t, err)
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	require.NoError(t, err)
	result, err := zxingqr.NewQRCodeReader().Decode(bitmap, nil)
	require.NoError(t, err)
	return result.GetText()
}

func TestQRPNGDecodesBackToTheToken(t *testing.T) {
	token, err := testSigner(t).Sign(exampleClaims())
	require.NoError(t, err)

	for _, size := range []int{MinQRSize, 200, DefaultQRSize, MaxQRSize} {
		pngBytes, err := QRPNG(token, size)
		require.NoError(t, err)
		img, err := png.Decode(bytes.NewReader(pngBytes))
		require.NoError(t, err)
		assert.Equal(t, size, img.Bounds().Dx())
		assert.Equal(t, size, img.Bounds().Dy())
		assert.Equal(t, token, decodeQR(t, pngBytes), "size %d", size)

		// Quiet zone: the outermost ring of pixels is white.
		r, g, b, _ := img.At(0, 0).RGBA()
		assert.Equal(t, [3]uint32{0xffff, 0xffff, 0xffff}, [3]uint32{r, g, b})
		t.Logf("size %d px: PNG is %d bytes", size, len(pngBytes))
	}

	version, modules, err := QRSize(token)
	require.NoError(t, err)
	assert.Equal(t, 9, version, "a 160-character token at level M")
	assert.Equal(t, 53, modules)
	t.Logf("token: %d characters, QR version %d, %d x %d modules (%d with the quiet zone)", len(token), version, modules, modules, modules+8)

	// At the smallest size a module is still at least two pixels.
	assert.GreaterOrEqual(t, MinQRSize/(modules+8), 2)
}

func TestQRPNGRefusesSizesOutsideTheContract(t *testing.T) {
	for _, size := range []int{0, -1, MinQRSize - 1, MaxQRSize + 1} {
		_, err := QRPNG("TLT1.a.b", size)
		assert.Error(t, err, "size %d", size)
	}
}
