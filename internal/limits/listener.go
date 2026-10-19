package limits

import (
	"net"
	"net/netip"
	"sync"
)

// HTTP connection limits (slowloris, T7): browsers open a handful of
// connections per page, the WebSocket being counted separately by the chat
// server.
const (
	MaxHTTPConns          = 1024
	MaxHTTPConnsPerSource = 32
)

// Listener refuses connections beyond the limits of conns by closing them
// at once. Sources for which exempt returns true (trusted reverse proxies)
// only count towards the global limit.
type Listener struct {
	net.Listener
	conns  *Conns
	global *Conns
	exempt func(netip.Addr) bool
}

// NewListener wraps ln. exempt may be nil.
func NewListener(ln net.Listener, maxTotal, maxPerSource int, exempt func(netip.Addr) bool) (*Listener, error) {
	perSource, err := NewConns(maxTotal, maxPerSource)
	if err != nil {
		return nil, err
	}
	global, err := NewConns(maxTotal, maxTotal)
	if err != nil {
		return nil, err
	}
	return &Listener{Listener: ln, conns: perSource, global: global, exempt: exempt}, nil
}

// Accept returns the next connection within the limits.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		addr := netip.Addr{}
		if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
			addr = ap.Addr().Unmap()
		}
		var release func()
		var ok bool
		if l.exempt != nil && l.exempt(addr) {
			release, ok = l.global.Acquire(netip.Addr{}) // one shared bucket: global cap only
		} else {
			release, ok = l.conns.Acquire(addr)
		}
		if !ok {
			_ = c.Close()
			continue
		}
		return &limitedConn{Conn: c, release: release}, nil
	}
}

// Count returns the number of open connections.
func (l *Listener) Count() int { return l.conns.Count() + l.global.Count() }

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
