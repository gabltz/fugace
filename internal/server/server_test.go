package server_test

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabltz/fugace/internal/server"
	"github.com/gabltz/fugace/internal/transport"
)

const (
	adminPass   = "admin-secret"
	readTimeout = 3 * time.Second
)

func fastRates() *server.Rates {
	r := server.Rate{Burst: 1000, Interval: time.Millisecond}
	return &server.Rates{Message: r, Join: r, Nick: r, Command: r}
}

func baseOpts() server.Options {
	return server.Options{
		Version:          "test",
		AdminPassword:    adminPass,
		Channels:         []string{"#general"},
		HistoryMaxBytes:  1 << 20,
		Rates:            fastRates(),
		OperFailureDelay: time.Millisecond,
	}
}

// start runs a server on a loopback listener and returns its address.
func start(t *testing.T, opts server.Options) (string, *server.Server) {
	t.Helper()
	srv, err := server.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = transport.ServeIRC(ctx, ln, srv); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = srv.Shutdown(sctx)
	})
	return ln.Addr().String(), srv
}

type tc struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	mu   sync.Mutex
	seen []string // every line received, for the no-IP check
}

func dial(t *testing.T, addr string) *tc {
	t.Helper()
	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &tc{t: t, conn: conn, r: bufio.NewReader(conn)}
}

func (c *tc) send(lines ...string) {
	c.t.Helper()
	for _, l := range lines {
		if _, err := c.conn.Write([]byte(l + "\r\n")); err != nil {
			c.t.Fatalf("write %q: %v", l, err)
		}
	}
}

// readLine returns the next line, or "" and false on timeout/EOF.
func (c *tc) readLine(timeout time.Duration) (string, bool) {
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", false
	}
	line = strings.TrimRight(line, "\r\n")
	c.mu.Lock()
	c.seen = append(c.seen, line)
	c.mu.Unlock()
	return line, true
}

// expect reads until a line contains want, and returns it.
func (c *tc) expect(want string) string {
	c.t.Helper()
	deadline := time.Now().Add(readTimeout)
	for time.Now().Before(deadline) {
		line, ok := c.readLine(time.Until(deadline))
		if !ok {
			break
		}
		if strings.Contains(line, want) {
			return line
		}
	}
	c.t.Fatalf("did not receive a line containing %q; got:\n%s", want, strings.Join(c.lines(), "\n"))
	return ""
}

// expectNone checks that no line containing bad arrives within d.
func (c *tc) expectNone(bad string, d time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		line, ok := c.readLine(time.Until(deadline))
		if !ok {
			return
		}
		if strings.Contains(line, bad) {
			c.t.Fatalf("unexpected line %q", line)
		}
	}
}

// expectClosed reads until EOF.
func (c *tc) expectClosed() {
	c.t.Helper()
	deadline := time.Now().Add(readTimeout)
	for time.Now().Before(deadline) {
		_ = c.conn.SetReadDeadline(deadline)
		if _, err := c.r.ReadString('\n'); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() { //nolint:errorlint // net.Error is an interface check
				break
			}
			return
		}
	}
	c.t.Fatal("connection not closed")
}

func (c *tc) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

func register(t *testing.T, addr, nick string, caps ...string) *tc {
	t.Helper()
	c := dial(t, addr)
	if len(caps) > 0 {
		c.send("CAP LS 302")
	}
	c.send("NICK "+nick, "USER "+nick+" 0 * :Real Name")
	if len(caps) > 0 {
		c.send("CAP REQ :"+strings.Join(caps, " "), "CAP END")
	}
	c.expect(" 422 " + nick + " ")
	return c
}

func join(c *tc, nick, ch string) {
	c.t.Helper()
	c.send("JOIN " + ch)
	c.expect(" 366 " + nick + " " + ch + " ")
}

func oper(c *tc) {
	c.t.Helper()
	c.send("OPER admin " + adminPass)
	c.expect(" 381 ")
}

// noIP asserts that no line ever exposed the loopback client address (T2).
func noIP(t *testing.T, clients ...*tc) {
	t.Helper()
	for _, c := range clients {
		for _, l := range c.lines() {
			if strings.Contains(l, "127.0.0.1") || strings.Contains(l, "localhost") {
				t.Errorf("address leaked: %q", l)
			}
		}
	}
}

func TestRegistration(t *testing.T) {
	addr, _ := start(t, baseOpts())
	c := dial(t, addr)
	c.send("NICK alice", "USER alice 0 * :Alice Liddell")
	c.expect(":fugace 001 alice :Welcome to the fugace network, alice")
	c.expect(" 004 alice fugace fugace-test o :no")
	is := c.expect(" 005 alice ")
	for _, tok := range []string{"NETWORK=fugace", "CASEMAPPING=ascii", "CHANTYPES=#", "CHANLIMIT=#:20", "CHANNELLEN=32", "NICKLEN=30", "TOPICLEN=300", "PREFIX=(o)@", "MAXTARGETS=4"} {
		if !strings.Contains(is, tok) {
			t.Errorf("ISUPPORT lacks %s: %q", tok, is)
		}
	}
	c.expect(" 422 alice ")
	noIP(t, c)
}

func TestCommandsBeforeRegistration(t *testing.T) {
	addr, _ := start(t, baseOpts())
	c := dial(t, addr)
	c.send("JOIN #general")
	c.expect(" 451 * :You have not registered")
	c.send("PING :tok")
	c.expect(":fugace PONG fugace :tok")
	c.send("NICK 1bad")
	c.expect(" 432 * 1bad ")
	c.send("NICK")
	c.expect(" 431 ")
	c.send("USER x")
	c.expect(" 461 * USER ")
}

func TestCapNegotiation(t *testing.T) {
	addr, _ := start(t, baseOpts())
	c := dial(t, addr)
	c.send("CAP LS 302", "NICK bob", "USER bob 0 * :b")
	c.expect("CAP * LS :draft/message-redaction echo-message fugace/avatar message-tags server-time")
	c.expectNone(" 001 ", 200*time.Millisecond) // suspended until CAP END
	c.send("CAP REQ :server-time unknown-cap")
	c.expect("CAP bob NAK :server-time unknown-cap")
	c.send("CAP REQ :server-time echo-message")
	c.expect("CAP bob ACK :server-time echo-message")
	c.send("CAP END")
	c.expect(" 001 bob ")
	c.send("CAP LIST")
	c.expect("CAP bob LIST :echo-message server-time")
	c.send("CAP FOO")
	c.expect(" 410 bob FOO ")
}
