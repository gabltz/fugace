package server

import (
	"crypto/subtle"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gabltz/fugace/internal/irc"
	"github.com/gabltz/fugace/internal/limits"
)

// namesLineBudget keeps 353 lines well under 512 bytes after the prefix.
const namesLineBudget = 400

// numeric builds a server reply to c. Only called by c's reader goroutine.
func (s *Server) numeric(c *client, code string, params ...string) irc.Message {
	target := c.nick
	if target == "" {
		target = "*"
	}
	return irc.Message{Source: ServerName, Command: code, Params: append([]string{target}, params...)}
}

// serverNotice builds a NOTICE from the server; safe from any goroutine.
func (s *Server) serverNotice(c *client, text string) irc.Message {
	s.mu.RLock()
	target := c.nick
	s.mu.RUnlock()
	if target == "" {
		target = "*"
	}
	return irc.Message{Source: ServerName, Command: "NOTICE", Params: []string{target, text}}
}

func (s *Server) dispatch(c *client, m irc.Message) (quit bool, reason string) {
	if !c.registered.Load() {
		switch m.Command {
		case "CAP", "PASS", "NICK", "USER", "PING", "PONG", "QUIT":
		default:
			c.reply(s.numeric(c, irc.ErrNotRegistered, "You have not registered"))
			return false, ""
		}
	}
	switch m.Command {
	case "CAP":
		return s.cmdCap(c, m)
	case "PASS":
		s.cmdPass(c, m)
	case "NICK":
		return s.cmdNick(c, m)
	case "USER":
		return s.cmdUser(c, m)
	case "PING":
		s.cmdPing(c, m)
	case "PONG", "TAGMSG":
		// activity already recorded; client tags are ignored
	case "QUIT":
		if text := m.Param(0); text != "" {
			return true, "Quit: " + text
		}
		return true, "Quit"
	case "JOIN":
		s.cmdJoin(c, m)
	case "PART":
		s.cmdPart(c, m)
	case "PRIVMSG", "NOTICE":
		s.cmdMessage(c, m)
	case "TOPIC":
		s.cmdTopic(c, m)
	case "NAMES":
		s.cmdNames(c, m)
	case "LIST":
		s.cmdList(c, m)
	case "WHO":
		s.cmdWho(c, m)
	case "WHOIS":
		s.cmdWhois(c, m)
	case "MODE":
		s.cmdMode(c, m)
	case "MOTD":
		c.reply(s.numeric(c, irc.ErrNoMotd, "MOTD File is missing"))
	case "LUSERS":
		s.lusers(c)
	case "VERSION":
		c.reply(s.numeric(c, irc.RplVersion, "fugace-"+s.opts.Version, ServerName, ""))
	case "TIME":
		c.reply(s.numeric(c, irc.RplTime, ServerName, s.opts.Now().UTC().Format(time.RFC1123)))
	case "OPER":
		return s.cmdOper(c, m)
	case "DELCHAN":
		s.cmdDelChan(c, m)
	case "CLEARHISTORY":
		s.cmdClearHistory(c, m)
	default:
		c.reply(s.numeric(c, irc.ErrUnknownCommand, m.Command, "Unknown command"))
	}
	return false, ""
}

func (s *Server) needMore(c *client, cmd string) {
	c.reply(s.numeric(c, irc.ErrNeedMoreParams, cmd, "Not enough parameters"))
}

// --- registration ---

func (s *Server) cmdCap(c *client, m irc.Message) (bool, string) {
	registered := c.registered.Load()
	switch sub := strings.ToUpper(m.Param(0)); sub {
	case "LS":
		if !registered {
			c.capNeg = true
		}
		all := supportedCaps()
		c.reply(s.numericCap(c, "LS", strings.Join(all[:], " ")))
	case "LIST":
		s.mu.RLock()
		list := c.capList()
		s.mu.RUnlock()
		c.reply(s.numericCap(c, "LIST", list))
	case "REQ":
		if !registered {
			c.capNeg = true
		}
		req := m.Param(1)
		fields := strings.Fields(req)
		ok := len(fields) > 0
		for _, f := range fields {
			if !isSupportedCap(strings.TrimPrefix(f, "-")) {
				ok = false
			}
		}
		if !ok {
			c.reply(s.numericCap(c, "NAK", req))
			return false, ""
		}
		s.mu.Lock()
		for _, f := range fields {
			if name, off := strings.CutPrefix(f, "-"); off {
				delete(c.caps, name)
			} else {
				c.caps[f] = true
			}
		}
		s.mu.Unlock()
		c.reply(s.numericCap(c, "ACK", req))
	case "END":
		c.capNeg = false
		return s.tryRegister(c)
	default:
		c.reply(s.numeric(c, irc.ErrInvalidCapCmd, m.Param(0), "Invalid CAP command"))
	}
	return false, ""
}

func (s *Server) numericCap(c *client, sub, list string) irc.Message {
	target := c.nick
	if target == "" {
		target = "*"
	}
	return irc.Message{Source: ServerName, Command: "CAP", Params: []string{target, sub, list}}
}

func (s *Server) cmdPass(c *client, m irc.Message) {
	switch {
	case c.registered.Load():
		c.reply(s.numeric(c, irc.ErrAlreadyRegistered, "You may not reregister"))
	case len(m.Params) < 1:
		s.needMore(c, "PASS")
	default:
		c.pass = m.Params[0]
	}
}

func (s *Server) cmdUser(c *client, m irc.Message) (bool, string) {
	switch {
	case c.registered.Load():
		c.reply(s.numeric(c, irc.ErrAlreadyRegistered, "You may not reregister"))
	case len(m.Params) < 4:
		s.needMore(c, "USER")
	default:
		// Ident and realname are discarded on purpose (privacy).
		c.gotUser = true
		return s.tryRegister(c)
	}
	return false, ""
}

func (s *Server) cmdNick(c *client, m irc.Message) (bool, string) {
	nick := m.Param(0)
	if nick == "" {
		c.reply(s.numeric(c, irc.ErrNoNicknameGiven, "No nickname given"))
		return false, ""
	}
	if !irc.ValidNick(nick) {
		c.reply(s.numeric(c, irc.ErrErroneusNickname, nick, "Erroneous nickname"))
		return false, ""
	}
	registered := c.registered.Load()
	if registered && !c.throttle(c.nickBucket) {
		return false, ""
	}
	key := irc.Fold(nick)
	s.mu.Lock()
	if other, taken := s.nicks[key]; taken && other != c {
		s.mu.Unlock()
		c.reply(s.numeric(c, irc.ErrNicknameInUse, nick, "Nickname is already in use"))
		return false, ""
	}
	if c.nick == nick {
		s.mu.Unlock()
		return false, ""
	}
	oldMask := c.maskLocked()
	if c.nick != "" {
		delete(s.nicks, irc.Fold(c.nick))
	}
	s.nicks[key] = c
	c.nick = nick
	var peers []*client
	if registered {
		peers = s.peersLocked(c, true)
	}
	s.mu.Unlock()
	if registered {
		s.fanout(c, peers, irc.Message{Source: oldMask, Command: "NICK", Params: []string{nick}})
		return false, ""
	}
	return s.tryRegister(c)
}

func (s *Server) tryRegister(c *client) (bool, string) {
	if c.registered.Load() || c.nick == "" || !c.gotUser || c.capNeg {
		return false, ""
	}
	if s.opts.Password != "" && subtle.ConstantTimeCompare([]byte(c.pass), []byte(s.opts.Password)) != 1 {
		c.reply(s.numeric(c, irc.ErrPasswdMismatch, "Password incorrect"))
		return true, "Bad password"
	}
	c.pass = ""
	c.registered.Store(true)
	s.welcome(c)
	return false, ""
}

func (s *Server) isupport() []string {
	return []string{
		"CASEMAPPING=ascii",
		"CHANLIMIT=#:" + strconv.Itoa(limits.MaxChannelsPerClient),
		"CHANMODES=,,,n",
		"CHANNELLEN=" + strconv.Itoa(irc.ChannelLen),
		"CHANTYPES=#",
		"MAXTARGETS=" + strconv.Itoa(limits.MaxTargets),
		"NETWORK=" + NetworkName,
		"NICKLEN=" + strconv.Itoa(irc.NickLen),
		"PREFIX=(o)@",
		"TARGMAX=PRIVMSG:" + strconv.Itoa(limits.MaxTargets) + ",NOTICE:" + strconv.Itoa(limits.MaxTargets),
		"TOPICLEN=" + strconv.Itoa(irc.TopicLen),
	}
}

func (s *Server) welcome(c *client) {
	v := "fugace-" + s.opts.Version
	c.reply(s.numeric(c, irc.RplWelcome, "Welcome to the "+NetworkName+" network, "+c.nick))
	c.reply(s.numeric(c, irc.RplYourHost, "Your host is "+ServerName+", running version "+v))
	c.reply(s.numeric(c, irc.RplCreated, "This server keeps nothing on disk"))
	c.reply(s.numeric(c, irc.RplMyInfo, ServerName, v, "o", "no"))
	c.reply(s.numeric(c, irc.RplISupport, append(s.isupport(), "are supported by this server")...))
	s.lusers(c)
	c.reply(s.numeric(c, irc.ErrNoMotd, "MOTD File is missing"))
}

func (s *Server) lusers(c *client) {
	s.mu.RLock()
	users, chans := len(s.nicks), len(s.channels)
	s.mu.RUnlock()
	c.reply(s.numeric(c, irc.RplLuserClient, "There are "+strconv.Itoa(users)+" users and 0 invisible on 1 servers"))
	c.reply(s.numeric(c, irc.RplLuserChannels, strconv.Itoa(chans), "channels formed"))
	c.reply(s.numeric(c, irc.RplLuserMe, "I have "+strconv.Itoa(users)+" clients and 0 servers"))
}

func (s *Server) cmdPing(c *client, m irc.Message) {
	if len(m.Params) < 1 || m.Params[0] == "" {
		c.reply(s.numeric(c, irc.ErrNoOrigin, "No origin specified"))
		return
	}
	c.reply(irc.Message{Source: ServerName, Command: "PONG", Params: []string{ServerName, m.Params[0]}})
}

// --- channels ---

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) cmdJoin(c *client, m irc.Message) {
	if len(m.Params) < 1 {
		s.needMore(c, "JOIN")
		return
	}
	if m.Params[0] == "0" {
		s.partAll(c)
		return
	}
	for _, name := range splitList(m.Params[0]) {
		if !irc.ValidChannel(name) {
			c.reply(s.numeric(c, irc.ErrBadChanMask, name, "Bad channel mask"))
			continue
		}
		if !c.throttle(c.joinBucket) {
			return
		}
		s.joinOne(c, name)
	}
}

func (s *Server) joinOne(c *client, name string) {
	key := irc.Fold(name)
	s.mu.Lock()
	ch := s.channels[key]
	if ch == nil {
		switch {
		case !c.oper:
			s.mu.Unlock()
			c.reply(s.numeric(c, irc.ErrNoSuchChannel, name, "No such channel (only the admin can create channels)"))
			return
		case len(s.channels) >= limits.MaxChannels:
			s.mu.Unlock()
			c.reply(s.numeric(c, irc.ErrTooManyChannels, name, "Too many channels on this server"))
			return
		}
		ch = newChannel(name)
		s.channels[key] = ch
	}
	if _, in := ch.members[c]; in {
		s.mu.Unlock()
		return
	}
	if len(c.channels) >= limits.MaxChannelsPerClient {
		s.mu.Unlock()
		c.reply(s.numeric(c, irc.ErrTooManyChannels, ch.name, "You have joined too many channels"))
		return
	}
	if len(ch.members) >= limits.MaxMembersPerChannel {
		s.mu.Unlock()
		c.reply(s.numeric(c, irc.ErrChannelIsFull, ch.name, "Cannot join channel (channel is full)"))
		return
	}
	ch.members[c] = struct{}{}
	c.channels[key] = ch
	recips, topic, names, mask, chName := ch.memberList(), ch.topic, namesLocked(ch), c.maskLocked(), ch.name
	s.mu.Unlock()

	s.fanout(c, recips, irc.Message{Source: mask, Command: "JOIN", Params: []string{chName}})
	if topic != "" {
		c.reply(s.numeric(c, irc.RplTopic, chName, topic))
	}
	s.sendNames(c, chName, names)
	s.replayHistory(c, key, chName)
}

// namesLocked lists the members of ch with their prefix. Caller holds s.mu.
func namesLocked(ch *channel) []string {
	names := make([]string, 0, len(ch.members))
	for m := range ch.members {
		if m.oper {
			names = append(names, "@"+m.nick)
		} else {
			names = append(names, m.nick)
		}
	}
	sort.Strings(names)
	return names
}

func (s *Server) sendNames(c *client, chName string, names []string) {
	var line strings.Builder
	for _, n := range names {
		if line.Len() > 0 && line.Len()+1+len(n) > namesLineBudget {
			c.reply(s.numeric(c, irc.RplNamReply, "=", chName, line.String()))
			line.Reset()
		}
		if line.Len() > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(n)
	}
	if line.Len() > 0 {
		c.reply(s.numeric(c, irc.RplNamReply, "=", chName, line.String()))
	}
	c.reply(s.numeric(c, irc.RplEndOfNames, chName, "End of /NAMES list"))
}

func (s *Server) replayHistory(c *client, key, chName string) {
	serverTime := c.hasCap(capServerTime)
	var last uint64
	for {
		batch := s.history.after(key, last, historyChunk)
		if len(batch) == 0 {
			return
		}
		for i := range batch {
			e := &batch[i]
			last = e.seq
			msg := irc.Message{Source: e.source, Command: e.command, Params: []string{chName, e.text}}
			if serverTime {
				msg = withTime(msg, e.at)
			} else {
				msg.Params[1] = stampText(e.text, e.at)
			}
			if b := encode(msg); b != nil {
				c.enqueueReply(b)
			}
		}
		select {
		case <-c.dead:
			return
		case <-c.drain:
			return
		default:
		}
	}
}

// stampText prefixes a replayed message with its UTC time, inside a CTCP
// ACTION if needed, for clients without server-time.
func stampText(text string, at time.Time) string {
	stamp := "[" + at.UTC().Format("2006-01-02 15:04") + "] "
	if rest, ok := strings.CutPrefix(text, "\x01ACTION "); ok {
		return "\x01ACTION " + stamp + rest
	}
	return stamp + text
}

func (s *Server) cmdPart(c *client, m irc.Message) {
	if len(m.Params) < 1 {
		s.needMore(c, "PART")
		return
	}
	for _, name := range splitList(m.Params[0]) {
		s.partOne(c, name, m.Param(1))
	}
}

func (s *Server) partAll(c *client) {
	s.mu.RLock()
	names := make([]string, 0, len(c.channels))
	for _, ch := range c.channels {
		names = append(names, ch.name)
	}
	s.mu.RUnlock()
	for _, n := range names {
		s.partOne(c, n, "")
	}
}

func (s *Server) partOne(c *client, name, reason string) {
	key := irc.Fold(name)
	s.mu.Lock()
	ch := s.channels[key]
	if ch == nil {
		s.mu.Unlock()
		c.reply(s.numeric(c, irc.ErrNoSuchChannel, name, "No such channel"))
		return
	}
	if _, in := ch.members[c]; !in {
		s.mu.Unlock()
		c.reply(s.numeric(c, irc.ErrNotOnChannel, ch.name, "You're not on that channel"))
		return
	}
	recips, mask, chName := ch.memberList(), c.maskLocked(), ch.name
	delete(ch.members, c)
	delete(c.channels, key)
	s.mu.Unlock()

	params := []string{chName}
	if reason != "" {
		params = append(params, reason)
	}
	s.fanout(c, recips, irc.Message{Source: mask, Command: "PART", Params: params})
}

// --- messages ---

func isDCC(text string) bool {
	const p = "\x01DCC"
	return len(text) >= len(p) && strings.EqualFold(text[:len(p)], p)
}

// storable reports whether a channel message goes to history: plain text
// and ACTION only, so that replaying never triggers CTCP auto-replies.
func storable(text string) bool {
	return !strings.HasPrefix(text, "\x01") || strings.HasPrefix(text, "\x01ACTION ")
}

func (s *Server) cmdMessage(c *client, m irc.Message) {
	notice := m.Command == "NOTICE"
	fail := func(code string, params ...string) {
		if !notice { // NOTICE never triggers error replies
			c.reply(s.numeric(c, code, params...))
		}
	}
	if m.Param(0) == "" {
		fail(irc.ErrNoRecipient, "No recipient given ("+m.Command+")")
		return
	}
	if m.Param(1) == "" {
		fail(irc.ErrNoTextToSend, "No text to send")
		return
	}
	seen := make(map[string]bool)
	var targets []string
	for _, t := range splitList(m.Params[0]) {
		if f := irc.Fold(t); !seen[f] {
			seen[f] = true
			targets = append(targets, t)
		}
	}
	if len(targets) > limits.MaxTargets {
		fail(irc.ErrTooManyTargets, m.Params[0], "Too many targets")
		return
	}
	text := m.Params[1]
	if isDCC(text) {
		c.reply(s.serverNotice(c, "DCC is disabled on fugace: it would reveal your IP address"))
		return
	}
	for _, t := range targets {
		if !c.throttle(c.msgBucket) {
			return
		}
		if irc.IsChannel(t) {
			s.messageChannel(c, m.Command, t, text, fail)
		} else {
			s.messageNick(c, m.Command, t, text, fail)
		}
	}
}

func (s *Server) messageChannel(c *client, cmd, target, text string, fail func(string, ...string)) {
	key := irc.Fold(target)
	s.mu.RLock()
	ch := s.channels[key]
	if ch == nil {
		s.mu.RUnlock()
		fail(irc.ErrNoSuchChannel, target, "No such channel")
		return
	}
	if _, in := ch.members[c]; !in {
		s.mu.RUnlock()
		fail(irc.ErrCannotSendToChan, ch.name, "Cannot send to channel")
		return
	}
	recips := make([]*client, 0, len(ch.members))
	for r := range ch.members {
		if r != c || c.hasCapLocked(capEchoMessage) {
			recips = append(recips, r)
		}
	}
	mask, chName := c.maskLocked(), ch.name
	if storable(text) {
		// Under s.mu so that a concurrent DELCHAN cannot leave orphan entries.
		s.history.add(key, entry{at: s.opts.Now(), source: mask, command: cmd, text: text})
	}
	s.mu.RUnlock()

	s.fanout(c, recips, irc.Message{Source: mask, Command: cmd, Params: []string{chName, text}})
}

func (s *Server) messageNick(c *client, cmd, target, text string, fail func(string, ...string)) {
	s.mu.RLock()
	r := s.nicks[irc.Fold(target)]
	if r == nil || !r.registered.Load() {
		s.mu.RUnlock()
		fail(irc.ErrNoSuchNick, target, "No such nick/channel")
		return
	}
	recips := []*client{r}
	if r != c && c.hasCapLocked(capEchoMessage) {
		recips = append(recips, c)
	}
	mask, nick := c.maskLocked(), r.nick
	s.mu.RUnlock()
	s.fanout(c, recips, irc.Message{Source: mask, Command: cmd, Params: []string{nick, text}})
}

func (s *Server) cmdTopic(c *client, m irc.Message) {
	if len(m.Params) < 1 {
		s.needMore(c, "TOPIC")
		return
	}
	key := irc.Fold(m.Params[0])
	if len(m.Params) == 1 {
		s.mu.RLock()
		ch := s.channels[key]
		var name, topic string
		if ch != nil {
			name, topic = ch.name, ch.topic
		}
		s.mu.RUnlock()
		switch {
		case ch == nil:
			c.reply(s.numeric(c, irc.ErrNoSuchChannel, m.Params[0], "No such channel"))
		case topic == "":
			c.reply(s.numeric(c, irc.RplNoTopic, name, "No topic is set"))
		default:
			c.reply(s.numeric(c, irc.RplTopic, name, topic))
		}
		return
	}
	topic := irc.TruncateUTF8(m.Params[1], irc.TopicLen)
	s.mu.Lock()
	ch := s.channels[key]
	if ch == nil {
		s.mu.Unlock()
		c.reply(s.numeric(c, irc.ErrNoSuchChannel, m.Params[0], "No such channel"))
		return
	}
	if _, in := ch.members[c]; !in {
		s.mu.Unlock()
		c.reply(s.numeric(c, irc.ErrNotOnChannel, ch.name, "You're not on that channel"))
		return
	}
	ch.topic = topic
	recips, mask, chName := ch.memberList(), c.maskLocked(), ch.name
	s.mu.Unlock()
	s.fanout(c, recips, irc.Message{Source: mask, Command: "TOPIC", Params: []string{chName, topic}})
}

func (s *Server) cmdNames(c *client, m irc.Message) {
	targets := splitList(m.Param(0))
	if len(targets) == 0 {
		c.reply(s.numeric(c, irc.RplEndOfNames, "*", "End of /NAMES list"))
		return
	}
	if len(targets) > limits.MaxTargets {
		targets = targets[:limits.MaxTargets]
	}
	for _, t := range targets {
		s.mu.RLock()
		ch := s.channels[irc.Fold(t)]
		var names []string
		name := t
		if ch != nil {
			names, name = namesLocked(ch), ch.name
		}
		s.mu.RUnlock()
		if ch == nil {
			c.reply(s.numeric(c, irc.RplEndOfNames, t, "End of /NAMES list"))
			continue
		}
		s.sendNames(c, name, names)
	}
}

func (s *Server) cmdList(c *client, m irc.Message) {
	type row struct {
		name, topic string
		count       int
	}
	filter := make(map[string]bool)
	for _, t := range splitList(m.Param(0)) {
		filter[irc.Fold(t)] = true
	}
	s.mu.RLock()
	rows := make([]row, 0, len(s.channels))
	for key, ch := range s.channels {
		if len(filter) == 0 || filter[key] {
			rows = append(rows, row{ch.name, ch.topic, len(ch.members)})
		}
	}
	s.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	c.reply(s.numeric(c, irc.RplListStart, "Channel", "Users  Name"))
	for _, r := range rows {
		c.reply(s.numeric(c, irc.RplList, r.name, strconv.Itoa(r.count), r.topic))
	}
	c.reply(s.numeric(c, irc.RplListEnd, "End of /LIST"))
}

// --- queries: never an address, never an idle time ---

type whoRow struct {
	channel, user, nick string
	oper                bool
}

func (s *Server) cmdWho(c *client, m irc.Message) {
	mask := m.Param(0)
	var rows []whoRow
	s.mu.RLock()
	switch {
	case irc.IsChannel(mask):
		if ch := s.channels[irc.Fold(mask)]; ch != nil {
			for r := range ch.members {
				rows = append(rows, whoRow{ch.name, r.userName(), r.nick, r.oper})
			}
		}
	case irc.ValidNick(mask):
		if r := s.nicks[irc.Fold(mask)]; r != nil && r.registered.Load() {
			rows = append(rows, whoRow{"*", r.userName(), r.nick, r.oper})
		}
	}
	s.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].nick < rows[j].nick })
	for _, r := range rows {
		flags := "H"
		if r.oper {
			flags += "*"
			if r.channel != "*" {
				flags += "@"
			}
		}
		c.reply(s.numeric(c, irc.RplWhoReply, r.channel, r.user, HiddenHost, ServerName, r.nick, flags, "0 *"))
	}
	if mask == "" {
		mask = "*"
	}
	c.reply(s.numeric(c, irc.RplEndOfWho, mask, "End of /WHO list"))
}

func (s *Server) cmdWhois(c *client, m irc.Message) {
	if len(m.Params) == 0 {
		c.reply(s.numeric(c, irc.ErrNoNicknameGiven, "No nickname given"))
		return
	}
	arg := m.Params[len(m.Params)-1] // WHOIS [server] nick
	targets := splitList(arg)
	if len(targets) > limits.MaxTargets {
		targets = targets[:limits.MaxTargets]
	}
	for _, t := range targets {
		s.mu.RLock()
		r := s.nicks[irc.Fold(t)]
		if r == nil || !r.registered.Load() {
			s.mu.RUnlock()
			c.reply(s.numeric(c, irc.ErrNoSuchNick, t, "No such nick/channel"))
			continue
		}
		nick, user, oper := r.nick, r.userName(), r.oper
		// Only channels shared with the requester are disclosed.
		var common []string
		for key, ch := range r.channels {
			if _, ok := c.channels[key]; ok {
				if oper {
					common = append(common, "@"+ch.name)
				} else {
					common = append(common, ch.name)
				}
			}
		}
		s.mu.RUnlock()
		sort.Strings(common)
		c.reply(s.numeric(c, irc.RplWhoisUser, nick, user, HiddenHost, "*", "*"))
		if len(common) > 0 {
			c.reply(s.numeric(c, irc.RplWhoisChannels, nick, strings.Join(common, " ")))
		}
		c.reply(s.numeric(c, irc.RplWhoisServer, nick, ServerName, NetworkName))
		if oper {
			c.reply(s.numeric(c, irc.RplWhoisOperator, nick, "is a fugace admin"))
		}
	}
	c.reply(s.numeric(c, irc.RplEndOfWhois, arg, "End of /WHOIS list"))
}

func (s *Server) cmdMode(c *client, m irc.Message) {
	target := m.Param(0)
	if target == "" {
		s.needMore(c, "MODE")
		return
	}
	if irc.IsChannel(target) {
		s.mu.RLock()
		ch := s.channels[irc.Fold(target)]
		name := target
		if ch != nil {
			name = ch.name
		}
		s.mu.RUnlock()
		if ch == nil {
			c.reply(s.numeric(c, irc.ErrNoSuchChannel, target, "No such channel"))
			return
		}
		if len(m.Params) == 1 {
			c.reply(s.numeric(c, irc.RplChannelModeIs, name, "+n"))
			return
		}
		switch strings.TrimPrefix(m.Params[1], "+") {
		case "b":
			c.reply(s.numeric(c, irc.RplEndOfBanList, name, "End of channel ban list"))
		case "e":
			c.reply(s.numeric(c, irc.RplEndOfExceptList, name, "End of channel exception list"))
		case "I":
			c.reply(s.numeric(c, irc.RplEndOfInviteList, name, "End of channel invite list"))
		default:
			c.reply(s.numeric(c, irc.ErrChanOPrivsNeeded, name, "Channel modes cannot be changed"))
		}
		return
	}
	if irc.Fold(target) != irc.Fold(c.nick) {
		s.mu.RLock()
		_, exists := s.nicks[irc.Fold(target)]
		s.mu.RUnlock()
		if exists {
			c.reply(s.numeric(c, irc.ErrUsersDontMatch, "Can't change mode for other users"))
		} else {
			c.reply(s.numeric(c, irc.ErrNoSuchNick, target, "No such nick/channel"))
		}
		return
	}
	umode := "+"
	if c.oper {
		umode = "+o"
	}
	c.reply(s.numeric(c, irc.RplUmodeIs, umode))
}

// --- administration (ADR 0003) ---

func (s *Server) cmdOper(c *client, m irc.Message) (bool, string) {
	if len(m.Params) < 2 {
		s.needMore(c, "OPER")
		return false, ""
	}
	if s.opts.AdminPassword == "" {
		c.reply(s.numeric(c, irc.ErrNoOperHost, "No admin is configured on this server"))
		return false, ""
	}
	if c.oper {
		c.reply(s.numeric(c, irc.RplYoureOper, "You are already a fugace admin"))
		return false, ""
	}
	if subtle.ConstantTimeCompare([]byte(m.Params[1]), []byte(s.opts.AdminPassword)) != 1 {
		c.operFailures++
		if !c.wait(s.opts.OperFailureDelay) {
			return false, ""
		}
		c.reply(s.numeric(c, irc.ErrPasswdMismatch, "Password incorrect"))
		if c.operFailures >= limits.MaxOperFailures {
			return true, "Too many failed OPER attempts"
		}
		return false, ""
	}
	type chanMembers struct {
		name    string
		members []*client
	}
	s.mu.Lock()
	c.oper = true
	nick := c.nick
	var chans []chanMembers
	for _, ch := range c.channels {
		chans = append(chans, chanMembers{ch.name, ch.memberList()})
	}
	s.mu.Unlock()

	c.reply(s.numeric(c, irc.RplYoureOper, "You are now a fugace admin"))
	c.reply(irc.Message{Source: ServerName, Command: "MODE", Params: []string{nick, "+o"}})
	for _, cm := range chans {
		s.fanout(c, cm.members, irc.Message{Source: ServerName, Command: "MODE", Params: []string{cm.name, "+o", nick}})
	}
	return false, ""
}

func (s *Server) requireOper(c *client, cmd string, m irc.Message) bool {
	if !c.oper {
		c.reply(s.numeric(c, irc.ErrNoPrivileges, "Permission denied: you are not the admin"))
		return false
	}
	if m.Param(0) == "" {
		s.needMore(c, cmd)
		return false
	}
	return true
}

func (s *Server) cmdDelChan(c *client, m irc.Message) {
	if !s.requireOper(c, "DELCHAN", m) {
		return
	}
	reason := m.Param(1)
	if reason == "" {
		reason = "Channel deleted"
	}
	key := irc.Fold(m.Params[0])
	type kicked struct {
		c    *client
		nick string
	}
	s.mu.Lock()
	ch := s.channels[key]
	if ch == nil {
		s.mu.Unlock()
		c.reply(s.numeric(c, irc.ErrNoSuchChannel, m.Params[0], "No such channel"))
		return
	}
	delete(s.channels, key)
	var members []kicked
	for r := range ch.members {
		delete(r.channels, key)
		members = append(members, kicked{r, r.nick})
	}
	mask, chName := c.maskLocked(), ch.name
	s.history.clear(key)
	s.mu.Unlock()

	for _, k := range members {
		s.fanout(c, []*client{k.c}, irc.Message{Source: mask, Command: "KICK", Params: []string{chName, k.nick, reason}})
	}
	c.reply(s.serverNotice(c, "Channel "+chName+" and its history were deleted"))
}

func (s *Server) cmdClearHistory(c *client, m irc.Message) {
	if !s.requireOper(c, "CLEARHISTORY", m) {
		return
	}
	key := irc.Fold(m.Params[0])
	s.mu.RLock()
	ch := s.channels[key]
	name := m.Params[0]
	if ch != nil {
		name = ch.name
	}
	s.mu.RUnlock()
	if ch == nil {
		c.reply(s.numeric(c, irc.ErrNoSuchChannel, name, "No such channel"))
		return
	}
	s.history.clear(key)
	c.reply(s.serverNotice(c, "History of "+name+" cleared"))
}
