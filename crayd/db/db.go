// Package db is a small helper over database/sql: placeholders only, context
// everywhere, and rows scanned straight into structs. It has no HTTP code and
// no driver; import the driver you need in main or in your store package.
//
//	var u User
//	err := conn.Get(ctx, &u, "SELECT id, email FROM users WHERE id = ?", id)
//
//	var list []Order
//	err := conn.Select(ctx, &list, "SELECT * FROM orders WHERE id IN (?)", []int{1, 2, 3})
//
// Always write ? for parameters. For PostgreSQL they become $1, $2, ... automatically.
// A slice argument expands to ?, ?, ? (handy for IN). Write ?? for a literal question mark.
//
// Queries are of type Query, so only constant strings are accepted:
//
//	conn.Get(ctx, &u, "SELECT ... WHERE email = ?", email)   // compiles
//	conn.Get(ctx, &u, "SELECT ... WHERE email = '" + email + "'") // does not compile
//
// SQL that has to be assembled at runtime (from trusted pieces such as whitelisted
// column names) goes through db.Dynamic.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Query is SQL text. Untyped string constants convert to it implicitly;
// string variables don't, which is what keeps user input out of SQL.
type Query string

// Dynamic marks SQL built at runtime as trusted. Never pass user input to it:
// values still go in as ? arguments.
func Dynamic(sql string) Query { return Query(sql) }

// ErrNotFound is returned by Get when no row matches (it is sql.ErrNoRows).
var ErrNotFound = sql.ErrNoRows

// Dialect decides placeholder style and quoting rules.
type Dialect int

const (
	MySQL    Dialect = iota // ?        (also MariaDB)
	Postgres                // $1, $2   (lib/pq, pgx)
	SQLite                  // ?
)

// DB wraps *sql.DB. It is safe for concurrent use; create one in main and inject it.
type DB struct {
	sql     *sql.DB
	dialect Dialect
	runner
}

// Tx is a transaction with the same helpers as DB.
type Tx struct {
	tx *sql.Tx
	runner
}

// Open connects using a driver registered by an import such as
// _ "github.com/go-sql-driver/mysql" or _ "github.com/jackc/pgx/v5/stdlib",
// and checks the connection. The dialect is chosen from the driver name.
func Open(ctx context.Context, driver, dsn string) (*DB, error) {
	d, err := dialectFor(driver)
	if err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(25)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	return New(sqlDB, d), nil
}

// New wraps an existing *sql.DB.
func New(sqlDB *sql.DB, d Dialect) *DB {
	return &DB{sql: sqlDB, dialect: d, runner: runner{q: sqlDB, dialect: d}}
}

func dialectFor(driver string) (Dialect, error) {
	switch strings.ToLower(driver) {
	case "mysql":
		return MySQL, nil
	case "postgres", "pgx", "pgx/v5", "pq":
		return Postgres, nil
	case "sqlite", "sqlite3":
		return SQLite, nil
	}
	return 0, fmt.Errorf("db: unknown driver %q (use New with an explicit Dialect)", driver)
}

// SQL returns the underlying *sql.DB for anything this package doesn't cover.
func (db *DB) SQL() *sql.DB     { return db.sql }
func (db *DB) Dialect() Dialect { return db.dialect }
func (db *DB) Close() error     { return db.sql.Close() }

// Tx runs fn in a transaction: committed if fn returns nil, rolled back if it
// returns an error or panics.
func (db *DB) Tx(ctx context.Context, fn func(tx *Tx) error) (err error) {
	sqlTx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			sqlTx.Rollback()
			panic(p)
		}
	}()
	if err := fn(&Tx{tx: sqlTx, runner: runner{q: sqlTx, dialect: db.dialect}}); err != nil {
		if rbErr := sqlTx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("%w (rollback failed: %v)", err, rbErr)
		}
		return err
	}
	return sqlTx.Commit()
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type runner struct {
	q       querier
	dialect Dialect
}

// Get scans the first row into dst: a pointer to a struct, or to a single value
// such as *int for "SELECT COUNT(*)". Returns ErrNotFound when there is no row.
func (r runner) Get(ctx context.Context, dst any, q Query, args ...any) error {
	rows, err := r.Rows(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return ErrNotFound
	}
	if err := scanInto(rows, dst); err != nil {
		return err
	}
	return rows.Close()
}

// Select scans all rows into dst, a pointer to a slice of structs, struct
// pointers or single values (e.g. *[]Order, *[]*Order, *[]string).
// No rows gives an empty, non-nil slice.
func (r runner) Select(ctx context.Context, dst any, q Query, args ...any) error {
	rows, err := r.Rows(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if err := scanAll(rows, dst); err != nil {
		return err
	}
	return rows.Err()
}

// Exec runs a statement and returns the number of affected rows.
func (r runner) Exec(ctx context.Context, q Query, args ...any) (int64, error) {
	query, args, err := bind(r.dialect, string(q), args)
	if err != nil {
		return 0, err
	}
	res, err := r.q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Insert runs an INSERT and returns the new row's ID. On PostgreSQL the query
// must end with "RETURNING id"; on MySQL and SQLite the driver's LastInsertId is used.
func (r runner) Insert(ctx context.Context, q Query, args ...any) (int64, error) {
	if r.dialect == Postgres {
		var id int64
		err := r.Get(ctx, &id, q, args...)
		if errors.Is(err, ErrNotFound) {
			return 0, errors.New(`db: Insert on PostgreSQL needs "RETURNING id"`)
		}
		return id, err
	}
	query, args, err := bind(r.dialect, string(q), args)
	if err != nil {
		return 0, err
	}
	res, err := r.q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Rows runs a query and returns the raw rows, for streaming large results.
// Close them when done.
func (r runner) Rows(ctx context.Context, q Query, args ...any) (*sql.Rows, error) {
	query, args, err := bind(r.dialect, string(q), args)
	if err != nil {
		return nil, err
	}
	return r.q.QueryContext(ctx, query, args...)
}

// ScanRow scans the current row of rows (from Rows) into dst, like Get does.
func ScanRow(rows *sql.Rows, dst any) error { return scanInto(rows, dst) }
