package irc

import (
	"strings"
	"testing"
)

func TestValidNick(t *testing.T) {
	tests := []struct {
		nick string
		ok   bool
	}{
		{"alice", true},
		{"[a]", true},
		{"`_^{|}\\", true},
		{"a-1", true},
		{strings.Repeat("a", NickLen), true},
		{strings.Repeat("a", NickLen+1), false},
		{"", false},
		{"1abc", false},
		{"-abc", false},
		{"a b", false},
		{"a!b", false},
		{"a@b", false},
		{"a#b", false},
		{"alicé", false},
		{"аlice", false}, // Cyrillic a: look-alike refused (T10)
		{"a\x00", false},
	}
	for _, tt := range tests {
		if got := ValidNick(tt.nick); got != tt.ok {
			t.Errorf("ValidNick(%q) = %v", tt.nick, got)
		}
	}
}

func TestValidChannel(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"#general", true},
		{"#a", true},
		{"#C++", true},
		{"#" + strings.Repeat("a", ChannelLen-1), true},
		{"#" + strings.Repeat("a", ChannelLen), false},
		{"#", false},
		{"general", false},
		{"&local", false},
		{"#a,b", false},
		{"#a b", false},
		{"#a\x07", false},
		{"#café", false},
	}
	for _, tt := range tests {
		if got := ValidChannel(tt.name); got != tt.ok {
			t.Errorf("ValidChannel(%q) = %v", tt.name, got)
		}
	}
}

func TestFold(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Alice", "alice"},
		{"alice", "alice"},
		{"[]\\~", "[]\\~"}, // ascii casemapping: no rfc1459 folding
		{"#GeNeRaL", "#general"},
		{"ÀB", "Àb"},
	}
	for _, tt := range tests {
		if got := Fold(tt.in); got != tt.want {
			t.Errorf("Fold(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if !IsChannel("#a") || IsChannel("a") {
		t.Error("IsChannel")
	}
}
