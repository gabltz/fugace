package server

import (
	"errors"
	"io"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabltz/fugace/internal/limits"
)

// fakeConn feeds lines to the server and can block writes forever.
type fakeConn struct {
	in      chan string
	block   bool
	closed  chan struct{}
	once    sync.Once
	mu      sync.Mutex
	written []string
	unblock chan struct{}
}

func newFakeConn(block bool) *fakeConn {
	return &fakeConn{in: make(chan string, 64), block: block, closed: make(chan struct{}), unblock: make(chan struct{})}
}

func (f *fakeConn) ReadLine() (string, error) {
	select {
	case l := <-f.in:
		return l, nil
	case <-f.closed:
		return "", io.EOF
	}
}

func (f *fakeConn) WriteLine(b []byte) error {
	f.mu.Lock()
	f.written = append(f.written, string(b))
	n := len(f.written)
	f.mu.Unlock()
	if f.block && n > 20 { // let registration through, then stall
		select {
		case <-f.closed:
			return errors.New("closed")
		case <-f.unblock:
		}
	}
	return nil
}

func (f *fakeConn) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeConn) output() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.written, "")
}

func testServer(t *testing.T) *Server {
	t.Helper()
	r := Rate{Burst: 100000, Interval: time.Microsecond}
	s, err := New(Options{Channels: []string{"#general"}, HistoryMaxBytes: 1 << 20, Rates: &Rates{r, r, r, r}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A client that stops reading is cut off once its SendQ is full, and never
// slows down the sender (T7).
func TestSendQExceeded(t *testing.T) {
	s := testServer(t)
	slow, fast := newFakeConn(true), newFakeConn(false)
	addr := netip.MustParseAddr("192.0.2.1")
	go s.ServeConn(slow, ConnInfo{Addr: addr})
	go s.ServeConn(fast, ConnInfo{Addr: addr})
	slow.in <- "NICK slow"
	slow.in <- "USER s 0 * :s"
	slow.in <- "JOIN #general"
	fast.in <- "NICK fast"
	fast.in <- "USER f 0 * :f"
	fast.in <- "JOIN #general"
	waitFor(t, "both joined", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return len(s.channels["#general"].members) == 2
	})
	start := time.Now()
	for range limits.SendQLines + 2*replyQueue + 50 {
		fast.in <- "PRIVMSG #general :flood"
	}
	waitFor(t, "slow client removed", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		_, there := s.nicks["slow"]
		return !there
	})
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("sender was slowed down: %v", el)
	}
	waitFor(t, "quit seen", func() bool { return strings.Contains(fast.output(), ":slow!~user@fugace QUIT :SendQ exceeded") })
	_ = fast.Close()
}

func TestHistoryBudget(t *testing.T) {
	text := strings.Repeat("x", 100)
	e := entry{source: "n!~user@fugace", command: "PRIVMSG", text: text}
	budget := 10 * e.size()
	h := newHistory(budget)
	for i := range 25 {
		key := "#a"
		if i%2 == 1 {
			key = "#b"
		}
		h.add(key, e)
		if h.usage() > budget {
			t.Fatalf("usage %d above budget %d", h.usage(), budget)
		}
	}
	a, b := h.after("#a", 0, 100), h.after("#b", 0, 100)
	if len(a)+len(b) != 10 {
		t.Fatalf("kept %d entries, want 10", len(a)+len(b))
	}
	// The oldest go first: the remaining entries are the 10 most recent.
	if a[0].seq < 16 || b[0].seq < 16 {
		t.Fatalf("oldest not evicted first: %d %d", a[0].seq, b[0].seq)
	}
	h.clear("#a")
	if got := h.usage(); got != int64(len(b))*e.size() {
		t.Fatalf("usage after clear = %d", got)
	}
	// Chunked reads.
	if got := h.after("#b", b[1].seq, 2); len(got) != 2 || got[0].seq != b[2].seq {
		t.Fatalf("after() = %+v", got)
	}
}

func TestHistoryDisabledAndOversized(t *testing.T) {
	h := newHistory(0)
	h.add("#a", entry{text: "x"})
	if h.usage() != 0 || len(h.after("#a", 0, 10)) != 0 {
		t.Fatal("disabled history stored something")
	}
	h = newHistory(10)
	h.add("#a", entry{text: "far too large for the budget"})
	if h.usage() != 0 {
		t.Fatal("oversized entry stored")
	}
}

func TestNewRejectsBadChannels(t *testing.T) {
	if _, err := New(Options{Channels: []string{"nohash"}}); err == nil {
		t.Fatal("invalid channel accepted")
	}
	many := make([]string, limits.MaxChannels+1)
	for i := range many {
		many[i] = "#c" + strings.Repeat("x", i%20) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, err := New(Options{Channels: many}); err == nil {
		t.Fatal("too many channels accepted")
	}
}

func TestStampText(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := stampText("hi", at); got != "[2026-01-02 03:04] hi" {
		t.Errorf("plain: %q", got)
	}
	if got := stampText("\x01ACTION waves\x01", at); got != "\x01ACTION [2026-01-02 03:04] waves\x01" {
		t.Errorf("action: %q", got)
	}
}
