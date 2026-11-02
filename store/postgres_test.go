package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lib/pq"
)

func TestIsPostgresDSN(t *testing.T) {
	for dsn, want := range map[string]bool{
		"postgres://app:pw@localhost:5432/crayd":          true,
		"postgresql://app:pw@db/crayd?sslmode=disable":    true,
		"postgres://app:p%zz@db/crayd":                    true, // invalid URL, still recognised as PostgreSQL
		"app:pw@tcp(localhost:3306)/crayd":                false,
		"app:pw@tcp(localhost:3306)/crayd?parseTime=true": false,
		"mysql://app:pw@localhost/crayd":                  false,
	} {
		if got := isPostgresDSN(dsn); got != want {
			t.Errorf("isPostgresDSN(%q) = %v, want %v", dsn, got, want)
		}
	}
}

func TestOpenPostgresErrorsDoNotLeakThePassword(t *testing.T) {
	for _, dsn := range []string{
		"postgres://app:hunter2-secret@db:5432/crayd%zz", // bad escape
		"postgres://app:hunter2-secret@/crayd",           // no host
		"postgres://app:hunter2-secret@db:5432",          // no database
	} {
		_, err := Open(context.Background(), dsn)
		if err == nil {
			t.Fatalf("%q: expected an error", dsn)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("error leaks the password: %v", err)
		}
	}
}

func TestIsDuplicateKeyPostgres(t *testing.T) {
	dup := &pq.Error{Code: "23505"}
	if !isDuplicateKey(dup) || !isDuplicateKey(fmt.Errorf("create user: %w", dup)) {
		t.Fatal("unique violation not recognised")
	}
	if isDuplicateKey(&pq.Error{Code: "23503"}) { // foreign key violation
		t.Fatal("other constraint errors are not duplicates")
	}
}
