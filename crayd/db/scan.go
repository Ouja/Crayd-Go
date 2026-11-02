package db

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Column -> field mapping:
//   - a `db:"name"` tag wins; `db:"-"` skips the field
//   - otherwise the field name in snake_case: ID -> id, UserID -> user_id, CreatedAt -> created_at
//   - embedded structs are flattened; unexported fields are ignored
//
// Every column in the result must map to a field, so a typo or a forgotten
// field is an error instead of a silently empty value. Fields without a column are left alone.

var (
	scannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()
	timeType    = reflect.TypeOf(time.Time{})
	fieldCache  sync.Map // reflect.Type -> map[string][]int
)

// isScalar reports whether t is scanned as one value rather than field by field.
func isScalar(t reflect.Type) bool {
	return t.Kind() != reflect.Struct || t == timeType || reflect.PointerTo(t).Implements(scannerType)
}

func scanInto(rows *sql.Rows, dst any) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return fmt.Errorf("db: destination must be a non-nil pointer, got %T", dst)
	}
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	targets, err := targetsFor(v.Elem(), cols)
	if err != nil {
		return err
	}
	return rows.Scan(targets...)
}

func scanAll(rows *sql.Rows, dst any) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Slice {
		return fmt.Errorf("db: Select needs a pointer to a slice, got %T", dst)
	}
	slice := v.Elem()
	elemType := slice.Type().Elem()
	isPtr := elemType.Kind() == reflect.Pointer
	base := elemType
	if isPtr {
		base = elemType.Elem()
	}
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	result := reflect.MakeSlice(slice.Type(), 0, 8)
	for rows.Next() {
		item := reflect.New(base)
		targets, err := targetsFor(item.Elem(), cols)
		if err != nil {
			return err
		}
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		if isPtr {
			result = reflect.Append(result, item)
		} else {
			result = reflect.Append(result, item.Elem())
		}
	}
	slice.Set(result)
	return nil
}

// targetsFor returns one scan target per column, pointing into v.
func targetsFor(v reflect.Value, cols []string) ([]any, error) {
	if isScalar(v.Type()) {
		if len(cols) != 1 {
			return nil, fmt.Errorf("db: scanning into %s needs exactly 1 column, query returned %d", v.Type(), len(cols))
		}
		return []any{v.Addr().Interface()}, nil
	}
	fields := fieldsOf(v.Type())
	targets := make([]any, len(cols))
	seen := make(map[string]bool, len(cols))
	for i, col := range cols {
		key := strings.ToLower(col)
		path, ok := fields[key]
		if !ok {
			return nil, fmt.Errorf("db: column %q has no matching field in %s (add a field or a `db:\"%s\"` tag)", col, v.Type(), key)
		}
		if seen[key] {
			return nil, fmt.Errorf("db: column %q appears twice; alias one of them (SELECT a.id, b.id AS b_id)", col)
		}
		seen[key] = true
		targets[i] = v.FieldByIndex(path).Addr().Interface()
	}
	return targets, nil
}

func fieldsOf(t reflect.Type) map[string][]int {
	if m, ok := fieldCache.Load(t); ok {
		return m.(map[string][]int)
	}
	m := map[string][]int{}
	collectFields(t, nil, m)
	fieldCache.Store(t, m)
	return m
}

func collectFields(t reflect.Type, prefix []int, m map[string][]int) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		path := append(append([]int(nil), prefix...), i)
		tag := f.Tag.Get("db")
		if tag == "-" {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && tag == "" && !isScalar(f.Type) {
			collectFields(f.Type, path, m) // flatten embedded structs
			continue
		}
		if !f.IsExported() {
			continue
		}
		name := tag
		if name == "" {
			name = snake(f.Name)
		}
		name = strings.ToLower(name)
		if _, dup := m[name]; !dup || len(path) <= len(m[name]) { // outer fields win over embedded ones
			m[name] = path
		}
	}
}

// snake turns "UserID" into "user_id" and "HTMLBody" into "html_body".
func snake(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) && i > 0 {
			prev := r[i-1]
			nextLower := i+1 < len(r) && unicode.IsLower(r[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}
