package irc

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		line string
		want Message
		err  error
	}{
		{"NICK alice\r\n", Message{Command: "NICK", Params: []string{"alice"}}, nil},
		{"nick alice\n", Message{Command: "NICK", Params: []string{"alice"}}, nil},
		{":src PRIVMSG #a :hello world", Message{Source: "src", Command: "PRIVMSG", Params: []string{"#a", "hello world"}}, nil},
		{"PRIVMSG #a ::)", Message{Command: "PRIVMSG", Params: []string{"#a", ":)"}}, nil},
		{"TOPIC #a :", Message{Command: "TOPIC", Params: []string{"#a", ""}}, nil},
		{"USER  u   0  * :Real Name ", Message{Command: "USER", Params: []string{"u", "0", "*", "Real Name "}}, nil},
		{"PING", Message{Command: "PING"}, nil},
		{"001 x :hi", Message{Command: "001", Params: []string{"x", "hi"}}, nil},
		{
			`@time=2026-01-01T00:00:00Z;+typing=active;a\b;example.com/k=a\sb\:c\\d\ :s CMD`,
			Message{
				Tags: []Tag{
					{"time", "2026-01-01T00:00:00Z"},
					{"+typing", "active"},
					{"example.com/k", `a b;c\d`},
				},
				Source: "s", Command: "CMD",
			},
			ErrInvalidTags, // a\b is not a valid key
		},
		{
			`@time=x;example.com/k=a\sb\:c\\d\ :s CMD`,
			Message{
				Tags:   []Tag{{"time", "x"}, {"example.com/k", `a b;c\d`}},
				Source: "s", Command: "CMD",
			},
			nil,
		},
		{"@a=1 PING", Message{Tags: []Tag{{"a", "1"}}, Command: "PING"}, nil},
		{"PRIVMSG #a :caf\xe9", Message{Command: "PRIVMSG", Params: []string{"#a", "caf�"}}, nil},
		{
			"CMD 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16",
			Message{Command: "CMD", Params: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15 16"}},
			nil,
		},
		{"", Message{}, ErrEmpty},
		{"\r\n", Message{}, ErrEmpty},
		{"   ", Message{}, ErrEmpty},
		{":onlyprefix", Message{}, ErrInvalidCommand},
		{":p ", Message{}, ErrInvalidCommand},
		{"@a=1", Message{}, ErrInvalidCommand},
		{"@ PING", Message{}, ErrInvalidTags},
		{"@=v PING", Message{}, ErrInvalidTags},
		{"@/k PING", Message{}, ErrInvalidTags},
		{"PRIV-MSG x", Message{}, ErrInvalidCommand},
		{"12 x", Message{}, ErrInvalidCommand},
		{"PRIVMSG #a :a\x00b", Message{}, ErrInvalidByte},
		{"PRIVMSG #a :a\rb", Message{}, ErrInvalidByte},
		{"PRIVMSG #a :" + strings.Repeat("x", 500), Message{}, ErrLineTooLong},
		{"@a=" + strings.Repeat("x", MaxTagsLen) + " PING", Message{}, ErrTagsTooLong},
	}
	for _, tt := range tests {
		got, err := Parse(tt.line)
		if !errors.Is(err, tt.err) {
			t.Errorf("Parse(%q) err = %v, want %v", tt.line, err, tt.err)
			continue
		}
		if err == nil && !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Parse(%q) = %#v, want %#v", tt.line, got, tt.want)
		}
	}
}

func TestParseMaxLength(t *testing.T) {
	ok := "PRIVMSG #a :" + strings.Repeat("x", MaxLineLen-len(crlf)-len("PRIVMSG #a :"))
	if _, err := Parse(ok + "\r\n"); err != nil {
		t.Fatalf("510-byte line rejected: %v", err)
	}
	if _, err := Parse(ok + "x"); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("511-byte line accepted: %v", err)
	}
	// Tags do not count towards the 512 bytes.
	if _, err := Parse("@k=" + strings.Repeat("v", 1000) + " " + ok); err != nil {
		t.Fatalf("tags counted in line length: %v", err)
	}
}

func TestBytes(t *testing.T) {
	tests := []struct {
		msg  Message
		want string
		err  error
	}{
		{Message{Command: "PING", Params: []string{"tok"}}, "PING tok\r\n", nil},
		{Message{Source: "fugace", Command: "001", Params: []string{"bob", "Welcome bob"}}, ":fugace 001 bob :Welcome bob\r\n", nil},
		{Message{Command: "TOPIC", Params: []string{"#a", ""}}, "TOPIC #a :\r\n", nil},
		{Message{Command: "PRIVMSG", Params: []string{"bob", "1"}}, "PRIVMSG bob :1\r\n", nil},
		{Message{Command: "JOIN", Params: []string{"#a"}}, "JOIN #a\r\n", nil},
		{Message{Command: "PRIVMSG", Params: []string{"#a", ":)"}}, "PRIVMSG #a ::)\r\n", nil},
		{Message{Tags: []Tag{{"time", "t"}, {"k", "a b;c\\\r\n"}, {"flag", ""}}, Command: "PING"}, "@time=t;k=a\\sb\\:c\\\\\\r\\n;flag PING\r\n", nil},
		{Message{Command: "PRIVMSG", Params: []string{"a b", "x"}}, "", ErrInvalidParam},
		{Message{Command: "PRIVMSG", Params: []string{"", "x"}}, "", ErrInvalidParam},
		{Message{Command: "PRIVMSG", Params: []string{"#a", "x\r\nQUIT"}}, "", ErrInvalidParam},
		{Message{Source: "a b", Command: "PING"}, "", ErrInvalidParam},
		{Message{Command: "BAD CMD"}, "", ErrInvalidCommand},
		{Message{Tags: []Tag{{"bad key", "v"}}, Command: "PING"}, "", ErrInvalidTags},
		{Message{Command: "X", Params: make([]string, MaxParams+1)}, "", ErrInvalidParam},
	}
	for _, tt := range tests {
		got, err := tt.msg.Bytes()
		if !errors.Is(err, tt.err) || string(got) != tt.want {
			t.Errorf("Bytes(%#v) = %q, %v; want %q, %v", tt.msg, got, err, tt.want, tt.err)
		}
	}
}

func TestBytesTruncatesOnRuneBoundary(t *testing.T) {
	m := Message{Source: "nick!~user@fugace", Command: "PRIVMSG", Params: []string{"#a", strings.Repeat("é", 400)}}
	b, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxLineLen {
		t.Fatalf("line is %d bytes", len(b))
	}
	back, err := Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if text := back.Params[1]; strings.ContainsRune(text, '�') || text == "" {
		t.Fatalf("truncation broke UTF-8: %q", text)
	}
}

func TestParam(t *testing.T) {
	m := Message{Params: []string{"a"}}
	if m.Param(0) != "a" || m.Param(1) != "" {
		t.Fatal("Param")
	}
}
