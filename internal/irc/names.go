package irc

import "strings"

// Name limits (SPEC §4, advertised in ISUPPORT).
const (
	NickLen    = 30
	ChannelLen = 32 // '#' included
	TopicLen   = 300
)

// ValidNick implements ^[A-Za-z\[\]\\`_^{|}][A-Za-z0-9\[\]\\`_^{|}-]{0,29}$.
func ValidNick(s string) bool {
	if s == "" || len(s) > NickLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case isNickSpecial(c), (c >= 'a' && c <= 'z'), (c >= 'A' && c <= 'Z'):
		case i > 0 && (isDigit(c) || c == '-'):
		default:
			return false
		}
	}
	return true
}

func isNickSpecial(c byte) bool {
	return strings.IndexByte("[]\\`_^{|}", c) >= 0
}

// ValidChannel accepts '#' followed by 1 to ChannelLen-1 printable ASCII
// characters other than ','. Restricting to ASCII rules out look-alike names.
func ValidChannel(s string) bool {
	if len(s) < 2 || len(s) > ChannelLen || s[0] != '#' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; c <= ' ' || c > '~' || c == ',' {
			return false
		}
	}
	return true
}

// IsChannel reports whether a target names a channel.
func IsChannel(s string) bool { return strings.HasPrefix(s, "#") }

// Fold applies CASEMAPPING=ascii: only A-Z are lowered.
func Fold(s string) string {
	for i := range len(s) {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}
