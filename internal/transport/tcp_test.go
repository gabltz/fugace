package transport

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/gabltz/fugace/internal/irc"
	"github.com/gabltz/fugace/internal/server"
)

func TestLineConnReadLine(t *testing.T) {
	client, srv := net.Pipe()
	defer func() { _ = client.Close() }()
	lc := NewLineConn(srv)
	go func() {
		_, _ = io.WriteString(client, "NICK a\r\n")
		_, _ = io.WriteString(client, strings.Repeat("x", irc.MaxReadLen+100)+"\r\n")
		_, _ = io.WriteString(client, "USER a 0 * :a\n")
		_ = client.Close()
	}()
	tests := []struct {
		line string
		err  error
	}{
		{"NICK a\r\n", nil},
		{"", server.ErrLineTooLong},
		{"USER a 0 * :a\n", nil},
	}
	for i, tt := range tests {
		line, err := lc.ReadLine()
		if line != tt.line || !errors.Is(err, tt.err) {
			t.Fatalf("read %d = %q, %v; want %q, %v", i, line, err, tt.line, tt.err)
		}
	}
	if _, err := lc.ReadLine(); err == nil {
		t.Fatal("expected EOF")
	}
}

func TestLineConnWrite(t *testing.T) {
	client, srv := net.Pipe()
	lc := NewLineConn(srv)
	go func() { _ = lc.WriteLine([]byte("PING x\r\n")); _ = lc.Close() }()
	b, _ := io.ReadAll(client)
	if string(b) != "PING x\r\n" {
		t.Fatalf("got %q", b)
	}
	if err := lc.WriteLine([]byte("x")); err == nil {
		t.Fatal("write on closed conn succeeded")
	}
}

func TestRemoteAddr(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var d net.Dialer
	c, err := d.DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if got := RemoteAddr(c); got.String() != "127.0.0.1" {
		t.Fatalf("RemoteAddr = %v", got)
	}
	p1, p2 := net.Pipe()
	defer func() { _ = p1.Close(); _ = p2.Close() }()
	if RemoteAddr(p1).IsValid() {
		t.Fatal("pipe has no IP")
	}
}

func TestServeIRC(t *testing.T) {
	srv, err := server.New(server.Options{Channels: []string{"#general"}})
	if err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- ServeIRC(ctx, ln, srv) }()

	var d net.Dialer
	c, err := d.DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, _ = io.WriteString(c, "NICK t\r\nUSER t 0 * :t\r\n")
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.Contains(line, " 001 t ") {
		t.Fatalf("welcome = %q, %v", line, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("ServeIRC: %v", err)
	}
	if _, err := d.DialContext(t.Context(), "tcp", ln.Addr().String()); err == nil {
		t.Fatal("listener still open after cancel")
	}
}
