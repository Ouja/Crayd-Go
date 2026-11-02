package db

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// bind rewrites ? placeholders for the dialect and expands slice arguments.
// It understands quoted strings, quoted identifiers and comments, so a ? inside
// 'text', "name", `name`, -- comments or /* comments */ is left alone.
func bind(d Dialect, q string, args []any) (string, []any, error) {
	var b strings.Builder
	b.Grow(len(q) + 8)
	out := make([]any, 0, len(args))
	argi, n := 0, 0 // next input argument, placeholders written so far

	placeholder := func() {
		n++
		if d == Postgres {
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteByte('?')
		}
	}

	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := skipQuoted(d, q, i)
			b.WriteString(q[i:j])
			i = j - 1
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			j := strings.IndexByte(q[i:], '\n')
			if j < 0 {
				j = len(q) - i
			}
			b.WriteString(q[i : i+j])
			i += j - 1
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			j := strings.Index(q[i+2:], "*/")
			end := len(q)
			if j >= 0 {
				end = i + 2 + j + 2
			}
			b.WriteString(q[i:end])
			i = end - 1
		case c == '?' && i+1 < len(q) && q[i+1] == '?':
			b.WriteByte('?') // ?? is a literal ? (e.g. PostgreSQL JSON operators)
			i++
		case c == '?':
			if argi >= len(args) {
				return "", nil, fmt.Errorf("db: query has more ? than arguments (%d given)", len(args))
			}
			arg := args[argi]
			argi++
			if items, ok := expandable(arg); ok {
				if len(items) == 0 {
					return "", nil, fmt.Errorf("db: empty slice for argument %d (an empty IN list is invalid SQL)", argi)
				}
				for k, it := range items {
					if k > 0 {
						b.WriteString(", ")
					}
					placeholder()
					out = append(out, it)
				}
				continue
			}
			placeholder()
			out = append(out, arg)
		default:
			b.WriteByte(c)
		}
	}
	if argi != len(args) {
		return "", nil, fmt.Errorf("db: %d arguments given but the query has %d ?", len(args), argi)
	}
	return b.String(), out, nil
}

// skipQuoted returns the index just past the quoted section starting at q[i].
// A doubled quote (”) is an escaped quote. MySQL also allows backslash escapes in strings.
func skipQuoted(d Dialect, q string, i int) int {
	quote := q[i]
	for j := i + 1; j < len(q); j++ {
		switch {
		case q[j] == '\\' && d == MySQL && quote != '`':
			j++ // skip the escaped character
		case q[j] == quote:
			if j+1 < len(q) && q[j+1] == quote {
				j++ // doubled quote: still inside
				continue
			}
			return j + 1
		}
	}
	return len(q) // unterminated: leave the rest as is, the database will complain
}

var valuerType = reflect.TypeOf((*driver.Valuer)(nil)).Elem()

// expandable reports whether arg is a slice to spread over several placeholders.
// []byte and types with their own Value method are passed through as one value.
func expandable(arg any) ([]any, bool) {
	if arg == nil {
		return nil, false
	}
	v := reflect.ValueOf(arg)
	if v.Kind() != reflect.Slice || v.Type().Elem().Kind() == reflect.Uint8 || v.Type().Implements(valuerType) {
		return nil, false
	}
	items := make([]any, v.Len())
	for i := range items {
		items[i] = v.Index(i).Interface()
	}
	return items, true
}
