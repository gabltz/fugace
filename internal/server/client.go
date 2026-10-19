package server

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gabltz/fugace/internal/irc"
	"github.com/gabltz/fugace/internal/limits"
)

// Capabilities offered with CAP LS.
const (
	capEchoMessage = "echo-message"
	capMessageTags = "message-tags"
	capServerTime  = "server-time"
)

// supportedCaps returns the offered capabilities, in CAP LS order.
func supportedCaps() [3]string { return [...]string{capEchoMessage, capMessageTags, capServerTime} }

func isSupportedCap(name string) bool {
	for _, c := range supportedCaps() {
		if c == name {
			return true
		}
	}
	return false
}

// client is one connection. Fields marked (mu) are written by the client's
// reader goroutine under s.mu and read by others under s.mu.
type client struct {
	s    *Server
	conn Conn
	kind Kind

	nick     string              // (mu)
	oper     bool                // (mu)
	caps     map[string]bool     // (mu)
	channels map[string]*channel // (mu) folded name -> channel
	gone     bool                // (mu) removed from the state

	registered atomic.Bool

	// Reader-goroutine-only registration state.
	gotUser      bool
	capNeg       bool
	pass         string
	operFailures int
	connectedAt  time.Time

	msgBucket, joinBucket, nickBucket, cmdBucket *limits.Bucket

	// Output: replies (blocking, own commands) and out (non-blocking,
	// messages from others, bounded by the SendQ).
	replies    chan []byte
	out        chan []byte
	drain      chan struct{} // closed by finish: flush then close
	dead       chan struct{} // closed by abort: close now
	finishOnce sync.Once
	abortOnce  sync.Once
	writerDone chan struct{}
	killReason atomic.Pointer[string]

	lastRecv atomic.Int64 // unix nanoseconds
	pingSent atomic.Bool
}

func newClient(s *Server, conn Conn, info ConnInfo) *client {
	now := s.opts.Now()
	c := &client{
		s:           s,
		conn:        conn,
		kind:        info.Kind,
		caps:        make(map[string]bool),
		channels:    make(map[string]*channel),
		connectedAt: now,
		msgBucket:   limits.NewBucket(s.rates.Message.Burst, s.rates.Message.Interval),
		joinBucket:  limits.NewBucket(s.rates.Join.Burst, s.rates.Join.Interval),
		nickBucket:  limits.NewBucket(s.rates.Nick.Burst, s.rates.Nick.Interval),
		cmdBucket:   limits.NewBucket(s.rates.Command.Burst, s.rates.Command.Interval),
		replies:     make(chan []byte, replyQueue),
		out:         make(chan []byte, limits.SendQLines),
		drain:       make(chan struct{}),
		dead:        make(chan struct{}),
		writerDone:  make(chan struct{}),
	}
	c.lastRecv.Store(now.UnixNano())
	return c
}

// userName is the fixed user part of the hostmask: the client-supplied
// ident is discarded so that a local account name never leaks.
func (c *client) userName() string {
	if c.kind == KindWeb {
		return "web"
	}
	return "~user"
}

// maskLocked returns nick!user@host. Caller holds s.mu (or is the reader).
func (c *client) maskLocked() string {
	nick := c.nick
	if nick == "" {
		nick = "*"
	}
	return nick + "!" + c.userName() + "@" + HiddenHost
}

func (c *client) hasCap(name string) bool {
	c.s.mu.RLock()
	defer c.s.mu.RUnlock()
	return c.caps[name]
}

// hasCapLocked is hasCap for callers already holding s.mu.
func (c *client) hasCapLocked(name string) bool { return c.caps[name] }

// enqueueReply queues a reply to the client's own command. It blocks while
// the queue is full (backpressure on this client only), and gives up when
// the connection dies.
func (c *client) enqueueReply(b []byte) {
	select {
	case c.replies <- b:
	case <-c.dead:
	case <-c.drain:
	}
}

// reply serializes and queues a reply.
func (c *client) reply(m irc.Message) {
	if c.hasCap(capServerTime) {
		m = withTime(m, c.s.opts.Now())
	}
	if b := encode(m); b != nil {
		c.enqueueReply(b)
	}
}

// deliver queues a message from someone else without ever blocking. A full
// SendQ disconnects the client.
func (c *client) deliver(b []byte) {
	select {
	case <-c.dead:
		return
	case <-c.drain:
		return
	default:
	}
	select {
	case c.out <- b:
	default:
		c.abort("SendQ exceeded")
	}
}

// finish flushes what is queued, sends ERROR and closes the connection.
func (c *client) finish(reason string) {
	c.finishOnce.Do(func() {
		c.setKillReason(reason)
		close(c.drain)
	})
}

// abort closes the connection at once.
func (c *client) abort(reason string) {
	c.abortOnce.Do(func() {
		c.setKillReason(reason)
		close(c.dead)
		_ = c.conn.Close() // unblocks the reader; must not block
	})
}

func (c *client) setKillReason(reason string) {
	c.killReason.CompareAndSwap(nil, &reason)
}

func (c *client) reason(def string) string {
	if r := c.killReason.Load(); r != nil {
		return *r
	}
	return def
}

func (c *client) write(b []byte) bool {
	if err := c.conn.WriteLine(b); err != nil {
		c.abort("Write error")
		return false
	}
	return true
}

func (c *client) writeLoop() {
	defer close(c.writerDone)
	defer func() { _ = recover() }() // never print: values may hold user data
	defer func() { _ = c.conn.Close() }()

	tick := time.NewTicker(c.s.opts.TickInterval)
	defer tick.Stop()
	for {
		select {
		case b := <-c.replies:
			if !c.write(b) {
				return
			}
		case b := <-c.out:
			if !c.write(b) {
				return
			}
		case <-c.drain:
			c.flush()
			return
		case <-c.dead:
			return
		case <-tick.C:
			if !c.checkTimers() {
				return
			}
		}
	}
}

// flush writes what is still queued, then the final ERROR line.
func (c *client) flush() {
	for {
		select {
		case b := <-c.replies:
			if !c.write(b) {
				return
			}
			continue
		case b := <-c.out:
			if !c.write(b) {
				return
			}
			continue
		case <-c.dead:
			return
		default:
		}
		break
	}
	m := irc.Message{Command: "ERROR", Params: []string{"Closing Link: " + ServerName + " (" + c.reason("Closed") + ")"}}
	if b := encode(m); b != nil {
		c.write(b)
	}
}

// checkTimers enforces the registration timeout and the ping cycle. It
// returns false when the connection was closed.
func (c *client) checkTimers() bool {
	now := c.s.opts.Now()
	if !c.registered.Load() {
		if now.Sub(c.connectedAt) >= c.s.opts.RegistrationTimeout {
			c.closeWithError("Registration timeout")
			return false
		}
		return true
	}
	idle := now.Sub(time.Unix(0, c.lastRecv.Load()))
	switch {
	case idle >= c.s.opts.PingTimeout:
		c.closeWithError("Ping timeout")
		return false
	case idle >= c.s.opts.PingInterval && !c.pingSent.Load():
		c.pingSent.Store(true)
		if b := encode(irc.Message{Command: "PING", Params: []string{ServerName}}); b != nil {
			return c.write(b)
		}
	}
	return true
}

func (c *client) closeWithError(reason string) {
	m := irc.Message{Command: "ERROR", Params: []string{"Closing Link: " + ServerName + " (" + reason + ")"}}
	if b := encode(m); b != nil {
		_ = c.conn.WriteLine(b) // best effort
	}
	c.abort(reason)
}

// wait sleeps for d unless the connection dies first.
func (c *client) wait(d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-c.dead:
		return false
	case <-c.drain:
		return false
	}
}

// throttle applies fake lag from b.
func (c *client) throttle(b *limits.Bucket) bool {
	return c.wait(b.Take(c.s.opts.Now()))
}

// readLoop reads and dispatches lines until the connection ends, and
// returns the quit reason shown to others.
func (c *client) readLoop() (reason string) {
	defer func() {
		if recover() != nil { // never print: values may hold user data
			reason = "Internal error"
			c.abort(reason)
		}
	}()
	for {
		line, err := c.conn.ReadLine()
		if errors.Is(err, ErrLineTooLong) {
			c.touch()
			c.reply(c.s.numeric(c, irc.ErrInputTooLong, "Input line was too long"))
			continue
		}
		if err != nil {
			return c.reason("Connection closed")
		}
		c.touch()
		msg, err := irc.Parse(line)
		switch {
		case errors.Is(err, irc.ErrLineTooLong), errors.Is(err, irc.ErrTagsTooLong):
			c.reply(c.s.numeric(c, irc.ErrInputTooLong, "Input line was too long"))
			continue
		case err != nil:
			continue // empty or malformed line: ignored
		}
		if !c.throttle(c.cmdBucket) {
			return c.reason("Connection closed")
		}
		if quit, why := c.s.dispatch(c, msg); quit {
			return why
		}
	}
}

func (c *client) touch() {
	c.lastRecv.Store(c.s.opts.Now().UnixNano())
	c.pingSent.Store(false)
}

// capList returns the enabled capabilities. Caller holds s.mu or is the reader.
func (c *client) capList() string {
	var out []string
	for _, name := range supportedCaps() {
		if c.caps[name] {
			out = append(out, name)
		}
	}
	return strings.Join(out, " ")
}
