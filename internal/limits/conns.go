package limits

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/netip"
	"sync"
)

// ipv6SourceBits groups IPv6 addresses by /64: a single host usually owns a
// whole /64, so per-address counting would be trivial to bypass.
const ipv6SourceBits = 64

// sourceIDLen is the truncated HMAC length kept in memory.
const sourceIDLen = 16

// SourceID is an opaque, keyed hash of a source address. The address itself
// is never stored.
type SourceID [sourceIDLen]byte

// Conns counts live connections, globally and per hashed source.
type Conns struct {
	key       []byte
	max       int
	perSource int

	mu    sync.Mutex
	total int
	by    map[SourceID]int
}

// NewConns returns a counter with a fresh random HMAC key that only ever
// lives in memory.
func NewConns(maxTotal, maxPerSource int) (*Conns, error) {
	key := make([]byte, sha256.Size)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("limits: random key: %w", err)
	}
	return &Conns{key: key, max: maxTotal, perSource: maxPerSource, by: make(map[SourceID]int)}, nil
}

// Source hashes addr with the boot key.
func (c *Conns) Source(addr netip.Addr) SourceID {
	addr = addr.Unmap().WithZone("")
	if addr.Is6() {
		if p, err := addr.Prefix(ipv6SourceBits); err == nil {
			addr = p.Addr()
		}
	}
	mac := hmac.New(sha256.New, c.key)
	b, _ := addr.MarshalBinary() // cannot fail for a valid or zero Addr
	mac.Write(b)
	var id SourceID
	copy(id[:], mac.Sum(nil))
	return id
}

// Acquire reserves a slot for addr. On success the returned release func
// must be called exactly once when the connection ends.
func (c *Conns) Acquire(addr netip.Addr) (release func(), ok bool) {
	id := c.Source(addr)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total >= c.max || c.by[id] >= c.perSource {
		return nil, false
	}
	c.total++
	c.by[id]++
	var once sync.Once
	return func() { once.Do(func() { c.release(id) }) }, true
}

func (c *Conns) release(id SourceID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total--
	if c.by[id]--; c.by[id] <= 0 {
		delete(c.by, id) // nothing about a source outlives its connections
	}
}

// Count returns the number of live connections.
func (c *Conns) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}
