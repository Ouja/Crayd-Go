package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"

	"crayd-go/crayd/db"
	"crayd-go/models"
)

// OpenMySQL connects to MySQL or MariaDB. The DSN looks like
//
//	user:password@tcp(localhost:3306)/crayd
//
// and is adjusted to what the stores need, so a forgotten option can't bite later:
//   - parseTime=true and loc=UTC: DATETIME columns become time.Time, stored as UTC
//   - clientFoundRows=true: "rows affected" counts matched rows, so updating a row to
//     the value it already has is not mistaken for "no such row"
func OpenMySQL(ctx context.Context, dsn string) (*db.DB, error) {
	full, err := normalizeMySQLDSN(dsn)
	if err != nil {
		return nil, err
	}
	return db.Open(ctx, "mysql", full)
}

func normalizeMySQLDSN(dsn string) (string, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", errors.New("DB_DSN is not a valid MySQL DSN (expected user:password@tcp(host:3306)/database)")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.ClientFoundRows = true
	cfg.MultiStatements = false // keeps one query = one statement, whatever the DSN says
	return cfg.FormatDSN(), nil
}

// isDuplicateKey reports MySQL error 1062 (a UNIQUE index was violated).
func isDuplicateKey(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

// notFound turns the db package's "no row" into the models package's.
func notFound(err error) error {
	if errors.Is(err, db.ErrNotFound) {
		return models.ErrNotFound
	}
	return err
}

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}
