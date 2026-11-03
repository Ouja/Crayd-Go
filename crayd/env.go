package crayd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

// LoadDotEnv reads KEY=VALUE lines from path into the environment. Variables that
// are already set win. A missing file is not an error.
//
// Supported: # comments, "export KEY=v", "double quoted" values with \n \t \" \\
// escapes, 'single quoted' literals, and a trailing " # comment" after unquoted
// values. Values do not span lines. CRLF and a UTF-8 BOM are handled.
//
// Write Windows paths unquoted or in single quotes: in "C:\temp\new" the \t and \n
// would become a tab and a newline.
//
// Don't commit a .env that holds secrets; commit .env.example instead.
func LoadDotEnv(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	pairs, err := parseDotEnv(string(b))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, kv := range pairs {
		if _, set := os.LookupEnv(kv[0]); !set {
			if err := os.Setenv(kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// parseDotEnv never puts values into its errors: they may be secrets.
func parseDotEnv(src string) ([][2]string, error) {
	src = strings.TrimPrefix(src, "\uFEFF")
	var out [][2]string
	for i, line := range strings.Split(src, "\n") {
		n := i + 1
		line = strings.TrimSpace(line) // also drops the \r of CRLF
		if line == "" || line[0] == '#' {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(rest)
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("line %d: expected KEY=value", n)
		}
		key := strings.TrimSpace(line[:eq])
		if !validEnvKey(key) {
			return nil, fmt.Errorf("line %d: %q is not a valid variable name", n, key)
		}
		val, err := parseEnvValue(strings.TrimSpace(line[eq+1:]))
		if err != nil {
			return nil, fmt.Errorf("line %d (%s): %w", n, key, err)
		}
		out = append(out, [2]string{key, val})
	}
	return out, nil
}

func validEnvKey(k string) bool {
	for i, r := range k {
		letter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !letter && !(i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return k != ""
}

func parseEnvValue(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	switch s[0] {
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", errors.New("unterminated single quote")
		}
		return s[1 : 1+end], afterQuote(s[2+end:])
	case '"':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			switch c := s[i]; {
			case c == '\\' && i+1 < len(s):
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				case '"', '\\':
					b.WriteByte(s[i])
				default: // unknown escape: keep it as written, so Windows paths like "C:\data" survive
					b.WriteByte('\\')
					b.WriteByte(s[i])
				}
			case c == '"':
				return b.String(), afterQuote(s[i+1:])
			default:
				b.WriteByte(c)
			}
		}
		return "", errors.New("unterminated double quote")
	}
	// Unquoted: a " #" starts a comment.
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			s = s[:i]
			break
		}
	}
	return strings.TrimSpace(s), nil
}

func afterQuote(rest string) error {
	rest = strings.TrimSpace(rest)
	if rest == "" || rest[0] == '#' {
		return nil
	}
	return errors.New("unexpected text after the closing quote")
}

// Env reads typed settings from the environment and collects every problem,
// so a bad deployment shows all mistakes at once:
//
//	e := &crayd.Env{}
//	addr := e.String("ADDR", ":8080")
//	dsn := e.Required("DB_DSN")
//	if err := e.Err(); err != nil { log.Fatal(err) }
//
// Empty variables count as unset. Values are trimmed of surrounding spaces.
// Error messages never include the value of String or Required settings.
type Env struct{ problems []string }

func lookup(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

// Add records a problem found by your own validation.
func (e *Env) Add(problem string) { e.problems = append(e.problems, problem) }

// String returns the variable, or def when unset.
func (e *Env) String(key, def string) string {
	if v, ok := lookup(key); ok {
		return v
	}
	return def
}

// Required returns the variable and records a problem when it is unset.
func (e *Env) Required(key string) string {
	v, ok := lookup(key)
	if !ok {
		e.Add(key + " is required")
	}
	return v
}

// Int parses a whole number.
func (e *Env) Int(key string, def int) int {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.Add(fmt.Sprintf("%s must be a whole number (got %q)", key, v))
		return def
	}
	return n
}

// Bool accepts true/false, 1/0, yes/no and on/off in any case.
func (e *Env) Bool(key string, def bool) bool {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	case "0", "false", "f", "no", "n", "off":
		return false
	}
	e.Add(fmt.Sprintf("%s must be true or false (got %q)", key, v))
	return def
}

// Duration parses values like 500ms, 15s or 2m. Negative values are rejected.
func (e *Env) Duration(key string, def time.Duration) time.Duration {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		e.Add(fmt.Sprintf("%s must be a duration like 15s or 2m (got %q)", key, v))
		return def
	}
	return d
}

// Size parses a byte size: a plain number of bytes, or a number with KB, MB or GB
// (1 KB = 1024 bytes; the B is optional and case doesn't matter), like 512KB or 10MB.
func (e *Env) Size(key string, def int64) int64 {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	n, err := parseSize(v)
	if err != nil {
		e.Add(fmt.Sprintf("%s must be a size like 1MB or 512KB (got %q)", key, v))
		return def
	}
	return n
}

func parseSize(v string) (int64, error) {
	s := strings.ToUpper(strings.TrimSpace(v))
	s = strings.TrimSuffix(s, "B")
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 || n > (1<<62)/mult {
		return 0, errors.New("invalid size")
	}
	return n * mult, nil
}

// OneOf returns the variable lower-cased, which must be one of allowed.
func (e *Env) OneOf(key, def string, allowed ...string) string {
	v, ok := lookup(key)
	if !ok {
		return def
	}
	v = strings.ToLower(v)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	e.Add(fmt.Sprintf("%s must be one of %s (got %q)", key, strings.Join(allowed, ", "), v))
	return def
}

// Err returns all recorded problems as one error, or nil.
func (e *Env) Err() error {
	if len(e.problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(e.problems, "\n  - "))
}
