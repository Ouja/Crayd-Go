// Package migrations holds the SQL files that create and change the database schema.
// They are embedded in the binary and applied in file name order by `go run . migrate`
// (or automatically at startup when AUTO_MIGRATE is on). Add new files; never edit
// one that has already run somewhere.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
