package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/gabltz/fugace/internal/limits"
	"github.com/gabltz/fugace/internal/server"
)

// Subprotocol is the IRCv3 WebSocket text subprotocol.
const Subprotocol = "text.ircv3.net"

// errBinary reports a binary message, which text.ircv3.net forbids.
var errBinary = errors.New("ws: binary message")

// wsConn carries one IRC line per WebSocket text message.
type wsConn struct {
	c      *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
}

func (w *wsConn) ReadLine() (string, error) {
	typ, b, err := w.c.Read(w.ctx)
	if err != nil {
		return "", fmt.Errorf("ws read: %w", err)
	}
	if typ != websocket.MessageText {
		_ = w.c.Close(websocket.StatusUnsupportedData, "text messages only")
		return "", errBinary
	}
	return string(b), nil
}

func (w *wsConn) WriteLine(b []byte) error {
	ctx, cancel := context.WithTimeout(w.ctx, WriteTimeout)
	defer cancel()
	line := strings.TrimSuffix(string(b), "\r\n")
	if err := w.c.Write(ctx, websocket.MessageText, []byte(line)); err != nil {
		return fmt.Errorf("ws write: %w", err)
	}
	return nil
}

func (w *wsConn) Close() error {
	w.cancel()
	return w.c.CloseNow()
}

// WSHandler serves IRC over WebSocket. Only same-origin browsers (or
// clients sending no Origin) are accepted.
func WSHandler(srv *server.Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The http.Server deadlines would survive the hijack and kill the
		// connection after ReadTimeout: idle is handled by IRC PING instead.
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Time{})  // unsupported writer: Accept fails below
		_ = rc.SetWriteDeadline(time.Time{}) // same
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}})
		if err != nil {
			return // Accept already answered (403 on a foreign Origin)
		}
		if c.Subprotocol() != Subprotocol {
			_ = c.Close(websocket.StatusPolicyViolation, "subprotocol "+Subprotocol+" required")
			return
		}
		c.SetReadLimit(limits.MaxWSMessage)
		ctx, cancel := context.WithCancel(context.Background())
		srv.ServeConn(&wsConn{c: c, ctx: ctx, cancel: cancel}, server.ConnInfo{Addr: requestAddr(r), Kind: server.KindWeb})
	})
}

// requestAddr returns the peer IP of r; it is only hashed, never kept.
func requestAddr(r *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr()
	}
	return netip.Addr{}
}
