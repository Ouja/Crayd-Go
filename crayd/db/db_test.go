package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

var errBoom = errors.New("boom")

type tags []string // has its own Value method: must not be expanded

func (t tags) Value() (driver.Value, error) { return strings.Join(t, ","), nil }

func TestBind(t *testing.T) {
	tests := []struct {
		name    string
		d       Dialect
		q       string
		args    []any
		want    string
		wantArg []any
		err     string
	}{
		{"mysql plain", MySQL, "SELECT * FROM t WHERE a = ? AND b = ?", []any{1, "x"},
			"SELECT * FROM t WHERE a = ? AND b = ?", []any{1, "x"}, ""},
		{"postgres numbered", Postgres, "SELECT * FROM t WHERE a = ? AND b = ?", []any{1, "x"},
			"SELECT * FROM t WHERE a = $1 AND b = $2", []any{1, "x"}, ""},
		{"slice expands (mysql)", MySQL, "WHERE id IN (?)", []any{[]int{1, 2, 3}},
			"WHERE id IN (?, ?, ?)", []any{1, 2, 3}, ""},
		{"slice expands, numbering continues (pg)", Postgres, "WHERE id IN (?) AND s = ?", []any{[]string{"a", "b"}, "z"},
			"WHERE id IN ($1, $2) AND s = $3", []any{"a", "b", "z"}, ""},
		{"[]byte is one value", Postgres, "WHERE b = ?", []any{[]byte("hi")},
			"WHERE b = $1", []any{[]byte("hi")}, ""},
		{"Valuer slice is one value", MySQL, "WHERE t = ?", []any{tags{"a", "b"}},
			"WHERE t = ?", []any{tags{"a", "b"}}, ""},
		{"nil arg", Postgres, "WHERE x = ?", []any{nil}, "WHERE x = $1", []any{nil}, ""},
		{"?? is a literal ?", Postgres, "WHERE data ?? 'k' AND id = ?", []any{7},
			"WHERE data ? 'k' AND id = $1", []any{7}, ""},
		{"? inside quotes untouched", Postgres, "SELECT '?', \"?\", `?` FROM t WHERE x = ?", []any{1},
			"SELECT '?', \"?\", `?` FROM t WHERE x = $1", []any{1}, ""},
		{"doubled quote stays in string", Postgres, "WHERE s = 'it''s ?' AND x = ?", []any{1},
			"WHERE s = 'it''s ?' AND x = $1", []any{1}, ""},
		{"mysql backslash escape", MySQL, `WHERE s = 'a\'?' AND x = ?`, []any{1},
			`WHERE s = 'a\'?' AND x = ?`, []any{1}, ""},
		{"comments untouched", Postgres, "-- why?\nSELECT ? /* really? */ FROM t", []any{1},
			"-- why?\nSELECT $1 /* really? */ FROM t", []any{1}, ""},
		{"no args no placeholders", Postgres, "SELECT 1", nil, "SELECT 1", []any{}, ""},
		{"too few args", MySQL, "WHERE a = ? AND b = ?", []any{1}, "", nil, "more ? than arguments"},
		{"too many args", MySQL, "WHERE a = ?", []any{1, 2}, "", nil, "2 arguments given"},
		{"empty slice", MySQL, "WHERE id IN (?)", []any{[]int{}}, "", nil, "empty slice"},
		// In PostgreSQL a backslash is an ordinary character: 'a\' is a complete string,
		// so the next ? is a real placeholder and the rest is an (invalid) open string,
		// exactly as PostgreSQL itself would read it. MySQL reads the same text differently.
		{"pg has no backslash escapes", Postgres, `WHERE s = 'a\'?' AND x = ?`, []any{1},
			`WHERE s = 'a\'$1' AND x = ?`, []any{1}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, args, err := bind(tt.d, tt.q, tt.args)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("query\n got %q\nwant %q", got, tt.want)
			}
			if !reflect.DeepEqual(args, tt.wantArg) {
				t.Fatalf("args = %#v, want %#v", args, tt.wantArg)
			}
		})
	}
}

func TestPostgresQueriesReachDriverRebound(t *testing.T) {
	db, f := newFake(t, Postgres, nil)
	db.Exec(ctx, "DELETE FROM orders WHERE id IN (?) AND owner = ?", []int{4, 5}, "ada")
	c := f.last()
	if c.query != "DELETE FROM orders WHERE id IN ($1, $2) AND owner = $3" {
		t.Fatalf("driver saw %q", c.query)
	}
	if !reflect.DeepEqual(c.args, []driver.Value{int64(4), int64(5), "ada"}) {
		t.Fatalf("driver args %#v", c.args)
	}
}

type Money int64

type Base struct {
	ID        int64
	CreatedAt time.Time
}

type Order struct {
	Base     // flattened: id, created_at
	Customer string
	Total    Money
	Note     *string      // NULL -> nil
	Paid     sql.NullBool // Scanner type
	Internal string       `db:"-"` // never scanned
	Email    string       `db:"customer_email"`
	Items    []string     // no column: left alone
	secret   string       //nolint:unused // unexported: ignored
}

var orderCols = []string{"id", "customer", "total", "note", "paid", "created_at", "customer_email"}

func TestGetStruct(t *testing.T) {
	now := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	db, f := newFake(t, MySQL, rowsReply(orderCols,
		[]driver.Value{int64(7), "Ada", int64(10800), nil, true, now, "ada@example.com"}))

	var o Order
	if err := db.Get(ctx, &o, "SELECT * FROM orders WHERE id = ?", 7); err != nil {
		t.Fatal(err)
	}
	if o.ID != 7 || o.Customer != "Ada" || o.Total != 10800 || o.Note != nil || !o.Paid.Valid || !o.Paid.Bool ||
		!o.CreatedAt.Equal(now) || o.Email != "ada@example.com" {
		t.Fatalf("scanned %+v", o)
	}
	if f.last().query != "SELECT * FROM orders WHERE id = ?" {
		t.Fatalf("query %q", f.last().query)
	}
}

func TestColumnNamesAreCaseInsensitive(t *testing.T) {
	db, _ := newFake(t, MySQL, rowsReply([]string{"ID", "Customer"}, []driver.Value{int64(1), "x"}))
	var o Order
	if err := db.Get(ctx, &o, "SELECT 1"); err != nil || o.ID != 1 || o.Customer != "x" {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestGetScalarsAndNotFound(t *testing.T) {
	db, _ := newFake(t, Postgres, rowsReply([]string{"count"}, []driver.Value{int64(42)}))
	var n int
	if err := db.Get(ctx, &n, "SELECT COUNT(*) FROM orders"); err != nil || n != 42 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var ts time.Time // time.Time is a struct but scans as one value
	db2, _ := newFake(t, Postgres, rowsReply([]string{"now"}, []driver.Value{time.Unix(5, 0)}))
	if err := db2.Get(ctx, &ts, "SELECT now()"); err != nil || ts.Unix() != 5 {
		t.Fatalf("ts=%v err=%v", ts, err)
	}

	empty, _ := newFake(t, MySQL, rowsReply(orderCols))
	var o Order
	err := empty.Get(ctx, &o, "SELECT * FROM orders WHERE id = ?", 99)
	if !errors.Is(err, ErrNotFound) || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestScanErrorsAreHelpful(t *testing.T) {
	cases := []struct {
		name string
		cols []string
		row  []driver.Value
		dst  any
		want string
	}{
		{"unknown column", []string{"id", "colour"}, []driver.Value{int64(1), "red"}, &Order{}, `column "colour" has no matching field`},
		{"duplicate column", []string{"id", "id"}, []driver.Value{int64(1), int64(2)}, &Order{}, `appears twice`},
		{"db:\"-\" field is not a target", []string{"internal"}, []driver.Value{"x"}, &Order{}, `no matching field`},
		{"scalar with two columns", []string{"a", "b"}, []driver.Value{int64(1), int64(2)}, new(int), "exactly 1 column"},
		{"not a pointer", []string{"id"}, []driver.Value{int64(1)}, Order{}, "non-nil pointer"},
		{"nil pointer", []string{"id"}, []driver.Value{int64(1)}, (*Order)(nil), "non-nil pointer"},
		{"type mismatch from driver", []string{"id"}, []driver.Value{"not a number"}, &Order{}, "converting"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, _ := newFake(t, MySQL, rowsReply(c.cols, c.row))
			err := db.Get(ctx, c.dst, "SELECT")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestSelect(t *testing.T) {
	rows := [][]driver.Value{{int64(1), "Ada"}, {int64(2), "Bob"}}
	db, _ := newFake(t, MySQL, rowsReply([]string{"id", "customer"}, rows...))

	var list []Order
	if err := db.Select(ctx, &list, "SELECT id, customer FROM orders"); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Customer != "Ada" || list[1].ID != 2 {
		t.Fatalf("%+v", list)
	}
	var ptrs []*Order
	if err := db.Select(ctx, &ptrs, "SELECT id, customer FROM orders"); err != nil || len(ptrs) != 2 || ptrs[1].Customer != "Bob" {
		t.Fatalf("%v %v", ptrs, err)
	}
	if ptrs[0] == ptrs[1] {
		t.Fatal("pointers share one struct")
	}

	names, _ := newFake(t, MySQL, rowsReply([]string{"customer"}, []driver.Value{"Ada"}, []driver.Value{"Bob"}))
	var ss []string
	if err := names.Select(ctx, &ss, "SELECT customer FROM orders"); err != nil || !reflect.DeepEqual(ss, []string{"Ada", "Bob"}) {
		t.Fatalf("%v %v", ss, err)
	}

	none, _ := newFake(t, MySQL, rowsReply([]string{"id"}))
	list = []Order{{Customer: "stale"}}
	if err := none.Select(ctx, &list, "SELECT id FROM orders"); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty result should give an empty non-nil slice, got %#v %v", list, err)
	}

	var notSlice Order
	if err := db.Select(ctx, &notSlice, "SELECT 1"); err == nil || !strings.Contains(err.Error(), "pointer to a slice") {
		t.Fatalf("err = %v", err)
	}
}

func TestQueryErrorsPropagate(t *testing.T) {
	boom := errors.New("connection refused")
	db, _ := newFake(t, MySQL, func(string, []driver.Value) reply { return reply{err: boom} })
	var o Order
	if err := db.Get(ctx, &o, "SELECT 1"); !errors.Is(err, boom) {
		t.Fatalf("Get: %v", err)
	}
	var l []Order
	if err := db.Select(ctx, &l, "SELECT 1"); !errors.Is(err, boom) {
		t.Fatalf("Select: %v", err)
	}
	if _, err := db.Exec(ctx, "DELETE"); !errors.Is(err, boom) {
		t.Fatalf("Exec: %v", err)
	}
	if err := db.Get(ctx, &o, "SELECT ?"); err == nil || !strings.Contains(err.Error(), "more ? than arguments") {
		t.Fatalf("bind error: %v", err)
	}
}

func TestExecAndInsert(t *testing.T) {
	my, f := newFake(t, MySQL, func(q string, _ []driver.Value) reply { return reply{lastID: 41, n: 3} })
	if n, err := my.Exec(ctx, "UPDATE orders SET paid = ? WHERE customer = ?", true, "Ada"); err != nil || n != 3 {
		t.Fatalf("Exec n=%d err=%v", n, err)
	}
	if id, err := my.Insert(ctx, "INSERT INTO orders (customer) VALUES (?)", "Ada"); err != nil || id != 41 {
		t.Fatalf("mysql Insert id=%d err=%v", id, err)
	}
	if f.last().query != "INSERT INTO orders (customer) VALUES (?)" {
		t.Fatalf("query %q", f.last().query)
	}

	pg, pf := newFake(t, Postgres, rowsReply([]string{"id"}, []driver.Value{int64(9)}))
	if id, err := pg.Insert(ctx, "INSERT INTO orders (customer) VALUES (?) RETURNING id", "Ada"); err != nil || id != 9 {
		t.Fatalf("pg Insert id=%d err=%v", id, err)
	}
	if pf.last().query != "INSERT INTO orders (customer) VALUES ($1) RETURNING id" {
		t.Fatalf("query %q", pf.last().query)
	}
	if id, err := pg.Insert(ctx, "INSERT INTO orders (customer) VALUES (?);\n", "Ada"); err != nil || id != 9 {
		t.Fatalf("pg Insert without RETURNING: id=%d err=%v", id, err)
	}
	if pf.last().query != "INSERT INTO orders (customer) VALUES ($1) RETURNING id" {
		t.Fatalf("RETURNING id not added: %q", pf.last().query)
	}
	pgNoRow, _ := newFake(t, Postgres, rowsReply([]string{"id"}))
	if _, err := pgNoRow.Insert(ctx, "INSERT INTO orders (customer) VALUES (?) RETURNING id", "Ada"); err == nil || !strings.Contains(err.Error(), "RETURNING id") {
		t.Fatalf("err = %v", err)
	}
}

func TestTx(t *testing.T) {
	db, f := newFake(t, Postgres, func(q string, _ []driver.Value) reply { return reply{n: 1} })

	err := db.Tx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, "UPDATE a SET x = ?", 1)
		return err
	})
	if err != nil || f.commits != 1 || f.rollbacks != 0 {
		t.Fatalf("commit path: err=%v commits=%d rollbacks=%d", err, f.commits, f.rollbacks)
	}
	if f.last().query != "UPDATE a SET x = $1" {
		t.Fatalf("tx query not rebound: %q", f.last().query)
	}

	sentinel := errors.New("out of stock")
	err = db.Tx(ctx, func(tx *Tx) error { return sentinel })
	if !errors.Is(err, sentinel) || f.commits != 1 || f.rollbacks != 1 {
		t.Fatalf("error path: err=%v commits=%d rollbacks=%d", err, f.commits, f.rollbacks)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic was swallowed")
			}
		}()
		db.Tx(ctx, func(tx *Tx) error { panic("bug") })
	}()
	if f.commits != 1 || f.rollbacks != 2 {
		t.Fatalf("panic path: commits=%d rollbacks=%d", f.commits, f.rollbacks)
	}
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{"ID": "id", "UserID": "user_id", "CreatedAt": "created_at",
		"HTMLBody": "html_body", "Total": "total", "Address2": "address2", "OrderID2": "order_id2"} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDialectFor(t *testing.T) {
	for name, want := range map[string]Dialect{"mysql": MySQL, "pgx": Postgres, "postgres": Postgres, "sqlite": SQLite} {
		if d, err := dialectFor(name); err != nil || d != want {
			t.Errorf("%s -> %v %v", name, d, err)
		}
	}
	if _, err := dialectFor("oracle"); err == nil {
		t.Error("unknown driver accepted")
	}
}

func TestDynamicIsTheOnlyWayToUseAVariable(t *testing.T) {
	col := "customer" // imagine: picked from a whitelist, never raw user input
	q := Dynamic("SELECT " + col + " FROM orders WHERE id = ?")
	db, f := newFake(t, Postgres, rowsReply([]string{"customer"}, []driver.Value{"Ada"}))
	var s string
	if err := db.Get(ctx, &s, q, 1); err != nil || s != "Ada" || f.last().query != "SELECT customer FROM orders WHERE id = $1" {
		t.Fatalf("%q %v %q", s, err, f.last().query)
	}
}
