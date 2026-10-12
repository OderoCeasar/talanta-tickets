package ticketing

import (
	"container/list"
	"sync"
)

// DefaultQRCacheEntries is how many drawn QR images the API keeps. A 512-pixel PNG is about
// 1.4 KB and a 1024-pixel one a few KB, so the whole cache is a few megabytes at most.
const DefaultQRCacheEntries = 1024

// QRCache keeps drawn QR PNGs by (token, size).
//
// Why: drawing is the expensive part of serving a ticket image (about 14 ms at 384 pixels
// and 54 ms at 1024; a ten-ticket web page draws ten), and it is pure: a ticket's token is
// signed once and never changes, so the PNG for a (token, size) pair is the same for ever.
// The cache is small, bounded and per process; the least recently used image is dropped
// when it is full. It holds nothing a reader of the process's memory does not already have
// in the database.
type QRCache struct {
	mu      sync.Mutex
	max     int
	order   *list.List // front = most recently used
	entries map[qrKey]*list.Element
	draws   int
}

type qrKey struct {
	token string
	size  int
}

type qrEntry struct {
	key qrKey
	png []byte
}

// NewQRCache returns a cache of at most max images; max <= 0 means DefaultQRCacheEntries.
func NewQRCache(max int) *QRCache {
	if max <= 0 {
		max = DefaultQRCacheEntries
	}
	return &QRCache{max: max, order: list.New(), entries: make(map[qrKey]*list.Element, max)}
}

// PNG is QRPNG through the cache. The returned bytes are shared: do not modify them.
func (c *QRCache) PNG(token string, size int) ([]byte, error) {
	key := qrKey{token: token, size: size}
	c.mu.Lock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		png := el.Value.(*qrEntry).png
		c.mu.Unlock()
		return png, nil
	}
	c.mu.Unlock()

	// Drawn outside the lock: two requests for the same new image may both draw it, which
	// costs a few milliseconds once and keeps every other reader moving.
	png, err := QRPNG(token, size)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.draws++
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*qrEntry).png, nil
	}
	c.entries[key] = c.order.PushFront(&qrEntry{key: key, png: png})
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*qrEntry).key)
	}
	return png, nil
}

// Len is how many images are cached. Draws is how many times an image was drawn, for tests
// and metrics.
func (c *QRCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

func (c *QRCache) Draws() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.draws
}
