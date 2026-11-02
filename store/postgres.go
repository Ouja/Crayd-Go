package store

import (
	"context"
	"errors"
	"net/url"

	"github.com/lib/pq"

	"crayd-go/crayd/db"
)

// OpenPostgres connects to PostgreSQL. The DSN is a URL:
//
//	postgres://user:password@localhost:5432/crayd?sslmode=disable
//
// Leave out sslmode=disable when the server uses TLS (the default is to require it).
func OpenPostgres(ctx context.Context, dsn string) (*db.DB, error) {
	if !isPostgresDSN(dsn) {
		return nil, errors.New("DB_DSN is not a PostgreSQL URL (expected postgres://user:password@host:5432/database)")
	}
	// url.Parse errors quote the whole string, password included, so report without it.
	if u, err := url.Parse(dsn); err != nil || u.Host == "" || len(u.Path) < 2 {
		return nil, errors.New("DB_DSN is not a valid PostgreSQL URL (expected postgres://user:password@host:5432/database)")
	}
	return db.Open(ctx, "postgres", dsn)
}

// Open picks the database from the DSN: a postgres:// URL opens PostgreSQL,
// anything else is read as a MySQL/MariaDB DSN.
func Open(ctx context.Context, dsn string) (*db.DB, error) {
	if isPostgresDSN(dsn) {
		return OpenPostgres(ctx, dsn)
	}
	return OpenMySQL(ctx, dsn)
}

func isPostgresDSN(dsn string) bool {
	u, err := url.Parse(dsn)
	if err != nil {
		// still decide by the scheme, so the error message names the right database
		for _, p := range []string{"postgres://", "postgresql://"} {
			if len(dsn) >= len(p) && dsn[:len(p)] == p {
				return true
			}
		}
		return false
	}
	return u.Scheme == "postgres" || u.Scheme == "postgresql"
}

func isPostgresDuplicate(err error) bool {
	var pe *pq.Error
	return errors.As(err, &pe) && pe.Code == "23505" // unique_violation
}
