package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"unicode"
)

// Migrate applies the *.sql files in dir of fsys that have not run yet, in file name
// order (001_create_users.sql, 002_...), and returns the versions it applied.
// Applied versions are recorded in the table schema_migrations.
//
// Embed the files to deploy a single binary:
//
//	//go:embed *.sql
//	var FS embed.FS
//	applied, err := db.Migrate(ctx, conn, migrations.FS, ".")
//
// A file may hold several statements separated by ";". Not supported: DELIMITER,
// PostgreSQL $$ bodies. Statements are sent as written, so "?" is not a placeholder here.
//
// A lock keeps two app instances from migrating at the same time (MySQL GET_LOCK,
// PostgreSQL advisory lock). PostgreSQL and SQLite run each file in a transaction.
// MySQL cannot roll back DDL: if a file fails halfway, fix the database by hand and
// run again; the failed file is not recorded as applied.
func Migrate(ctx context.Context, d *DB, fsys fs.FS, dir string) (applied []string, err error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}

	// Locks belong to one connection, so do everything on a single one.
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	r := runner{q: conn, dialect: d.dialect}

	unlock, err := lockMigrations(ctx, r)
	if err != nil {
		return nil, err
	}
	defer func() {
		if uerr := unlock(); uerr != nil && err == nil {
			err = uerr
		}
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(255) NOT NULL PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	var done []string
	if err := r.Select(ctx, &done, "SELECT version FROM schema_migrations"); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	have := make(map[string]bool, len(done))
	for _, v := range done {
		have[v] = true
	}

	for _, f := range files {
		version := strings.TrimSuffix(f, ".sql")
		if have[version] {
			continue
		}
		body, err := fs.ReadFile(fsys, path.Join(dir, f))
		if err != nil {
			return applied, err
		}
		if err := applyMigration(ctx, conn, d.dialect, version, string(body)); err != nil {
			return applied, fmt.Errorf("migrate %s: %w", f, err)
		}
		applied = append(applied, version)
	}
	return applied, nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, d Dialect, version, body string) error {
	run := func(q querier) error {
		for _, stmt := range splitStatements(d, body) {
			if _, err := q.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		_, err := runner{q: q, dialect: d}.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", version)
		return err
	}
	if d == MySQL { // DDL commits implicitly there; a transaction would only pretend
		return run(conn)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := run(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

const migrationLockName = "crayd_migrate"

func lockMigrations(ctx context.Context, r runner) (unlock func() error, err error) {
	switch r.dialect {
	case MySQL:
		var got sql.NullInt64
		if err := r.Get(ctx, &got, "SELECT GET_LOCK(?, 30)", migrationLockName); err != nil {
			return nil, fmt.Errorf("migrate: lock: %w", err)
		}
		if !got.Valid || got.Int64 != 1 {
			return nil, errors.New("migrate: another instance is migrating (could not get the lock in 30s)")
		}
		return func() error {
			var x sql.NullInt64
			return r.Get(context.WithoutCancel(ctx), &x, "SELECT RELEASE_LOCK(?)", migrationLockName)
		}, nil
	case Postgres:
		if _, err := r.q.ExecContext(ctx, "SELECT pg_advisory_lock(727274)"); err != nil {
			return nil, fmt.Errorf("migrate: lock: %w", err)
		}
		return func() error {
			_, err := r.q.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(727274)")
			return err
		}, nil
	}
	return func() error { return nil }, nil
}

// splitStatements cuts SQL text at ";" outside quotes and comments. Comments stay in the
// statement they belong to; pieces with no SQL in them are dropped.
func splitStatements(d Dialect, src string) []string {
	var out []string
	start, hasCode := 0, false
	flush := func(end int) {
		if hasCode {
			out = append(out, strings.TrimSpace(src[start:end]))
		}
		start, hasCode = end, false
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			hasCode = true
			i = skipQuoted(d, src, i) - 1
		case c == '-' && i+1 < len(src) && src[i+1] == '-',
			c == '#' && d == MySQL:
			if j := strings.IndexByte(src[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(src)
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			if j := strings.Index(src[i+2:], "*/"); j >= 0 {
				i += j + 3
			} else {
				i = len(src)
			}
		case c == ';':
			flush(i)
			start = i + 1
		case !unicode.IsSpace(rune(c)):
			hasCode = true
		}
	}
	flush(len(src))
	return out
}
