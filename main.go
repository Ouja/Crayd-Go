package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"crayd-go/controllers"
	"crayd-go/crayd"
	"crayd-go/crayd/db"
	"crayd-go/migrations"
	"crayd-go/models"
	"crayd-go/store"
)

// Deps are the long-lived services, created once in main.
type Deps struct {
	Cache    models.Cache
	Sessions crayd.SessionStore
	Users    models.UserStore
	Orders   models.OrderStore
	Insecure bool // allow session cookies over plain HTTP (LAN testing only)
	Dev      bool // reload views when their files change
}

// buildApp wires controllers to services. Tests call it with in-memory deps.
func buildApp(d Deps) *crayd.App {
	users := &models.UserService{Store: d.Users}
	orders := &models.OrderService{Store: d.Orders, Cache: d.Cache}

	app := crayd.New("app/views")
	app.Dev(d.Dev)
	app.Use(crayd.Logger)
	app.Sessions(crayd.SessionOptions{Store: d.Sessions, Insecure: d.Insecure})
	app.Register("index", &controllers.Index{})
	app.Register("user", &controllers.User{})
	app.Register("auth", &controllers.Auth{Users: users})
	app.Register("order", &controllers.Order{Orders: orders})
	return app
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run returns instead of exiting, so cleanup always happens.
//
//	go run .            start the server
//	go run . migrate    apply new database migrations and exit (for deploys)
func run() error {
	if err := crayd.LoadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if len(os.Args) > 1 {
		if os.Args[1] == "migrate" {
			return runMigrate(cfg)
		}
		return fmt.Errorf("unknown command %q (available: migrate)", os.Args[1])
	}

	d := Deps{
		Cache:    store.NewMemoryCache(),
		Sessions: crayd.ServerSessions(crayd.NewMemorySessionBackend()),
		Users:    store.NewMemoryUsers(),
		Orders:   store.NewMemoryOrders(),
		Insecure: cfg.InsecureCookies,
		Dev:      cfg.Dev(),
	}

	// Everything opened here is closed when the app stops, or right away if startup fails.
	var closers []func() error
	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}

	if cfg.DBDSN != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		conn, err := store.OpenMySQL(ctx, cfg.DBDSN)
		if err != nil {
			return err
		}
		closers = append(closers, conn.Close)
		if cfg.AutoMigrate {
			if err := migrateAndLog(ctx, conn); err != nil {
				closeAll()
				return err
			}
		}
		d.Users, d.Orders = store.NewUserSQL(conn), store.NewOrderSQL(conn)
		log.Println("data: MySQL/MariaDB")
	} else {
		log.Println("data: in-memory (set DB_DSN to keep users and orders)")
	}

	switch {
	case cfg.RedisAddr != "":
		rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
		closers = append(closers, rdb.Close)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := rdb.Ping(ctx).Err(); err != nil {
			closeAll()
			return fmt.Errorf("redis at %s: %w", cfg.RedisAddr, err)
		}
		d.Cache = store.NewRedisCache(rdb)
		d.Sessions = crayd.ServerSessions(store.NewRedisSessions(rdb))
		log.Println("cache + sessions: redis at", cfg.RedisAddr)
	case cfg.SessionSecret != "":
		d.Sessions = crayd.CookieSessions([]byte(cfg.SessionSecret))
		log.Println("cache: in-memory; sessions: encrypted cookies")
	default:
		log.Println("cache + sessions: in-memory (set REDIS_ADDR or SESSION_SECRET to persist sessions)")
	}
	log.Println("mode:", cfg.Env)

	app := buildApp(d)
	app.ShutdownTimeout = cfg.ShutdownTimeout
	for _, c := range closers {
		app.OnShutdown(func(context.Context) error { return c() })
	}
	return app.Run(cfg.Addr)
}

func runMigrate(cfg Config) error {
	if cfg.DBDSN == "" {
		return fmt.Errorf("DB_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := store.OpenMySQL(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer conn.Close()
	return migrateAndLog(ctx, conn)
}

func migrateAndLog(ctx context.Context, conn *db.DB) error {
	applied, err := db.Migrate(ctx, conn, migrations.FS, ".")
	if len(applied) > 0 {
		log.Println("migrations applied:", strings.Join(applied, ", "))
	}
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		log.Println("migrations: database is up to date")
	}
	return nil
}
