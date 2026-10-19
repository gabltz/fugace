// Package transport adapts network connections (TCP, TLS, WebSocket) to
// server.Conn.
package transport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/gabltz/fugace/internal/irc"
	"github.com/gabltz/fugace/internal/server"
)

// Timeouts and back-off for stream transports.
const (
	WriteTimeout     = 30 * time.Second
	acceptBackoffMin = 5 * time.Millisecond
	acceptBackoffMax = time.Second
)

// lineConn reads CRLF- (or LF-) terminated lines from a stream.
type lineConn struct {
	c net.Conn
	r *bufio.Reader
}

// NewLineConn wraps a stream connection (TCP or TLS).
func NewLineConn(c net.Conn) server.Conn {
	// +2 for the CRLF that ReadSlice keeps.
	return &lineConn{c: c, r: bufio.NewReaderSize(c, irc.MaxReadLen+2)}
}

func (l *lineConn) ReadLine() (string, error) {
	line, err := l.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		// Discard the rest of the oversized line, then report it.
		for errors.Is(err, bufio.ErrBufferFull) {
			_, err = l.r.ReadSlice('\n')
		}
		if err != nil {
			return "", fmt.Errorf("read: %w", err)
		}
		return "", server.ErrLineTooLong
	}
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return string(line), nil
}

func (l *lineConn) WriteLine(b []byte) error {
	if err := l.c.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil {
		return fmt.Errorf("deadline: %w", err)
	}
	if _, err := l.c.Write(b); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

func (l *lineConn) Close() error { return l.c.Close() }

// RemoteAddr extracts the IP of a connection; it is only hashed, never kept.
func RemoteAddr(c net.Conn) netip.Addr {
	if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
		return ap.Addr()
	}
	return netip.Addr{}
}

// ServeIRC accepts stream connections on ln until ctx is done or ln is
// closed, and hands each one to srv.
func ServeIRC(ctx context.Context, ln net.Listener, srv *server.Server) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	backoff := time.Duration(0)
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			// Typically EMFILE: back off instead of spinning.
			backoff = min(max(2*backoff, acceptBackoffMin), acceptBackoffMax)
			t := time.NewTimer(backoff)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return nil
			}
			continue
		}
		backoff = 0
		go func() {
			defer func() { _ = recover() }() // never print: values may hold user data
			srv.ServeConn(NewLineConn(c), server.ConnInfo{Addr: RemoteAddr(c), Kind: server.KindTCP})
		}()
	}
}
