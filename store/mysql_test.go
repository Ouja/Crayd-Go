package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestNormalizeMySQLDSN(t *testing.T) {
	out, err := normalizeMySQLDSN("app:s3cr3t@tcp(db.internal:3306)/crayd?charset=utf8mb4&timeout=5s&parseTime=false&loc=Asia%2FJakarta&multiStatements=true")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(out)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ParseTime || cfg.Loc != time.UTC || !cfg.ClientFoundRows || cfg.MultiStatements {
		t.Fatalf("required options not enforced: %+v", cfg)
	}
	if cfg.User != "app" || cfg.Passwd != "s3cr3t" || cfg.Addr != "db.internal:3306" || cfg.DBName != "crayd" || cfg.Timeout != 5*time.Second {
		t.Fatalf("user's own settings lost: %+v", cfg)
	}
}

func TestNormalizeMySQLDSNErrorsDoNotLeakThePassword(t *testing.T) {
	_, err := normalizeMySQLDSN("app:hunter2-secret@tcp(db:3306")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestIsDuplicateKey(t *testing.T) {
	dup := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'x' for key 'uq_users_email'"}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"duplicate":         {dup, true},
		"wrapped duplicate": {fmt.Errorf("create user: %w", dup), true},
		"other mysql error": {&mysql.MySQLError{Number: 1064}, false},
		"plain error":       {errors.New("boom"), false},
		"nil":               {nil, false},
	} {
		if got := isDuplicateKey(tc.err); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}
