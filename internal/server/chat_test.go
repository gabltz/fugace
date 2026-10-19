package server_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabltz/fugace/internal/files"
	"github.com/gabltz/fugace/internal/server"
)

func TestNickCollisionAndCase(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := register(t, addr, "Alice")
	b := dial(t, addr)
	b.send("NICK ALICE")
	b.expect(" 433 * ALICE :Nickname is already in use")
	// T10: look-alike and non-ASCII nicks are refused outright.
	b.send("NICK аlice")
	b.expect(" 432 ")
	b.send("NICK bob", "USER b 0 * :b")
	b.expect(" 001 bob ")
	join(a, "Alice", "#general")
	join(b, "bob", "#general")
	a.send("NICK alice") // case change only
	b.expect(":Alice!~user@fugace NICK alice")
	a.send("NICK bob")
	a.expect(" 433 alice bob ")
}

func TestChannelChat(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := register(t, addr, "alice")
	b := register(t, addr, "bob")
	join(a, "alice", "#general")
	b.send("JOIN #GENERAL")
	a.expect(":bob!~user@fugace JOIN #general")
	names := b.expect(" 353 bob = #general ")
	if !strings.Contains(names, "alice") || !strings.Contains(names, "bob") {
		t.Fatalf("names = %q", names)
	}
	b.expect(" 366 bob #general ")

	a.send("PRIVMSG #general :hello bob")
	b.expect(":alice!~user@fugace PRIVMSG #general :hello bob")
	a.expectNone("hello bob", 100*time.Millisecond) // no echo without the cap

	b.send("PRIVMSG alice :\x01ACTION waves\x01")
	a.expect(":bob!~user@fugace PRIVMSG alice :\x01ACTION waves\x01")
	b.send("NOTICE alice :psst")
	a.expect(":bob!~user@fugace NOTICE alice :psst")

	b.send("TOPIC #general :new topic")
	a.expect(":bob!~user@fugace TOPIC #general :new topic")
	b.expect("TOPIC #general :new topic")
	a.send("TOPIC #general")
	a.expect(" 332 alice #general :new topic")

	b.send("PART #general :later")
	a.expect(":bob!~user@fugace PART #general :later")
	b.send("PRIVMSG #general :still here?")
	b.expect(" 404 bob #general ")
	b.send("NOTICE #general :silent")
	b.expectNone(" 404 ", 100*time.Millisecond) // NOTICE never errors

	a.send("QUIT :bye all")
	a.expect("ERROR :Closing Link: fugace (Quit: bye all)")
	a.expectClosed()
	noIP(t, a, b)
}

func TestQuitBroadcast(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := register(t, addr, "alice")
	b := register(t, addr, "bob")
	join(a, "alice", "#general")
	join(b, "bob", "#general")
	b.send("QUIT :see you")
	a.expect(":bob!~user@fugace QUIT :Quit: see you")
	c := register(t, addr, "carol")
	_ = c.conn.Close()
	a.expectNone("carol", 100*time.Millisecond) // no common channel
}

func TestEchoAndServerTime(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := register(t, addr, "alice", "echo-message", "server-time")
	join(a, "alice", "#general")
	a.send("PRIVMSG #general :echoed")
	line := a.expect("PRIVMSG #general :echoed")
	if !strings.HasPrefix(line, "@time=") {
		t.Fatalf("missing server-time: %q", line)
	}
}

func TestErrors(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := register(t, addr, "alice")
	tests := []struct{ send, want string }{
		{"FOO bar", " 421 alice FOO :Unknown command"},
		{"JOIN #nope", " 403 alice #nope :No such channel"},
		{"JOIN nochan", " 476 alice nochan "},
		{"JOIN", " 461 alice JOIN "},
		{"PART #general", " 442 alice #general "},
		{"PART #nope", " 403 alice #nope "},
		{"PRIVMSG", " 411 alice "},
		{"PRIVMSG bob", " 412 alice "},
		{"PRIVMSG ghost :hi", " 401 alice ghost "},
		{"PRIVMSG a,b,c,d,e :hi", " 407 alice a,b,c,d,e "},
		{"PRIVMSG #general :hi", " 404 alice #general "},
		{"TOPIC #general :x", " 442 alice #general "},
		{"TOPIC #nope", " 403 alice #nope "},
		{"USER a b c d", " 462 alice "},
		{"PASS x", " 462 alice "},
		{"PING", " 409 alice "},
		{"MODE", " 461 alice MODE "},
		{"MODE #nope", " 403 alice #nope "},
		{"MODE #general +k key", " 482 alice #general "},
		{"MODE ghost", " 401 alice ghost "},
		{"DELCHAN #general", " 481 alice "},
		{"CLEARHISTORY #general", " 481 alice "},
		{"OPER admin", " 461 alice OPER "},
		{"WHOIS", " 431 alice "},
		{"WHOIS ghost", " 401 alice ghost "},
		{"PRIVMSG #general :" + strings.Repeat("x", 600), " 417 alice "},
	}
	for _, tt := range tests {
		a.send(tt.send)
		a.expect(tt.want)
	}
}

func TestQueries(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := register(t, addr, "alice")
	b := register(t, addr, "bob")
	join(a, "alice", "#general")
	join(b, "bob", "#general")
	tests := []struct{ send, want string }{
		{"WHO #general", " 352 alice #general ~user fugace fugace bob H :0 *"},
		{"WHO bob", " 352 alice * ~user fugace fugace bob H :0 *"},
		{"WHO *", " 315 alice * :End of /WHO list"},
		{"WHOIS bob", " 311 alice bob ~user fugace * :*"},
		{"WHOIS bob", " 319 alice bob :#general"},
		{"WHOIS fugace bob", " 318 alice bob "},
		{"NAMES #general", " 353 alice = #general :alice bob"},
		{"NAMES", " 366 alice * "},
		{"LIST", " 322 alice #general 2 :"},
		{"LIST #nope", " 323 alice "},
		{"MODE #general", " 324 alice #general :+n"},
		{"MODE #general b", " 368 alice #general "},
		{"MODE #general +e", " 349 alice #general "},
		{"MODE #general I", " 347 alice #general "},
		{"MODE alice +i", " 221 alice :+"},
		{"MODE bob", " 502 alice "},
		{"MOTD", " 422 alice "},
		{"LUSERS", " 251 alice :There are 2 users"},
		{"VERSION", " 351 alice fugace-test fugace :"},
		{"TIME", " 391 alice fugace :"},
		{"TAGMSG #general", ""},
		{"PONG :x", ""},
	}
	for _, tt := range tests {
		a.send(tt.send)
		if tt.want != "" {
			a.expect(tt.want)
		}
	}
	// T2: no idle time and no address in WHOIS.
	a.send("WHOIS bob")
	deadline := " 318 alice bob "
	for {
		l := a.expect(" ")
		if strings.Contains(l, " 317 ") || strings.Contains(l, " 338 ") || strings.Contains(l, " 378 ") {
			t.Fatalf("WHOIS discloses idle/host: %q", l)
		}
		if strings.Contains(l, deadline) {
			break
		}
	}
	noIP(t, a, b)
}

func TestWhoisOnlyCommonChannels(t *testing.T) {
	opts := baseOpts()
	opts.Channels = []string{"#general", "#secret"}
	addr, _ := start(t, opts)
	a := register(t, addr, "alice")
	b := register(t, addr, "bob")
	join(b, "bob", "#general")
	join(b, "bob", "#secret")
	join(a, "alice", "#general")
	a.send("WHOIS bob")
	l := a.expect(" 319 alice bob ")
	if strings.Contains(l, "#secret") {
		t.Fatalf("WHOIS discloses a non-shared channel: %q", l)
	}
}

func TestDCCBlocked(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := register(t, addr, "alice")
	b := register(t, addr, "bob")
	a.send("PRIVMSG bob :\x01DCC SEND file 2130706433 5000 10\x01")
	a.expect("NOTICE alice :DCC is disabled")
	a.send("NOTICE bob :\x01dcc chat chat 2130706433 5000\x01")
	a.expect("NOTICE alice :DCC is disabled")
	a.send("PRIVMSG bob :after")
	if l := b.expect("PRIVMSG bob"); !strings.HasSuffix(l, ":after") {
		t.Fatalf("DCC reached the peer: %q", l)
	}
}

func TestHistoryReplay(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := register(t, addr, "alice")
	join(a, "alice", "#general")
	a.send("PRIVMSG #general :first", "PRIVMSG #general :\x01ACTION waves\x01", "PRIVMSG #general :\x01VERSION\x01", "NOTICE #general :second")
	a.send("PING :sync")
	a.expect("PONG fugace :sync")

	plain := register(t, addr, "bob")
	join(plain, "bob", "#general")
	l := plain.expect("PRIVMSG #general :[")
	if !strings.HasPrefix(l, ":alice!~user@fugace PRIVMSG #general :[") || !strings.HasSuffix(l, "] first") {
		t.Fatalf("replay = %q", l)
	}
	l = plain.expect("ACTION")
	if !strings.Contains(l, "\x01ACTION [") || !strings.HasSuffix(l, "] waves\x01") {
		t.Fatalf("action replay = %q", l)
	}
	plain.expect("NOTICE #general :[")

	tagged := register(t, addr, "carol", "server-time")
	join(tagged, "carol", "#general")
	l = tagged.expect("PRIVMSG #general :first")
	if !strings.HasPrefix(l, "@time=") {
		t.Fatalf("tagged replay = %q", l)
	}
	tagged.expectNone("VERSION", 200*time.Millisecond) // CTCP queries are never replayed
}

func TestAdmin(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := register(t, addr, "admin")
	u := register(t, addr, "user")
	join(a, "admin", "#general")
	join(u, "user", "#general")

	a.send("OPER admin wrong")
	a.expect(" 464 admin ")
	oper(a)
	u.expect(":fugace MODE #general +o :admin")
	a.send("MODE admin")
	a.expect(" 221 admin :+o")

	a.send("JOIN #team")
	a.expect(" 366 admin #team ")
	u.send("JOIN #team")
	u.expect(":user!~user@fugace JOIN #team")
	names := u.expect(" 353 user = #team ")
	if !strings.Contains(names, "@admin") {
		t.Fatalf("admin prefix missing: %q", names)
	}
	u.send("PRIVMSG #team :to be cleared")
	a.expect("to be cleared")
	a.send("CLEARHISTORY #team")
	a.expect("History of #team cleared")
	u.send("WHOIS admin")
	u.expect(" 313 user admin ")

	// Empty channels persist with their history.
	u.send("PRIVMSG #team :kept", "PART #team", "PART #team")
	u.expect(" 442 user #team ")
	a.send("PART #team")
	u.send("JOIN #team")
	l := u.expect("PRIVMSG #team :[")
	if !strings.HasSuffix(l, "] kept") {
		t.Fatalf("history after clear = %q", l)
	}

	a.send("DELCHAN #team :cleanup")
	u.expect(":admin!~user@fugace KICK #team user :cleanup")
	a.expect("Channel #team and its history were deleted")
	u.send("JOIN #team")
	u.expect(" 403 user #team ")
	a.send("DELCHAN #nope")
	a.expect(" 403 admin #nope ")
}

func TestOperBruteForceDisconnects(t *testing.T) {
	addr, _ := start(t, baseOpts())
	c := register(t, addr, "mallory")
	for i := range 5 {
		c.send(fmt.Sprintf("OPER admin guess%d", i))
		c.expect(" 464 mallory ")
	}
	c.expect("ERROR :Closing Link: fugace (Too many failed OPER attempts)")
	c.expectClosed()
}

func TestNoAdminConfigured(t *testing.T) {
	opts := baseOpts()
	opts.AdminPassword = ""
	addr, _ := start(t, opts)
	c := register(t, addr, "x")
	c.send("OPER admin anything")
	c.expect(" 491 x ")
}

func TestServerPassword(t *testing.T) {
	opts := baseOpts()
	opts.Password = "letmein"
	addr, _ := start(t, opts)
	bad := dial(t, addr)
	bad.send("PASS nope", "NICK bad", "USER b 0 * :b")
	bad.expect(" 464 bad ")
	bad.expectClosed()
	none := dial(t, addr)
	none.send("NICK none", "USER b 0 * :b")
	none.expect(" 464 none ")
	ok := dial(t, addr)
	ok.send("PASS letmein", "NICK good", "USER b 0 * :b")
	ok.expect(" 001 good ")
}

func TestPingTimeout(t *testing.T) {
	opts := baseOpts()
	opts.PingInterval = 150 * time.Millisecond
	opts.PingTimeout = 400 * time.Millisecond
	opts.TickInterval = 20 * time.Millisecond
	addr, _ := start(t, opts)
	a := register(t, addr, "alice")
	b := register(t, addr, "bob")
	join(a, "alice", "#general")
	join(b, "bob", "#general")

	// bob answers pings and stays; alice does not.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if l, ok := b.readLine(50 * time.Millisecond); ok && strings.HasPrefix(l, "PING ") {
				b.send("PONG " + strings.TrimPrefix(l, "PING "))
			}
		}
	})
	a.expect("PING fugace")
	a.expect("ERROR :Closing Link: fugace (Ping timeout)")
	a.expectClosed()
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
	found := false
	for _, l := range b.lines() {
		if strings.Contains(l, ":alice!~user@fugace QUIT :Ping timeout") {
			found = true
		}
	}
	if !found {
		t.Fatalf("bob did not see the ping timeout: %q", b.lines())
	}
}

func TestRegistrationTimeout(t *testing.T) {
	opts := baseOpts()
	opts.RegistrationTimeout = 150 * time.Millisecond
	opts.TickInterval = 20 * time.Millisecond
	addr, _ := start(t, opts)
	c := dial(t, addr)
	c.send("NICK slow")
	c.expect("ERROR :Closing Link: fugace (Registration timeout)")
	c.expectClosed()
}

func TestPerSourceLimit(t *testing.T) {
	opts := baseOpts()
	opts.MaxConnsPerSource = 2
	addr, srv := start(t, opts)
	register(t, addr, "a1")
	register(t, addr, "a2")
	c := dial(t, addr)
	c.expect("ERROR :Closing Link: Too many connections")
	c.expectClosed()
	if n := srv.ConnCount(); n != 2 {
		t.Fatalf("conn count = %d", n)
	}
}

func TestFakeLag(t *testing.T) {
	opts := baseOpts()
	r := *fastRates()
	r.Message = server.Rate{Burst: 2, Interval: 200 * time.Millisecond}
	opts.Rates = &r
	addr, _ := start(t, opts)
	a := register(t, addr, "alice")
	b := register(t, addr, "bob")
	begin := time.Now()
	a.send("PRIVMSG bob :1", "PRIVMSG bob :2", "PRIVMSG bob :3", "PRIVMSG bob :4")
	b.expect("PRIVMSG bob :4")
	if el := time.Since(begin); el < 350*time.Millisecond {
		t.Fatalf("4 messages with burst 2 at 5/s took only %v", el)
	}
}

func TestShutdownNotifiesClients(t *testing.T) {
	addr, srv := start(t, baseOpts())
	a := register(t, addr, "alice")
	go func() { _ = srv.Shutdown(t.Context()) }()
	a.expect("NOTICE alice :Server shutting down")
	a.expect("ERROR :Closing Link: fugace (Server shutting down)")
	a.expectClosed()
}

// TestManyClients runs N clients in one channel: everyone receives every
// other client's message (ROADMAP M1 integration scenario).
func TestManyClients(t *testing.T) {
	const n = 40
	opts := baseOpts()
	opts.MaxConnsPerSource = n // every test client comes from 127.0.0.1
	addr, _ := start(t, opts)
	clients := make([]*tc, n)
	for i := range clients {
		nick := fmt.Sprintf("u%02d", i)
		clients[i] = register(t, addr, nick)
		join(clients[i], nick, "#general")
	}
	for i, c := range clients {
		c.send(fmt.Sprintf("PRIVMSG #general :hello from u%02d", i))
	}
	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i, c := range clients {
		wg.Go(func() {
			got := 0
			deadline := time.Now().Add(5 * time.Second)
			for got < n-1 && time.Now().Before(deadline) {
				if l, ok := c.readLine(time.Until(deadline)); ok && strings.Contains(l, " PRIVMSG #general :hello from ") {
					got++
				}
			}
			if got != n-1 {
				errs <- fmt.Sprintf("u%02d got %d messages", i, got)
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	noIP(t, clients...)
}

// T9: the upload token is announced in 005 and dies with the connection.
func TestUploadTokenLifecycle(t *testing.T) {
	tokens := files.NewTokens(3, 10*time.Second)
	opts := baseOpts()
	opts.Uploads = tokens
	addr, _ := start(t, opts)
	c := dial(t, addr)
	c.send("NICK up", "USER u 0 * :u")
	line := c.expect(" 005 up ")
	_, after, ok := strings.Cut(line, server.UploadISupport+"=")
	if !ok {
		t.Fatalf("no upload token in %q", line)
	}
	token, _, _ := strings.Cut(after, " ")
	if _, ok := tokens.Owner(token); !ok {
		t.Fatal("announced token is not valid")
	}
	other := register(t, addr, "other")
	for _, l := range other.lines() {
		if strings.Contains(l, token) {
			t.Fatal("token shared between connections")
		}
	}
	c.send("QUIT :bye")
	c.expectClosed()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok := tokens.Owner(token); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("token still valid after QUIT")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Unregistered connections get no token at all.
	u := dial(t, addr)
	u.send("NICK late")
	u.expectNone("UPLOAD", 200*time.Millisecond)
}
