// Package irc parses and serializes IRC messages (RFC 1459/2812 with IRCv3
// message tags) and validates nicknames and channel names.
package irc

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Protocol size limits.
const (
	// MaxLineLen is the maximum length of a line without its tags, CRLF included.
	MaxLineLen = 512
	// MaxTagsLen is the maximum length of the tags section, '@' and the
	// trailing space included.
	MaxTagsLen = 4096
	// MaxReadLen bounds a whole incoming line, tags included, without CRLF.
	MaxReadLen = MaxTagsLen + MaxLineLen
	// MaxParams is the maximum number of parameters of a message.
	MaxParams = 15

	crlf = "\r\n"
)

// Parse errors.
var (
	ErrEmpty          = errors.New("irc: empty line")
	ErrLineTooLong    = errors.New("irc: line too long")
	ErrTagsTooLong    = errors.New("irc: tags too long")
	ErrInvalidTags    = errors.New("irc: invalid tags")
	ErrInvalidCommand = errors.New("irc: invalid command")
	ErrInvalidByte    = errors.New("irc: forbidden byte in line")
	ErrInvalidParam   = errors.New("irc: invalid parameter")
)

// Tag is one IRCv3 message tag. Value is unescaped.
type Tag struct {
	Key   string
	Value string
}

// Message is one IRC line. All strings are valid UTF-8 after Parse.
type Message struct {
	Tags    []Tag
	Source  string // prefix without ':'
	Command string // upper-case
	Params  []string
}

// Param returns the i-th parameter or "".
func (m *Message) Param(i int) string {
	if i < len(m.Params) {
		return m.Params[i]
	}
	return ""
}

// Parse parses one line. A trailing "\r\n" or "\n" is ignored. Invalid
// UTF-8 is replaced with U+FFFD so that every relayed string is valid.
func Parse(line string) (Message, error) {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if strings.ContainsAny(line, "\x00\r\n") {
		return Message{}, ErrInvalidByte
	}
	var m Message
	if strings.HasPrefix(line, "@") {
		end := strings.IndexByte(line, ' ')
		if end < 0 {
			end = len(line)
		}
		if end+1 > MaxTagsLen {
			return Message{}, ErrTagsTooLong
		}
		tags, err := parseTags(line[1:end])
		if err != nil {
			return Message{}, err
		}
		m.Tags = tags
		line = line[end:]
	}
	line = strings.TrimLeft(line, " ")
	if len(line)+len(crlf) > MaxLineLen {
		return Message{}, ErrLineTooLong
	}
	if strings.HasPrefix(line, ":") {
		end := strings.IndexByte(line, ' ')
		if end < 0 {
			return Message{}, ErrInvalidCommand
		}
		m.Source = strings.ToValidUTF8(line[1:end], "�")
		line = strings.TrimLeft(line[end:], " ")
	}
	if line == "" {
		if m.Source == "" && m.Tags == nil {
			return Message{}, ErrEmpty
		}
		return Message{}, ErrInvalidCommand
	}
	cmd, rest, _ := strings.Cut(line, " ")
	if !validCommand(cmd) {
		return Message{}, ErrInvalidCommand
	}
	m.Command = strings.ToUpper(cmd)
	m.Params = parseParams(rest)
	return m, nil
}

func parseParams(rest string) []string {
	var params []string
	for {
		rest = strings.TrimLeft(rest, " ")
		if rest == "" {
			return params
		}
		if rest[0] == ':' || len(params) == MaxParams-1 {
			p := strings.TrimPrefix(rest, ":")
			return append(params, strings.ToValidUTF8(p, "�"))
		}
		var p string
		p, rest, _ = strings.Cut(rest, " ")
		params = append(params, strings.ToValidUTF8(p, "�"))
	}
}

// validCommand accepts letters (and inner hyphens), or exactly three digits.
func validCommand(s string) bool {
	if s == "" {
		return false
	}
	if len(s) == 3 && isDigit(s[0]) && isDigit(s[1]) && isDigit(s[2]) {
		return true
	}
	// Letters, with inner hyphens allowed for vendor commands (CLEAR-ACCOUNT).
	if s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func parseTags(s string) ([]Tag, error) {
	if s == "" {
		return nil, ErrInvalidTags
	}
	parts := strings.Split(s, ";")
	tags := make([]Tag, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue // tolerated, as in the IRCv3 spec
		}
		k, v, _ := strings.Cut(p, "=")
		if !validTagKey(k) {
			return nil, ErrInvalidTags
		}
		tags = append(tags, Tag{Key: k, Value: strings.ToValidUTF8(unescapeTag(v), "�")})
	}
	return tags, nil
}

// validTagKey implements: ['+'] [vendor '/'] key, key = 1*(alnum / '-').
func validTagKey(k string) bool {
	k = strings.TrimPrefix(k, "+")
	if i := strings.LastIndexByte(k, '/'); i >= 0 {
		vendor := k[:i]
		if vendor == "" || !isHostLike(vendor) {
			return false
		}
		k = k[i+1:]
	}
	if k == "" {
		return false
	}
	for i := range len(k) {
		c := k[i]
		if !isAlnum(c) && c != '-' {
			return false
		}
	}
	return true
}

func isHostLike(s string) bool {
	for i := range len(s) {
		c := s[i]
		if !isAlnum(c) && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func unescapeTag(v string) string {
	if !strings.ContainsRune(v, '\\') {
		return v
	}
	var b strings.Builder
	b.Grow(len(v))
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i == len(v) {
			break // a lone trailing backslash is dropped
		}
		switch v[i] {
		case ':':
			b.WriteByte(';')
		case 's':
			b.WriteByte(' ')
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		default:
			b.WriteByte(v[i])
		}
	}
	return b.String()
}

func escapeTag(v string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\:`, " ", `\s`, "\r", `\r`, "\n", `\n`)
	return r.Replace(v)
}

// Bytes serializes m with a trailing CRLF. The part after the tags is capped
// at MaxLineLen: the last parameter is truncated on a rune boundary if
// needed. It fails if a parameter other than the last cannot be encoded.
func (m *Message) Bytes() ([]byte, error) {
	if !validCommand(m.Command) {
		return nil, ErrInvalidCommand
	}
	var b strings.Builder
	if len(m.Tags) > 0 {
		b.WriteByte('@')
		for i, t := range m.Tags {
			if !validTagKey(t.Key) {
				return nil, ErrInvalidTags
			}
			if i > 0 {
				b.WriteByte(';')
			}
			b.WriteString(t.Key)
			if t.Value != "" {
				b.WriteByte('=')
				b.WriteString(escapeTag(t.Value))
			}
		}
		if b.Len()+1 > MaxTagsLen {
			return nil, ErrTagsTooLong
		}
		b.WriteByte(' ')
	}
	tagsLen := b.Len()
	if m.Source != "" {
		if strings.ContainsAny(m.Source, " \x00\r\n") {
			return nil, ErrInvalidParam
		}
		b.WriteByte(':')
		b.WriteString(m.Source)
		b.WriteByte(' ')
	}
	b.WriteString(m.Command)
	if len(m.Params) > MaxParams {
		return nil, ErrInvalidParam
	}
	for i, p := range m.Params {
		b.WriteByte(' ')
		last := i == len(m.Params)-1
		if !last {
			if !validMiddle(p) {
				return nil, ErrInvalidParam
			}
			b.WriteString(p)
			continue
		}
		if strings.ContainsAny(p, "\x00\r\n") {
			return nil, ErrInvalidParam
		}
		// Like most servers, always use the trailing form when there are
		// several parameters: some clients expect it for the text.
		if p == "" || p[0] == ':' || strings.ContainsRune(p, ' ') || len(m.Params) > 1 {
			b.WriteByte(':')
		}
		room := MaxLineLen - len(crlf) - (b.Len() - tagsLen)
		if room < 0 {
			return nil, ErrLineTooLong
		}
		b.WriteString(TruncateUTF8(p, room))
	}
	if b.Len()-tagsLen+len(crlf) > MaxLineLen {
		return nil, ErrLineTooLong
	}
	b.WriteString(crlf)
	return []byte(b.String()), nil
}

// validMiddle reports whether s can be sent as a non-trailing parameter.
func validMiddle(s string) bool {
	return s != "" && s[0] != ':' && !strings.ContainsAny(s, " \x00\r\n")
}

// TruncateUTF8 cuts s to at most n bytes without splitting a rune.
func TruncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
