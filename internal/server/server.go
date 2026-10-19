// Package server holds the chat state (clients, channels, history) and
// dispatches IRC commands. Transports hand it connections through ServeConn.
//
// Locking rule: s.mu protects nicks, channels and every client's identity
// fields. No I/O ever happens under s.mu: handlers compute what to send
// under the lock, release it, then enqueue.
package server

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"

	"github.com/gabltz/fugace/internal/irc"
	"github.com/gabltz/fugace/internal/limits"
)

// Fixed identity of the server and of every user host.
const (
	ServerName  = "fugace"
	NetworkName = "fugace"
	HiddenHost  = "fugace"
)

// Timers (SPEC §5).
const (
	DefaultPingInterval        = 90 * time.Second
	DefaultPingTimeout         = 180 * time.Second
	DefaultRegistrationTimeout = 30 * time.Second
	DefaultTickInterval        = 5 * time.Second
	// OperFailureDelay slows down password guessing on OPER.
	OperFailureDelay = 2 * time.Second
	// historyChunk bounds how many history entries are copied at once.
	historyChunk = 128
	// replyQueue is the capacity of the blocking queue used for replies to
	// the client's own commands (backpressure, never a disconnection).
	replyQueue = 64
)

// ErrLineTooLong may be returned by Conn.ReadLine for an oversized line;
// the connection stays usable.
var ErrLineTooLong = irc.ErrLineTooLong

// Conn is one client connection as provided by a transport.
type Conn interface {
	// ReadLine returns the next line without its line terminator.
	ReadLine() (string, error)
	// WriteLine writes one serialized line, bounded by a write timeout.
	WriteLine(b []byte) error
	// Close aborts the connection immediately; it must not block.
	Close() error
}

// Kind tells which transport a client uses; it selects its hostmask.
type Kind int

// Transport kinds.
const (
	KindTCP Kind = iota
	KindWeb
)

// ConnInfo describes a new connection. Addr is used only to compute a
// keyed hash for the per-source limit, then forgotten.
type ConnInfo struct {
	Addr netip.Addr
	Kind Kind
}

// Rate is a token bucket setting.
type Rate struct {
	Burst    int
	Interval time.Duration
}

// Rates groups the per-client rate limits.
type Rates struct {
	Message, Join, Nick, Command Rate
}

// DefaultRates returns the limits of ARCHITECTURE §6.
func DefaultRates() Rates {
	return Rates{
		Message: Rate{limits.MessageBurst, limits.MessageInterval},
		Join:    Rate{limits.JoinBurst, limits.JoinInterval},
		Nick:    Rate{limits.NickBurst, limits.NickInterval},
		Command: Rate{limits.CommandBurst, limits.CommandInterval},
	}
}

// Options configures a Server. Zero values select the defaults.
type Options struct {
	Version             string
	Password            string // server password (PASS); empty = open
	AdminPassword       string // OPER password; empty = no admin
	Channels            []string
	HistoryMaxBytes     int64
	PingInterval        time.Duration
	PingTimeout         time.Duration
	RegistrationTimeout time.Duration
	TickInterval        time.Duration
	OperFailureDelay    time.Duration
	Rates               *Rates
	Now                 func() time.Time
	MaxConns            int
	MaxConnsPerSource   int
	// Uploads issues the per-connection upload token announced in 005
	// (fugace/UPLOAD); nil disables file sharing.
	Uploads UploadTokens
}

// UploadTokens issues a token bound to one connection; revoke is called
// when the connection ends.
type UploadTokens interface {
	Issue() (token string, revoke func(), err error)
}

// UploadISupport is the vendor ISUPPORT token carrying the upload token.
const UploadISupport = "fugace/UPLOAD"

// Server is the chat core.
type Server struct {
	opts    Options
	rates   Rates
	conns   *limits.Conns
	history *history

	mu       sync.RWMutex
	clients  map[*client]struct{}
	nicks    map[string]*client  // folded nick -> client (reserved before registration)
	channels map[string]*channel // folded name
	closing  bool

	wg sync.WaitGroup
}

// New returns a Server with the startup channels created.
func New(opts Options) (*Server, error) {
	if opts.PingInterval <= 0 {
		opts.PingInterval = DefaultPingInterval
	}
	if opts.PingTimeout <= 0 {
		opts.PingTimeout = DefaultPingTimeout
	}
	if opts.RegistrationTimeout <= 0 {
		opts.RegistrationTimeout = DefaultRegistrationTimeout
	}
	if opts.TickInterval <= 0 {
		opts.TickInterval = DefaultTickInterval
	}
	if opts.OperFailureDelay <= 0 {
		opts.OperFailureDelay = OperFailureDelay
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxConns <= 0 {
		opts.MaxConns = limits.MaxConns
	}
	if opts.MaxConnsPerSource <= 0 {
		opts.MaxConnsPerSource = limits.MaxConnsPerSource
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	rates := DefaultRates()
	if opts.Rates != nil {
		rates = *opts.Rates
	}
	conns, err := limits.NewConns(opts.MaxConns, opts.MaxConnsPerSource)
	if err != nil {
		return nil, err
	}
	s := &Server{
		opts:     opts,
		rates:    rates,
		conns:    conns,
		history:  newHistory(opts.HistoryMaxBytes),
		clients:  make(map[*client]struct{}),
		nicks:    make(map[string]*client),
		channels: make(map[string]*channel),
	}
	for _, name := range opts.Channels {
		if !irc.ValidChannel(name) {
			return nil, errors.New("server: invalid startup channel")
		}
		if len(s.channels) >= limits.MaxChannels {
			return nil, errors.New("server: too many startup channels")
		}
		s.channels[irc.Fold(name)] = newChannel(name)
	}
	return s, nil
}

// ServeConn runs one connection until it ends. It never panics and never
// prints anything.
func (s *Server) ServeConn(conn Conn, info ConnInfo) {
	release, ok := s.conns.Acquire(info.Addr)
	if !ok {
		s.refuse(conn, "Too many connections")
		return
	}
	defer release()

	c := newClient(s, conn, info)
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		s.refuse(conn, "Server shutting down")
		return
	}
	s.clients[c] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()

	go c.writeLoop()
	reason := c.readLoop()
	s.disconnect(c, reason)
	<-c.writerDone
}

func (s *Server) refuse(conn Conn, reason string) {
	m := irc.Message{Command: "ERROR", Params: []string{"Closing Link: " + reason}}
	if b, err := m.Bytes(); err == nil {
		_ = conn.WriteLine(b) // best effort before closing
	}
	_ = conn.Close()
}

// ConnCount returns the number of live connections.
func (s *Server) ConnCount() int { return s.conns.Count() }

// Shutdown notifies every client, closes their connections and waits for
// them, at most until ctx is done.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	all := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		all = append(all, c)
	}
	s.mu.Unlock()

	for _, c := range all {
		c.reply(s.serverNotice(c, "Server shutting down"))
		c.finish("Server shutting down")
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		for _, c := range all {
			c.abort("Server shutting down")
		}
		<-done
		return ctx.Err()
	}
}

// disconnect removes c from the state and tells the users sharing a
// channel with it. It is idempotent.
func (s *Server) disconnect(c *client, reason string) {
	s.mu.Lock()
	if c.gone {
		s.mu.Unlock()
		return
	}
	c.gone = true
	delete(s.clients, c)
	if c.nick != "" && s.nicks[irc.Fold(c.nick)] == c {
		delete(s.nicks, irc.Fold(c.nick))
	}
	var peers []*client
	if c.registered.Load() {
		peers = s.peersLocked(c, false)
	}
	for key, ch := range c.channels {
		delete(ch.members, c)
		delete(c.channels, key)
	}
	mask := c.maskLocked()
	s.mu.Unlock()

	s.fanout(nil, peers, irc.Message{Source: mask, Command: "QUIT", Params: []string{reason}})
	if c.uploadRevoke != nil {
		c.uploadRevoke() // T9: no upload once the connection is gone
	}
	c.finish(reason)
}

// peersLocked returns the distinct clients sharing a channel with c,
// optionally including c itself. Caller holds s.mu.
func (s *Server) peersLocked(c *client, self bool) []*client {
	seen := make(map[*client]struct{})
	if self {
		seen[c] = struct{}{}
	}
	for _, ch := range c.channels {
		for m := range ch.members {
			seen[m] = struct{}{}
		}
	}
	if !self {
		delete(seen, c)
	}
	out := make([]*client, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	return out
}

// fanout sends msg to every recipient. actor, if among them, gets it on its
// reply queue so that it stays ordered with its other replies. Serialization
// happens at most twice (with and without server-time). Must not be called
// with s.mu held.
func (s *Server) fanout(actor *client, recips []*client, msg irc.Message) {
	if len(recips) == 0 {
		return
	}
	at := s.opts.Now()
	var plain, tagged []byte
	for _, r := range recips {
		var b []byte
		if r.hasCap(capServerTime) {
			if tagged == nil {
				tagged = encode(withTime(msg, at))
			}
			b = tagged
		} else {
			if plain == nil {
				plain = encode(msg)
			}
			b = plain
		}
		if b == nil {
			continue
		}
		if r == actor {
			r.enqueueReply(b)
		} else {
			r.deliver(b)
		}
	}
}

func withTime(m irc.Message, at time.Time) irc.Message {
	m.Tags = append([]irc.Tag{{Key: "time", Value: formatTime(at)}}, m.Tags...)
	return m
}

func formatTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// encode serializes m; a message that cannot be encoded is dropped (nil).
func encode(m irc.Message) []byte {
	b, err := m.Bytes()
	if err != nil {
		return nil
	}
	return b
}
