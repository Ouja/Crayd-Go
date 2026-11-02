// Package migrations holds the SQL files that create and change the database schema:
// MySQL/MariaDB files in this folder, PostgreSQL files in postgres/. They are embedded
// in the binary and applied in file name order by `go run . migrate` (or at startup
// when AUTO_MIGRATE is on). Add new files; never edit one that has already run somewhere.
package migrations

import (
	"embed"

	"crayd-go/crayd/db"
)

//go:embed *.sql postgres/*.sql
var FS embed.FS

// Dir returns the folder in FS with the migrations for the database's dialect.
func Dir(d db.Dialect) string {
	if d == db.Postgres {
		return "postgres"
	}
	return "."
}
