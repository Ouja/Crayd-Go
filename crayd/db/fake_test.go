package db

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// A minimal database/sql driver for tests: it records every statement and
// answers queries from a handler, so we can check exactly what reaches the database.

type call struct {
	query string
	args  []driver.Value
}

type reply struct {
	cols   []string
	rows   [][]driver.Value
	lastID int64
	n      int64
	err    error
}

type fakeDB struct {
	mu        sync.Mutex
	calls     []call
	handler   func(query string, args []driver.Value) reply
	commits   int
	rollbacks int
}

func (f *fakeDB) record(q string, args []driver.Value) reply {
	f.mu.Lock()
	f.calls = append(f.calls, call{q, args})
	f.mu.Unlock()
	if f.handler == nil {
		return reply{}
	}
	return f.handler(q, args)
}

func (f *fakeDB) last() call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

var (
	fakes   sync.Map
	fakeSeq atomic.Int64
)

func init() { sql.Register("crayd-fake", fakeDriver{}) }

// newFake returns a DB backed by a fresh fake for the given dialect.
func newFake(t *testing.T, d Dialect, h func(q string, args []driver.Value) reply) (*DB, *fakeDB) {
	t.Helper()
	f := &fakeDB{handler: h}
	dsn := strconv.FormatInt(fakeSeq.Add(1), 10)
	fakes.Store(dsn, f)
	sqlDB, err := sql.Open("crayd-fake", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return New(sqlDB, d), f
}

// rowsReply is a handler that always returns the same rows.
func rowsReply(cols []string, rows ...[]driver.Value) func(string, []driver.Value) reply {
	return func(string, []driver.Value) reply { return reply{cols: cols, rows: rows} }
}

type fakeDriver struct{}

func (fakeDriver) Open(dsn string) (driver.Conn, error) {
	f, ok := fakes.Load(dsn)
	if !ok {
		return nil, errors.New("unknown fake")
	}
	return &fakeConn{f.(*fakeDB)}, nil
}

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{c.db, q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return &fakeTx{c.db}, nil }

type fakeTx struct{ db *fakeDB }

func (t *fakeTx) Commit() error   { t.db.mu.Lock(); t.db.commits++; t.db.mu.Unlock(); return nil }
func (t *fakeTx) Rollback() error { t.db.mu.Lock(); t.db.rollbacks++; t.db.mu.Unlock(); return nil }

type fakeStmt struct {
	db *fakeDB
	q  string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	r := s.db.record(s.q, args)
	return fakeResult{r.lastID, r.n}, r.err
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	r := s.db.record(s.q, args)
	if r.err != nil {
		return nil, r.err
	}
	return &fakeRows{cols: r.cols, rows: r.rows}, nil
}

type fakeResult struct{ id, n int64 }

func (r fakeResult) LastInsertId() (int64, error) { return r.id, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.n, nil }

type fakeRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}
