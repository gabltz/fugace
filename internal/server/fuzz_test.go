package server

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gabltz/fugace/internal/irc"
)

// FuzzDispatch drives every command handler with arbitrary lines, from an
// unregistered and from an admin client. dispatch is called directly so
// that a panic is not hidden by the per-connection recover (T8).
func FuzzDispatch(f *testing.F) {
	for _, s := range []string{
		"NICK a\nUSER a 0 * :a\nJOIN #general\nPRIVMSG #general :hi",
		"CAP LS 302\nCAP REQ :-server-time echo-message\nCAP END\nCAP LIST",
		"OPER x pw\nJOIN #new,#general,0\nDELCHAN #new :r\nCLEARHISTORY #general",
		"WHO #general\nWHOIS a,b,c,d,e\nNAMES #general,#x\nLIST #general\nMODE #general b\nMODE a +i",
		"TOPIC #general :t\nPART #general :bye\nPRIVMSG a,#general,b :\x01DCC SEND\x01\nQUIT :x",
		"NICK [\\]^_`{|}\nNICK -\nPASS\nPING\nTIME\nVERSION\nLUSERS\nMOTD",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		r := Rate{Burst: 1 << 20, Interval: time.Microsecond}
		s, err := New(Options{
			Channels: []string{"#general"}, HistoryMaxBytes: 4096, AdminPassword: "pw-pw-pw",
			Rates: &Rates{r, r, r, r}, OperFailureDelay: time.Microsecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		admin := s.attach(t, "pw-pw-pw")
		user := s.attach(t, "")
		for i, line := range strings.Split(input, "\n") {
			m, err := irc.Parse(line)
			if err != nil {
				continue
			}
			c := user
			if i%2 == 1 {
				c = admin
			}
			if quit, _ := s.dispatch(c, m); quit {
				return
			}
		}
	})
}

// attach registers a client backed by a discarding connection.
func (s *Server) attach(t *testing.T, operPass string) *client {
	t.Helper()
	conn := newFakeConn(false)
	c := newClient(s, conn, ConnInfo{Addr: netip.MustParseAddr("192.0.2.9")})
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	go c.writeLoop()
	t.Cleanup(func() { s.disconnect(c, "done"); <-c.writerDone })
	nick := "n" + strings.Repeat("x", len(s.clients))
	for _, l := range []string{"NICK " + nick, "USER u 0 * :u"} {
		m, _ := irc.Parse(l)
		s.dispatch(c, m)
	}
	if operPass != "" {
		m, _ := irc.Parse("OPER admin " + operPass)
		s.dispatch(c, m)
	}
	return c
}
