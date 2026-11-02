package db

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSplitStatements(t *testing.T) {
	tests := []struct {
		name string
		d    Dialect
		src  string
		want []string
	}{
		{"two statements", MySQL, "CREATE TABLE a (id INT);\nCREATE TABLE b (id INT);\n",
			[]string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)"}},
		{"no trailing semicolon", MySQL, "SELECT 1", []string{"SELECT 1"}},
		{"semicolon in string", MySQL, "INSERT INTO t VALUES ('a;b'); SELECT 2;",
			[]string{"INSERT INTO t VALUES ('a;b')", "SELECT 2"}},
		{"doubled quote", MySQL, "SELECT 'it''s; fine'; SELECT 3", []string{"SELECT 'it''s; fine'", "SELECT 3"}},
		{"backtick identifier", MySQL, "CREATE TABLE `a;b` (id INT); SELECT 4", []string{"CREATE TABLE `a;b` (id INT)", "SELECT 4"}},
		{"mysql backslash escape", MySQL, `SELECT 'a\';b'; SELECT 5`, []string{`SELECT 'a\';b'`, "SELECT 5"}},
		{"line comments hide semicolons", MySQL, "-- drop; everything;\nSELECT 6; # also; hidden\nSELECT 7",
			[]string{"-- drop; everything;\nSELECT 6", "# also; hidden\nSELECT 7"}},
		{"block comment", MySQL, "/* a; b */ SELECT 8;", []string{"/* a; b */ SELECT 8"}},
		{"only comments and blanks dropped", MySQL, "-- nothing\n;\n  ;\n/* x */;", nil},
		{"empty", MySQL, "  \n ", nil},
		{"hash is an operator on postgres", Postgres, "SELECT a # b; SELECT 9", []string{"SELECT a # b", "SELECT 9"}},
		{"pg backslash is plain", Postgres, `SELECT 'a\'; SELECT 10`, []string{`SELECT 'a\'`, "SELECT 10"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitStatements(tt.d, tt.src); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

var migrationFiles = fstest.MapFS{
	"001_users.sql":  {Data: []byte("CREATE TABLE users (id INT);\nCREATE INDEX i ON users (id);")},
	"002_orders.sql": {Data: []byte("CREATE TABLE orders (id INT); -- ok\n")},
	"README.md":      {Data: []byte("not a migration")},
	"sub/003.sql":    {Data: []byte("SELECT 'ignored: lives in a subfolder'")},
}

// migrateHandler pretends to be MySQL: the lock is granted and `applied` are already recorded.
func migrateHandler(applied ...string) func(string, []driver.Value) reply {
	return func(q string, args []driver.Value) reply {
		switch {
		case strings.HasPrefix(q, "SELECT GET_LOCK"):
			return reply{cols: []string{"l"}, rows: [][]driver.Value{{int64(1)}}}
		case strings.HasPrefix(q, "SELECT RELEASE_LOCK"):
			return reply{cols: []string{"l"}, rows: [][]driver.Value{{int64(1)}}}
		case strings.HasPrefix(q, "SELECT version FROM schema_migrations"):
			var rows [][]driver.Value
			for _, a := range applied {
				rows = append(rows, []driver.Value{a})
			}
			return reply{cols: []string{"version"}, rows: rows}
		}
		return reply{n: 1}
	}
}

func queriesOf(f *fakeDB) []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.query)
	}
	return out
}

func TestMigrateAppliesPendingFilesInOrder(t *testing.T) {
	conn, f := newFake(t, MySQL, migrateHandler())
	applied, err := Migrate(ctx, conn, migrationFiles, ".")
	if err != nil || !reflect.DeepEqual(applied, []string{"001_users", "002_orders"}) {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	q := queriesOf(f)
	want := []string{
		"SELECT GET_LOCK(?, 30)",
		"CREATE TABLE IF NOT EXISTS schema_migrations",
		"SELECT version FROM schema_migrations",
		"CREATE TABLE users (id INT)",
		"CREATE INDEX i ON users (id)",
		"INSERT INTO schema_migrations (version) VALUES (?)",
		"CREATE TABLE orders (id INT)",
		"INSERT INTO schema_migrations (version) VALUES (?)",
		"SELECT RELEASE_LOCK(?)",
	}
	if len(q) != len(want) {
		t.Fatalf("queries:\n%s", strings.Join(q, "\n"))
	}
	for i := range want {
		if !strings.HasPrefix(q[i], want[i]) {
			t.Errorf("query %d = %q, want prefix %q", i, q[i], want[i])
		}
	}
	// versions are recorded as arguments, never concatenated into SQL
	var recorded []driver.Value
	for _, c := range f.calls {
		if strings.HasPrefix(c.query, "INSERT INTO schema_migrations") {
			recorded = append(recorded, c.args[0])
		}
	}
	if !reflect.DeepEqual(recorded, []driver.Value{"001_users", "002_orders"}) {
		t.Fatalf("recorded %v", recorded)
	}
	if f.commits != 0 {
		t.Fatal("MySQL migrations must not pretend to be transactional")
	}
}

func TestMigrateSkipsAlreadyApplied(t *testing.T) {
	conn, f := newFake(t, MySQL, migrateHandler("001_users"))
	applied, err := Migrate(ctx, conn, migrationFiles, ".")
	if err != nil || !reflect.DeepEqual(applied, []string{"002_orders"}) {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	for _, q := range queriesOf(f) {
		if strings.Contains(q, "CREATE TABLE users") {
			t.Fatal("re-ran an applied migration")
		}
	}
	conn2, f2 := newFake(t, MySQL, migrateHandler("001_users", "002_orders"))
	if applied, err := Migrate(ctx, conn2, migrationFiles, "."); err != nil || len(applied) != 0 {
		t.Fatalf("up to date: %v %v", applied, err)
	}
	if got := queriesOf(f2); got[len(got)-1] != "SELECT RELEASE_LOCK(?)" {
		t.Fatalf("lock not released: %v", got)
	}
}

func TestMigrateStopsAtFailureAndReleasesLock(t *testing.T) {
	h := migrateHandler()
	conn, f := newFake(t, MySQL, func(q string, a []driver.Value) reply {
		if strings.HasPrefix(q, "CREATE INDEX") {
			return reply{err: errBoom}
		}
		return h(q, a)
	})
	applied, err := Migrate(ctx, conn, migrationFiles, ".")
	if err == nil || !strings.Contains(err.Error(), "001_users.sql") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("a failed file must not count as applied: %v", applied)
	}
	for _, q := range queriesOf(f) {
		if strings.HasPrefix(q, "INSERT INTO schema_migrations") || strings.Contains(q, "CREATE TABLE orders") {
			t.Fatalf("went on after the failure: %q", q)
		}
	}
	if got := queriesOf(f); got[len(got)-1] != "SELECT RELEASE_LOCK(?)" {
		t.Fatalf("lock not released after failure: %v", got)
	}
}

func TestMigrateLockTimeout(t *testing.T) {
	conn, _ := newFake(t, MySQL, func(q string, _ []driver.Value) reply {
		if strings.HasPrefix(q, "SELECT GET_LOCK") {
			return reply{cols: []string{"l"}, rows: [][]driver.Value{{int64(0)}}}
		}
		return reply{}
	})
	if _, err := Migrate(ctx, conn, migrationFiles, "."); err == nil || !strings.Contains(err.Error(), "another instance") {
		t.Fatalf("err = %v", err)
	}
}

func TestMigrateWithoutMigrationsDir(t *testing.T) {
	conn, _ := newFake(t, MySQL, migrateHandler())
	if _, err := Migrate(ctx, conn, fstest.MapFS{}, "nope"); err == nil {
		t.Fatal("missing directory should fail")
	}
}
