package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"crayd-go/crayd/db"
	"crayd-go/crayd/password"
	"crayd-go/migrations"
	"crayd-go/models"
)

// Runs only when MYSQL_TEST_DSN points at a scratch database whose name ends in _test.
// It DROPS the app's tables first, so it refuses any other database.
//
//	create database crayd_test;
//	MYSQL_TEST_DSN="root:secret@tcp(localhost:3306)/crayd_test" go test ./store -run MySQL -v
func TestMySQLStores(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_TEST_DSN (database name must end in _test) to run against a real MySQL/MariaDB")
	}
	ctx := context.Background()
	conn, err := OpenMySQL(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var name string
	if err := conn.Get(ctx, &name, "SELECT DATABASE()"); err != nil || !strings.HasSuffix(name, "_test") {
		t.Fatalf("refusing to run: database %q (%v) does not end in _test", name, err)
	}
	for _, tbl := range []string{"order_items", "orders", "users", "schema_migrations"} {
		if _, err := conn.Exec(ctx, db.Dynamic("DROP TABLE IF EXISTS "+tbl)); err != nil {
			t.Fatal(err)
		}
	}

	applied, err := db.Migrate(ctx, conn, migrations.FS, ".")
	if err != nil || len(applied) != 2 {
		t.Fatalf("first migrate: %v %v", applied, err)
	}
	if again, err := db.Migrate(ctx, conn, migrations.FS, "."); err != nil || len(again) != 0 {
		t.Fatalf("second migrate must do nothing: %v %v", again, err)
	}

	exerciseStores(t, conn)
}

// Runs only when POSTGRES_TEST_DSN points at a scratch database whose name ends in _test.
// It DROPS the app's tables first, so it refuses any other database.
//
//	createdb crayd_test
//	POSTGRES_TEST_DSN="postgres://postgres:secret@localhost:5432/crayd_test?sslmode=disable" go test ./store -run Postgres -v
func TestPostgresStores(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN (database name must end in _test) to run against a real PostgreSQL")
	}
	ctx := context.Background()
	conn, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.Dialect() != db.Postgres {
		t.Fatal("Open did not pick PostgreSQL")
	}

	var name string
	if err := conn.Get(ctx, &name, "SELECT current_database()"); err != nil || !strings.HasSuffix(name, "_test") {
		t.Fatalf("refusing to run: database %q (%v) does not end in _test", name, err)
	}
	for _, tbl := range []string{"order_items", "orders", "users", "schema_migrations"} {
		if _, err := conn.Exec(ctx, db.Dynamic("DROP TABLE IF EXISTS "+tbl+" CASCADE")); err != nil {
			t.Fatal(err)
		}
	}

	dir := migrations.Dir(conn.Dialect())
	applied, err := db.Migrate(ctx, conn, migrations.FS, dir)
	if err != nil || len(applied) != 2 {
		t.Fatalf("first migrate: %v %v", applied, err)
	}
	if again, err := db.Migrate(ctx, conn, migrations.FS, dir); err != nil || len(again) != 0 {
		t.Fatalf("second migrate must do nothing: %v %v", again, err)
	}

	exerciseStores(t, conn)
}

// exerciseStores runs the same checks on any database the stores' SQL works on.
func exerciseStores(t *testing.T, conn *db.DB) {
	ctx := context.Background()
	password.Iterations = 1000
	users, orders := NewUserSQL(conn), NewOrderSQL(conn)

	t.Run("users", func(t *testing.T) {
		svc := &models.UserService{Store: users}
		ada, err := svc.Register(ctx, "Ada@Example.com", "correct horse")
		if err != nil || ada.ID == 0 {
			t.Fatalf("register: %+v %v", ada, err)
		}
		// duplicate email -> a friendly validation error, not a 500
		_, err = svc.Register(ctx, "ada@example.com", "another password")
		var verr models.ValidationError
		if !errors.As(err, &verr) || verr["email"] == "" {
			t.Fatalf("duplicate: %v", err)
		}
		if got, err := svc.Authenticate(ctx, "ADA@example.com", "correct horse"); err != nil || got.ID != ada.ID {
			t.Fatalf("authenticate: %v %v", got, err)
		}
		if _, err := svc.Authenticate(ctx, "ada@example.com", "wrong"); !errors.Is(err, models.ErrBadCredentials) {
			t.Fatalf("wrong password: %v", err)
		}
		found, err := users.Find(ctx, ada.ID)
		if err != nil || found.Email != "ada@example.com" || found.PasswordHash != ada.PasswordHash ||
			!isUTC(found.CreatedAt) || time.Since(found.CreatedAt) > time.Minute {
			t.Fatalf("find: %+v %v", found, err)
		}
		if _, err := users.Find(ctx, 987654); !errors.Is(err, models.ErrNotFound) {
			t.Fatalf("find missing: %v", err)
		}
		if _, err := users.FindByEmail(ctx, "nobody@example.com"); !errors.Is(err, models.ErrNotFound) {
			t.Fatalf("find by email missing: %v", err)
		}
		// setting the hash it already has must not look like "not found"
		if err := users.UpdatePasswordHash(ctx, ada.ID, ada.PasswordHash); err != nil {
			t.Fatalf("same-value update: %v", err)
		}
		if err := users.UpdatePasswordHash(ctx, 987654, "x"); !errors.Is(err, models.ErrNotFound) {
			t.Fatalf("update missing: %v", err)
		}
	})

	t.Run("orders", func(t *testing.T) {
		svc := &models.OrderService{Store: orders}
		first, err := svc.Checkout(ctx, "Zoë <b>", []models.Item{
			{Name: "Keyboard", Qty: 2, Price: 6000}, {Name: "Cable 'x'; --", Qty: 1, Price: 500}})
		if err != nil || first.ID == 0 {
			t.Fatalf("checkout: %+v %v", first, err)
		}
		second, _ := svc.Checkout(ctx, "Bob", []models.Item{{Name: "Pen", Qty: 3, Price: 150}})
		third, _ := svc.Checkout(ctx, "Cy", []models.Item{{Name: "A", Qty: 1, Price: 1}, {Name: "B", Qty: 1, Price: 2}, {Name: "C", Qty: 1, Price: 3}})

		got, err := svc.Get(ctx, first.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Customer != "Zoë <b>" || got.Subtotal != 12500 || got.Discount != 1250 || got.Total != 11250 ||
			len(got.Items) != 2 || got.Items[0].Name != "Keyboard" || got.Items[1].Name != "Cable 'x'; --" || got.Items[0].Price != 6000 {
			t.Fatalf("round trip lost data: %+v", got)
		}
		if time.Since(got.CreatedAt) > time.Minute || !isUTC(got.CreatedAt) {
			t.Fatalf("created_at: %v", got.CreatedAt)
		}

		// All: newest first, items attached to the right orders, also with tiny IN-chunks
		for _, chunk := range []int{500, 2, 1} {
			itemsPerQuery = chunk
			list, err := svc.List(ctx)
			if err != nil || len(list) != 3 {
				t.Fatalf("chunk %d: list: %d orders, %v", chunk, len(list), err)
			}
			if list[0].ID != third.ID || len(list[0].Items) != 3 || list[0].Items[2].Name != "C" ||
				list[1].ID != second.ID || len(list[1].Items) != 1 || list[1].Items[0].Name != "Pen" ||
				list[2].ID != first.ID || len(list[2].Items) != 2 {
				t.Fatalf("chunk %d: items mixed up: %+v", chunk, list)
			}
		}
		itemsPerQuery = 500

		if _, err := svc.Get(ctx, 987654); !errors.Is(err, models.ErrNotFound) {
			t.Fatalf("get missing: %v", err)
		}
		if err := svc.Delete(ctx, first.ID); err != nil {
			t.Fatal(err)
		}
		if err := svc.Delete(ctx, first.ID); !errors.Is(err, models.ErrNotFound) {
			t.Fatalf("delete twice: %v", err)
		}
		var leftovers int
		if err := conn.Get(ctx, &leftovers, "SELECT COUNT(*) FROM order_items WHERE order_id = ?", first.ID); err != nil || leftovers != 0 {
			t.Fatalf("items of a deleted order survived: %d %v", leftovers, err)
		}
		if list, _ := svc.List(ctx); len(list) != 2 {
			t.Fatalf("after delete: %d orders", len(list))
		}
	})

	t.Run("create is atomic", func(t *testing.T) {
		before := countOrders(t, conn)
		// an item the database rejects (name too long for the column) must not leave a half-stored order
		bad := &models.Order{Customer: "Atomic", Items: []models.Item{
			{Name: "fine", Qty: 1, Price: 1}, {Name: strings.Repeat("x", 300), Qty: 1, Price: 1}}, CreatedAt: time.Now()}
		if err := orders.Create(ctx, bad); err == nil {
			t.Skip("this database accepted an over-long value (non-strict SQL mode); atomicity cannot be shown this way")
		}
		if bad.ID != 0 {
			t.Fatalf("ID set on a failed create: %d", bad.ID)
		}
		if after := countOrders(t, conn); after != before {
			t.Fatalf("half-stored order left behind: %d -> %d orders", before, after)
		}
	})
}

func isUTC(t time.Time) bool { _, off := t.Zone(); return off == 0 }

func countOrders(t *testing.T, conn *db.DB) int {
	t.Helper()
	var n int
	if err := conn.Get(context.Background(), &n, "SELECT COUNT(*) FROM orders"); err != nil {
		t.Fatal(err)
	}
	return n
}
