package files

import (
	"path"
	"strings"
)

const (
	maxNameLen  = 80
	defaultName = "file"
)

// SanitizeName keeps a short, harmless file name: base name only, ASCII
// letters, digits, '.', '-', '_'. For re-encoded images ext replaces the
// extension so that the name matches the served type.
func SanitizeName(name, ext string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.TrimLeft(b.String(), "._-")
	if ext != "" {
		s = strings.TrimSuffix(s, path.Ext(s))
	}
	if len(s) > maxNameLen-len(ext) {
		s = s[:maxNameLen-len(ext)]
	}
	s = strings.TrimRight(s, ".")
	if s == "" {
		s = defaultName
	}
	return s + ext
}
