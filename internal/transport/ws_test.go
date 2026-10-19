package transport

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/gabltz/fugace/internal/limits"
	"github.com/gabltz/fugace/internal/server"
	"github.com/gabltz/fugace/internal/web"
)

// stack runs the chat server behind both the TCP and the WebSocket transports.
func stack(t *testing.T) (wsURL, tcpAddr string) {
	t.Helper()
	srv, err := server.New(server.Options{Channels: []string{"#general"}})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewUnstartedServer(nil)
	hs.Config = web.NewServer(web.NewHandler(web.Options{WS: WSHandler(srv)}))
	hs.Start()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = ServeIRC(ctx, ln, srv) }()
	t.Cleanup(func() {
		cancel()
		hs.Close()
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = srv.Shutdown(sctx)
	})
	return "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws", ln.Addr().String()
}

type wsClient struct {
	t *testing.T
	c *websocket.Conn
}

// dialWS returns the client, the handshake HTTP status, and the error.
func dialWS(t *testing.T, url string, hdr http.Header, protos ...string) (*wsClient, int, error) {
	t.Helper()
	if protos == nil {
		protos = []string{Subprotocol}
	}
	c, resp, err := websocket.Dial(t.Context(), url, &websocket.DialOptions{Subprotocols: protos, HTTPHeader: hdr})
	code := 0
	if resp != nil {
		code = resp.StatusCode
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	if err != nil {
		return nil, code, err
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return &wsClient{t: t, c: c}, code, nil
}

func (w *wsClient) send(line string) {
	w.t.Helper()
	if err := w.c.Write(w.t.Context(), websocket.MessageText, []byte(line)); err != nil {
		w.t.Fatalf("ws write: %v", err)
	}
}

func (w *wsClient) expect(want string) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.t.Context(), 3*time.Second)
	defer cancel()
	for {
		_, b, err := w.c.Read(ctx)
		if err != nil {
			w.t.Fatalf("waiting for %q: %v", want, err)
		}
		line := string(b)
		if strings.HasSuffix(line, "\r\n") {
			w.t.Fatalf("ws message carries CRLF: %q", line)
		}
		if strings.Contains(line, want) {
			return
		}
	}
}

// Web and TCP users share the same channel (ROADMAP M2).
func TestWebAndTCPInSameChannel(t *testing.T) {
	wsURL, tcpAddr := stack(t)
	w, _, err := dialWS(t, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.send("NICK webby")
	w.send("USER web 0 * :web")
	w.expect(" 001 webby ")
	w.send("JOIN #general")
	w.expect(" 366 webby #general ")

	var d net.Dialer
	tc, err := d.DialContext(t.Context(), "tcp", tcpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tc.Close() }()
	r := bufio.NewReader(tc)
	_, _ = tc.Write([]byte("NICK tcpy\r\nUSER t 0 * :t\r\nJOIN #general\r\n"))
	readUntil := func(want string) string {
		_ = tc.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("tcp waiting for %q: %v", want, err)
			}
			if strings.Contains(l, want) {
				return l
			}
		}
	}
	readUntil(" 366 tcpy #general ")
	w.expect(":tcpy!~user@fugace JOIN #general")

	w.send("PRIVMSG #general :hello from the web")
	if l := readUntil("hello from the web"); !strings.HasPrefix(l, ":webby!web@fugace PRIVMSG #general :") {
		t.Fatalf("web hostmask = %q", l)
	}
	_, _ = tc.Write([]byte("PRIVMSG #general :hello from tcp\r\n"))
	w.expect(":tcpy!~user@fugace PRIVMSG #general :hello from tcp")
}

// T14: a cross-site page must not open a WebSocket on our origin.
func TestWSOrigin(t *testing.T) {
	wsURL, _ := stack(t)
	tests := []struct {
		origin string
		ok     bool
	}{
		{"", true}, // non-browser client
		{"http://" + strings.TrimPrefix(strings.TrimSuffix(wsURL, "/ws"), "ws://"), true},
		{"https://evil.example", false},
		{"null", false},
		{"http://127.0.0.1.evil.example", false},
	}
	for _, tt := range tests {
		hdr := http.Header{}
		if tt.origin != "" {
			hdr.Set("Origin", tt.origin)
		}
		_, code, err := dialWS(t, wsURL, hdr)
		if (err == nil) != tt.ok {
			t.Errorf("origin %q: err=%v", tt.origin, err)
		}
		if !tt.ok && code != http.StatusForbidden {
			t.Errorf("origin %q: want 403, got %d", tt.origin, code)
		}
	}
}

func TestWSRejections(t *testing.T) {
	wsURL, _ := stack(t)
	readErr := func(w *wsClient) websocket.StatusCode {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		for {
			if _, _, err := w.c.Read(ctx); err != nil {
				return websocket.CloseStatus(err)
			}
		}
	}
	t.Run("missing subprotocol", func(t *testing.T) {
		w, _, err := dialWS(t, wsURL, nil, "other")
		if err != nil {
			t.Fatal(err)
		}
		if code := readErr(w); code != websocket.StatusPolicyViolation {
			t.Fatalf("close = %v", code)
		}
	})
	t.Run("binary message", func(t *testing.T) {
		w, _, err := dialWS(t, wsURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = w.c.Write(t.Context(), websocket.MessageBinary, []byte("NICK x"))
		if code := readErr(w); code != websocket.StatusUnsupportedData {
			t.Fatalf("close = %v", code)
		}
	})
	t.Run("oversized message", func(t *testing.T) {
		w, _, err := dialWS(t, wsURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = w.c.Write(t.Context(), websocket.MessageText, []byte(strings.Repeat("x", limits.MaxWSMessage+1)))
		if code := readErr(w); code != websocket.StatusMessageTooBig {
			t.Fatalf("close = %v", code)
		}
	})
}

// The HTTP server deadlines must not kill a WebSocket after ReadTimeout.
func TestWSOutlivesHTTPTimeouts(t *testing.T) {
	srv, err := server.New(server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewUnstartedServer(nil)
	hs.Config = web.NewServer(web.NewHandler(web.Options{WS: WSHandler(srv)}))
	hs.Config.ReadTimeout = 200 * time.Millisecond
	hs.Config.WriteTimeout = 200 * time.Millisecond
	hs.Start()
	defer hs.Close()
	w, _, err := dialWS(t, "ws"+strings.TrimPrefix(hs.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	w.send("PING :alive")
	w.expect("PONG fugace :alive")
}
