package irc

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParse checks that Parse never panics, that its output is valid UTF-8
// and that a parsed message survives a serialize/parse round trip.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"NICK alice\r\n",
		":n!u@h PRIVMSG #a :hello world",
		`@time=2026-01-01T00:00:00Z;+k=a\sb\:c PRIVMSG #a ::)`,
		"CMD 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16",
		"PRIVMSG #a :\x01DCC SEND x 1 2 3\x01",
		"@;; PING",
		"\xff\xfe PING",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		m, err := Parse(line)
		if err != nil {
			return
		}
		if !utf8.ValidString(m.Source) {
			t.Fatalf("invalid UTF-8 source")
		}
		for _, p := range m.Params {
			if !utf8.ValidString(p) {
				t.Fatalf("invalid UTF-8 param %q", p)
			}
		}
		for _, tg := range m.Tags {
			if !utf8.ValidString(tg.Value) {
				t.Fatalf("invalid UTF-8 tag")
			}
		}
		b, err := m.Bytes()
		if errors.Is(err, ErrTagsTooLong) {
			return // replacement characters grew the tags; the server never re-emits client tags
		}
		if err != nil {
			// Replacement characters may push a valid line past 512 bytes:
			// Bytes truncates, so only a structural error is a bug.
			t.Fatalf("Bytes(%#v): %v", m, err)
		}
		back, err := Parse(string(b))
		if err != nil {
			t.Fatalf("reparse %q: %v", b, err)
		}
		// Bytes may only shorten the last parameter, and only to a prefix.
		if n := len(m.Params) - 1; n >= 0 && n < len(back.Params) && back.Params[n] != m.Params[n] {
			if !strings.HasPrefix(m.Params[n], back.Params[n]) {
				t.Fatalf("last param altered: %q -> %q", m.Params[n], back.Params[n])
			}
			back.Params[n] = m.Params[n]
		}
		if !reflect.DeepEqual(normalize(back), normalize(m)) {
			t.Fatalf("round trip:\n %#v\n %#v", m, back)
		}
	})
}

func normalize(m Message) Message {
	if len(m.Tags) == 0 {
		m.Tags = nil
	}
	if len(m.Params) == 0 {
		m.Params = nil
	}
	return m
}
