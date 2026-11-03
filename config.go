package main

import (
	"time"

	"crayd-go/crayd"
)

// Config holds every setting the app reads from the environment (or a .env file).
type Config struct {
	Env             string        // APP_ENV: development (default) or production
	Addr            string        // ADDR, or ":" + PORT, default ":8080"
	RedisAddr       string        // REDIS_ADDR: cache and sessions in Redis
	DBDSN           string        // DB_DSN: user:pass@tcp(localhost:3306)/crayd (MySQL) or postgres://... ; empty = in-memory data
	AutoMigrate     bool          // AUTO_MIGRATE: apply new migrations at startup (default: on in development, off in production)
	SessionSecret   string        // SESSION_SECRET: encrypted-cookie sessions when there is no Redis
	InsecureCookies bool          // INSECURE_COOKIES: allow session cookies over plain HTTP (LAN testing)
	ShutdownTimeout time.Duration // SHUTDOWN_TIMEOUT: how long to wait for running requests on exit
	MaxBodySize     int64         // MAX_BODY_SIZE: largest request body accepted, e.g. 1MB (default) or 20MB for uploads
}

// Dev reports development mode: views reload when their files change.
func (c Config) Dev() bool { return c.Env == "development" }

// loadConfig reads and validates the environment, reporting all mistakes at once.
// Call crayd.LoadDotEnv first to include a .env file.
func loadConfig() (Config, error) {
	e := &crayd.Env{}
	c := Config{
		Env:             e.OneOf("APP_ENV", "development", "development", "production"),
		Addr:            e.String("ADDR", ""),
		RedisAddr:       e.String("REDIS_ADDR", ""),
		DBDSN:           e.String("DB_DSN", ""),
		SessionSecret:   e.String("SESSION_SECRET", ""),
		InsecureCookies: e.Bool("INSECURE_COOKIES", false),
		ShutdownTimeout: e.Duration("SHUTDOWN_TIMEOUT", 15*time.Second),
		MaxBodySize:     e.Size("MAX_BODY_SIZE", crayd.DefaultMaxBodyBytes),
	}
	c.AutoMigrate = e.Bool("AUTO_MIGRATE", c.Dev())
	if c.Addr == "" {
		c.Addr = ":8080"
		if port := e.String("PORT", ""); port != "" { // hosting platforms usually set PORT
			c.Addr = ":" + port
		}
	}
	if c.SessionSecret != "" && len(c.SessionSecret) < 32 {
		e.Add("SESSION_SECRET must be at least 32 characters; use a long random value")
	}
	if !c.Dev() {
		if c.RedisAddr == "" && c.SessionSecret == "" {
			e.Add("APP_ENV=production needs REDIS_ADDR or SESSION_SECRET, otherwise everyone is signed out on every restart")
		}
		if c.DBDSN == "" {
			e.Add("APP_ENV=production needs DB_DSN, otherwise users and orders are lost on every restart")
		}
		if c.InsecureCookies {
			e.Add("INSECURE_COOKIES must not be enabled in production")
		}
	}
	return c, e.Err()
}
