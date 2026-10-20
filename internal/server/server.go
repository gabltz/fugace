// Package server holds the chat state (clients, channels, history) and
// dispatches IRC commands. Transports hand it connections through ServeConn.
//
// Locking rule: s.mu protects nicks, channels and every client's identity
// fields. No I/O ever happens under s.mu: handlers compute what to send
// under the lock, release it, then enqueue.
package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
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
	// OperFailureDelay slows down password guessing (OPER and server PASS).
	OperFailureDelay = 2 * time.Second
	// Sizes of the per-boot msgid prefix and of identity hashes.
	msgPrefixBytes = 4
	ownerBytes     = 16
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
	// Account is optional; nil disables pictures and upload clean-up.
	Account AccountHooks
}

// UploadTokens issues a token bound to one connection and attributed to an
// author identity; revoke is called when the connection ends.
type UploadTokens interface {
	Issue(owner string) (token string, revoke func(), err error)
}

// AccountHooks connects the chat core to what an author owns elsewhere
// (files, profile picture). Identities are opaque keyed hashes.
type AccountHooks interface {
	// AvatarVersion returns the version of owner's picture, 0 if none.
	AvatarVersion(owner string) int
	// Cleared deletes everything owner uploaded (CLEAR-ACCOUNT).
	Cleared(owner string)
	// Gone is called when the last connection of owner ends.
	Gone(owner string)
}

// UploadISupport is the vendor ISUPPORT token carrying the upload token.
const UploadISupport = "fugace/UPLOAD"

// Server is the chat core.
type Server struct {
	opts    Options
	rates   Rates
	conns   *limits.Conns
	history *history
	pms     *pmIndex

	ownerKey  []byte        // per-boot HMAC key for identity keys
	msgPrefix string        // per-boot msgid prefix
	msgSeq    atomic.Uint64 // msgid counter

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
	ownerKey := make([]byte, sha256.Size)
	prefix := make([]byte, msgPrefixBytes)
	if _, err := rand.Read(ownerKey); err != nil {
		return nil, fmt.Errorf("server: random key: %w", err)
	}
	if _, err := rand.Read(prefix); err != nil {
		return nil, fmt.Errorf("server: random prefix: %w", err)
	}
	s := &Server{
		opts:    opts,
		rates:   rates,
		conns:   conns,
		history: newHistory(opts.HistoryMaxBytes),
		pms:     newPMIndex(),

		ownerKey:  ownerKey,
		msgPrefix: base64.RawURLEncoding.EncodeToString(prefix),
		clients:   make(map[*client]struct{}),
		nicks:     make(map[string]*client),
		channels:  make(map[string]*channel),
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
	last := true
	for o := range s.clients {
		if o.owner == c.owner {
			last = false
			break
		}
	}
	s.mu.Unlock()
	if last && s.opts.Account != nil {
		s.opts.Account.Gone(c.owner)
	}

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

// stamp carries the tags of a message: its time, and for chat messages
// its msgid and author identity.
type stamp struct {
	at    time.Time
	msgid string
	owner string
}

// fanout sends msg to every recipient. actor, if among them, gets it on its
// reply queue so that it stays ordered with its other replies. Must not be
// called with s.mu held.
func (s *Server) fanout(actor *client, recips []*client, msg irc.Message) {
	s.fanoutStamped(actor, recips, msg, stamp{at: s.opts.Now()})
}

// fanoutStamped adds, per recipient, the time tag (server-time), the msgid
// (message-tags) and fugace/own for the author's own connections. Each
// variant is serialized once.
func (s *Server) fanoutStamped(actor *client, recips []*client, msg irc.Message, st stamp) {
	var cache [8][]byte
	var done [8]bool
	for _, r := range recips {
		v := r.variant(st)
		if !done[v] {
			cache[v], done[v] = encode(tagged(msg, st, v)), true
		}
		b := cache[v]
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

// Tag variants, combined as bits.
const (
	varTime = 1 << iota
	varTags
	varOwn
)

func (c *client) variant(st stamp) int {
	c.s.mu.RLock()
	defer c.s.mu.RUnlock()
	v := 0
	if c.caps[capServerTime] {
		v |= varTime
	}
	if c.caps[capMessageTags] {
		v |= varTags
		if st.owner != "" && st.owner == c.owner {
			v |= varOwn
		}
	}
	return v
}

// ownTag marks, for its author only, a message they can redact.
const ownTag = "fugace/own"

func tagged(m irc.Message, st stamp, v int) irc.Message {
	var tags []irc.Tag
	if v&varTime != 0 {
		tags = append(tags, irc.Tag{Key: "time", Value: formatTime(st.at)})
	}
	if v&varTags != 0 && st.msgid != "" {
		tags = append(tags, irc.Tag{Key: "msgid", Value: st.msgid})
	}
	if v&varOwn != 0 {
		tags = append(tags, irc.Tag{Key: ownTag})
	}
	m.Tags = append(tags, m.Tags...)
	return m
}

// newMsgID returns a unique message id for this boot.
func (s *Server) newMsgID() string {
	return s.msgPrefix + strconv.FormatUint(s.msgSeq.Add(1), 36)
}

// connOwner is the identity of a connection that sent no KEY.
func connOwner() string {
	b := make([]byte, ownerBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return "c" + hex.EncodeToString(b)
}

// keyOwner derives a stable identity from a client secret; only this keyed
// hash is kept, and it changes with every boot.
func (s *Server) keyOwner(key string) string {
	mac := hmac.New(sha256.New, s.ownerKey)
	mac.Write([]byte(key))
	return "k" + hex.EncodeToString(mac.Sum(nil)[:ownerBytes])
}

// OwnerOf returns the identity behind a registered nick.
func (s *Server) OwnerOf(nick string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.nicks[irc.Fold(nick)]
	if c == nil || !c.registered.Load() {
		return "", false
	}
	return c.owner, true
}

// AvatarChanged tells the users sharing a channel with owner's connections
// (and those connections) that the picture changed.
func (s *Server) AvatarChanged(owner string) {
	version := 0
	if s.opts.Account != nil {
		version = s.opts.Account.AvatarVersion(owner)
	}
	type send struct {
		mask  string
		peers []*client
	}
	var sends []send
	s.mu.RLock()
	for c := range s.clients {
		if c.owner != owner || !c.registered.Load() {
			continue
		}
		var peers []*client
		for _, p := range s.peersLocked(c, true) {
			if p.caps[capAvatar] {
				peers = append(peers, p)
			}
		}
		sends = append(sends, send{c.maskLocked(), peers})
	}
	s.mu.RUnlock()
	for _, sd := range sends {
		s.fanout(nil, sd.peers, irc.Message{Source: sd.mask, Command: "AVATAR", Params: []string{strconv.Itoa(version)}})
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
