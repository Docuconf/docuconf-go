package docuconf

import (
	"fmt"
	"os"
	"strings"
)

// readDotEnv parses a .env file: KEY=value lines, with optional "export "
// prefixes, # comments, and single- or double-quoted values. Double-quoted
// values may span lines and understand \n, \t, \" and \\ escapes.
func readDotEnv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDotEnv(string(data), path)
}

func parseDotEnv(s, name string) (map[string]string, error) {
	out := map[string]string{}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	line := 1
	for len(s) > 0 {
		// One logical line: skip blanks and comments.
		nl := strings.IndexByte(s, '\n')
		cur := s
		if nl >= 0 {
			cur = s[:nl]
		}
		trimmed := strings.TrimSpace(cur)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if nl < 0 {
				break
			}
			s, line = s[nl+1:], line+1
			continue
		}
		rest := strings.TrimLeft(s, " \t")
		rest = strings.TrimPrefix(rest, "export ")
		first := rest
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			first = rest[:i]
		}
		eq := strings.IndexByte(first, '=')
		if eq < 0 {
			return nil, fmt.Errorf("%s:%d: expected KEY=value", name, line)
		}
		key := strings.TrimSpace(rest[:eq])
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty key", name, line)
		}
		rest = strings.TrimLeft(rest[eq+1:], " \t")
		var val string
		switch {
		case strings.HasPrefix(rest, `"`):
			var b strings.Builder
			i := 1
			for ; i < len(rest) && rest[i] != '"'; i++ {
				c := rest[i]
				if c == '\n' {
					line++
				}
				if c == '\\' && i+1 < len(rest) {
					i++
					switch rest[i] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					case 'r':
						b.WriteByte('\r')
					default:
						b.WriteByte(rest[i])
					}
					continue
				}
				b.WriteByte(c)
			}
			if i >= len(rest) {
				return nil, fmt.Errorf("%s:%d: unterminated double-quoted value for %s", name, line, key)
			}
			val, rest = b.String(), rest[i+1:]
		case strings.HasPrefix(rest, `'`):
			end := strings.IndexByte(rest[1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("%s:%d: unterminated single-quoted value for %s", name, line, key)
			}
			val, rest = rest[1:end+1], rest[end+2:]
			line += strings.Count(val, "\n")
		default:
			end := strings.IndexByte(rest, '\n')
			if end < 0 {
				end = len(rest)
			}
			val, rest = rest[:end], rest[end:]
			if i := strings.Index(val, " #"); i >= 0 {
				val = val[:i]
			}
			val = strings.TrimRight(val, " \t")
		}
		out[key] = val
		// Discard the remainder of the line (trailing comment).
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			s, line = rest[i+1:], line+1
		} else {
			s = ""
		}
	}
	return out, nil
}
