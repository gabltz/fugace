package server_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabltz/fugace/internal/server"
)

const (
	keyA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	keyB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeAccount records the account hooks.
type fakeAccount struct {
	mu       sync.Mutex
	versions map[string]int
	cleared  []string
	gone     []string
}

func (f *fakeAccount) AvatarVersion(o string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.versions[o]
}

func (f *fakeAccount) Cleared(o string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, o)
}

func (f *fakeAccount) Gone(o string) { f.mu.Lock(); defer f.mu.Unlock(); f.gone = append(f.gone, o) }

// webLike registers like the web client: KEY first, then the v0.3 caps.
func webLike(t *testing.T, addr, nick, key string) *tc {
	t.Helper()
	c := dial(t, addr)
	c.send("CAP LS 302")
	if key != "" {
		c.send("KEY " + key)
	}
	c.send("NICK "+nick, "USER web 0 * :web", "CAP REQ :message-tags server-time echo-message draft/message-redaction fugace/avatar", "CAP END")
	c.expect(" 422 " + nick + " ")
	return c
}

func msgidOf(t *testing.T, line string) string {
	t.Helper()
	for _, tag := range strings.Split(strings.SplitN(strings.TrimPrefix(line, "@"), " ", 2)[0], ";") {
		if v, ok := strings.CutPrefix(tag, "msgid="); ok {
			return v
		}
	}
	t.Fatalf("no msgid in %q", line)
	return ""
}

func TestMsgidAndOwnTag(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := webLike(t, addr, "alice", keyA)
	b := webLike(t, addr, "bob", keyB)
	join(a, "alice", "#general")
	join(b, "bob", "#general")
	a.send("PRIVMSG #general :hello")
	mine := a.expect("PRIVMSG #general :hello")
	theirs := b.expect("PRIVMSG #general :hello")
	if !strings.Contains(mine, ";fugace/own ") || strings.Contains(theirs, "fugace/own") {
		t.Fatalf("own tag: author=%q other=%q", mine, theirs)
	}
	if msgidOf(t, mine) != msgidOf(t, theirs) {
		t.Fatal("different msgids for the same message")
	}
	// Plain IRC clients get no tags at all.
	p := register(t, addr, "plain")
	join(p, "plain", "#general")
	if l := p.expect("PRIVMSG #general :["); strings.HasPrefix(l, "@") {
		t.Fatalf("tags sent to a client without message-tags: %q", l)
	}
	// The owner keeps the own tag in the replayed history, even from a new
	// connection with the same key.
	again := webLike(t, addr, "alice2", keyA)
	join(again, "alice2", "#general")
	if l := again.expect("PRIVMSG #general :hello"); !strings.Contains(l, "fugace/own") {
		t.Fatalf("own tag lost across reconnections: %q", l)
	}
}

func TestRedactChannel(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := webLike(t, addr, "alice", keyA)
	b := webLike(t, addr, "bob", keyB)
	plain := register(t, addr, "plain")
	for _, c := range []struct {
		c    *tc
		nick string
	}{{a, "alice"}, {b, "bob"}, {plain, "plain"}} {
		join(c.c, c.nick, "#general")
	}
	a.send("PRIVMSG #general :oops")
	id := msgidOf(t, a.expect("PRIVMSG #general :oops"))
	b.expect("PRIVMSG #general :oops")

	// Someone else cannot delete it.
	b.send("REDACT #general " + id)
	b.expect("FAIL REDACT REDACT_FORBIDDEN #general " + id)
	b.send("REDACT #general nope")
	b.expect("FAIL REDACT UNKNOWN_MSGID #general nope")

	// The author can, everyone with the cap is told, and history forgets it.
	a.send("REDACT #general " + id + " :typo")
	a.expect(":alice!~user@fugace REDACT #general " + id + " :typo")
	b.expect(":alice!~user@fugace REDACT #general " + id + " :typo")
	plain.expectNone("REDACT", 200*time.Millisecond)
	late := webLike(t, addr, "late", "")
	join(late, "late", "#general")
	late.expectNone("oops", 300*time.Millisecond)
	a.send("REDACT #general " + id)
	a.expect("FAIL REDACT UNKNOWN_MSGID")
}

func TestRedactPrivate(t *testing.T) {
	addr, _ := start(t, baseOpts())
	a := webLike(t, addr, "alice", keyA)
	b := webLike(t, addr, "bob", keyB)
	a.send("PRIVMSG bob :secret")
	id := msgidOf(t, b.expect("PRIVMSG bob :secret"))
	b.send("REDACT alice " + id)
	b.expect("FAIL REDACT REDACT_FORBIDDEN")
	a.send("REDACT bob " + id)
	b.expect(":alice!~user@fugace REDACT bob :" + id)
	a.expect(":alice!~user@fugace REDACT bob :" + id)
}

func TestClearAccount(t *testing.T) {
	acct := &fakeAccount{versions: map[string]int{}}
	opts := baseOpts()
	opts.Account = acct
	addr, srv := start(t, opts)
	a := webLike(t, addr, "alice", keyA)
	b := webLike(t, addr, "bob", keyB)
	join(a, "alice", "#general")
	join(b, "bob", "#general")
	a.send("PRIVMSG #general :one", "PRIVMSG #general :two", "PRIVMSG bob :pm")
	id1 := msgidOf(t, b.expect("PRIVMSG #general :one"))
	id2 := msgidOf(t, b.expect("PRIVMSG #general :two"))
	idPM := msgidOf(t, b.expect("PRIVMSG bob :pm"))
	b.send("PRIVMSG #general :bob stays")
	b.expect("bob stays")
	owner, _ := srv.OwnerOf("alice")

	a.send("CLEAR-ACCOUNT")
	a.expect("NOTICE alice :Your messages, files and picture were deleted")
	a.expect("ERROR :Closing Link: fugace (Account cleared)")
	for _, id := range []string{id1, id2} {
		b.expect("REDACT #general :" + id)
	}
	b.expect("REDACT bob :" + idPM)
	b.expect(":alice!~user@fugace QUIT :Account cleared")
	acct.mu.Lock()
	cleared := strings.Join(acct.cleared, ",")
	acct.mu.Unlock()
	if cleared != owner {
		t.Fatalf("Cleared(%q), want %q", cleared, owner)
	}
	late := webLike(t, addr, "late", "")
	join(late, "late", "#general")
	late.expect("bob stays")
	for _, l := range late.lines() {
		if strings.Contains(l, ":one") || strings.Contains(l, ":two") {
			t.Fatalf("cleared message replayed: %q", l)
		}
	}
	// The IRC spelling works too.
	c := webLike(t, addr, "carol", "")
	c.send("CLEARACCOUNT")
	c.expect("Account cleared")
}

func TestKeyRules(t *testing.T) {
	addr, srv := start(t, baseOpts())
	c := dial(t, addr)
	c.send("KEY short")
	c.expect("FAIL KEY INVALID_KEY")
	c.send("KEY "+keyA, "NICK k", "USER k 0 * :k")
	c.expect(" 001 k ")
	c.send("KEY " + keyB)
	c.expect(" 462 k ")
	o1, _ := srv.OwnerOf("k")
	d := webLike(t, addr, "k2", keyA)
	_ = d
	o2, _ := srv.OwnerOf("k2")
	if o1 != o2 || strings.Contains(o1, keyA) {
		t.Fatalf("owners %q %q", o1, o2)
	}
	p := register(t, addr, "p")
	_ = p
	o3, _ := srv.OwnerOf("p")
	if o3 == o1 || o3 == "" {
		t.Fatal("connection identity")
	}
}

func TestAvatarNotifications(t *testing.T) {
	acct := &fakeAccount{versions: map[string]int{}}
	opts := baseOpts()
	opts.Account = acct
	addr, srv := start(t, opts)
	a := webLike(t, addr, "alice", keyA)
	join(a, "alice", "#general")
	owner, _ := srv.OwnerOf("alice")
	acct.mu.Lock()
	acct.versions[owner] = 7
	acct.mu.Unlock()
	b := webLike(t, addr, "bob", keyB)
	b.send("JOIN #general")
	b.expect(":alice!~user@fugace AVATAR 7")
	plain := register(t, addr, "plain")
	join(plain, "plain", "#general")
	srv.AvatarChanged(owner)
	b.expect(":alice!~user@fugace AVATAR 7")
	a.expect(":alice!~user@fugace AVATAR 7")
	plain.expectNone("AVATAR", 200*time.Millisecond)
	// Last connection gone: the picture is dropped.
	a.send("QUIT")
	b.expect("QUIT")
	time.Sleep(50 * time.Millisecond)
	acct.mu.Lock()
	gone := strings.Join(acct.gone, ",")
	acct.mu.Unlock()
	if !strings.Contains(gone, owner) {
		t.Fatalf("Gone not called for %q: %q", owner, gone)
	}
}

var _ server.AccountHooks = (*fakeAccount)(nil)
