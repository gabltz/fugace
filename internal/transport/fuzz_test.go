package transport

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// FuzzClientAddr: never panics, and the result is either the peer or an
// address taken from the header (never something invented).
func FuzzClientAddr(f *testing.F) {
	f.Add("10.1.2.3:443", "198.51.100.9, 192.0.2.1")
	f.Add("[::1]:1", "::ffff:1.2.3.4,,  ")
	f.Add("203.0.113.5:80", "garbage")
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	f.Fuzz(func(t *testing.T, remote, xff string) {
		req := httptest.NewRequest(http.MethodGet, "/ws", nil) //nolint:noctx // fuzz body, no request is sent
		req.RemoteAddr = remote
		req.Header.Set("X-Forwarded-For", xff)
		got := ClientAddr(req, trusted)
		if !got.IsValid() {
			return
		}
		peer, err := netip.ParseAddrPort(remote)
		if err != nil {
			t.Fatalf("valid result %v from an invalid peer", got)
		}
		if !isTrusted(peer.Addr().Unmap(), trusted) && got != peer.Addr().Unmap() {
			t.Fatalf("untrusted peer %v, header believed: %v", peer, got)
		}
	})
}
