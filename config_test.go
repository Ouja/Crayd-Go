package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

var configVars = []string{"APP_ENV", "ADDR", "PORT", "REDIS_ADDR", "DB_DSN", "AUTO_MIGRATE", "SESSION_SECRET", "INSECURE_COOKIES", "SHUTDOWN_TIMEOUT", "MAX_BODY_SIZE"}

func cleanConfigEnv(t *testing.T) {
	for _, k := range configVars {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestConfigDefaults(t *testing.T) {
	cleanConfigEnv(t)
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Dev() || !c.AutoMigrate || c.DBDSN != "" || c.Addr != ":8080" || c.ShutdownTimeout != 15*time.Second || c.RedisAddr != "" || c.InsecureCookies || c.MaxBodySize != 1<<20 {
		t.Fatalf("%+v", c)
	}
}

func TestConfigPortAndAddr(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("PORT", "9000")
	if c, _ := loadConfig(); c.Addr != ":9000" {
		t.Errorf("PORT: %q", c.Addr)
	}
	t.Setenv("ADDR", "127.0.0.1:7000")
	if c, _ := loadConfig(); c.Addr != "127.0.0.1:7000" {
		t.Errorf("ADDR must win over PORT: %q", c.Addr)
	}
}

func TestConfigProduction(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("APP_ENV", "Production")

	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "REDIS_ADDR or SESSION_SECRET") {
		t.Fatalf("production without a session store: %v", err)
	}
	t.Setenv("REDIS_ADDR", "localhost:6379")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "DB_DSN") {
		t.Fatalf("production without a database: %v", err)
	}
	t.Setenv("DB_DSN", "u:p@tcp(localhost:3306)/crayd")
	c, err := loadConfig()
	if err != nil || c.Dev() || c.AutoMigrate {
		t.Fatalf("production must not migrate automatically by default: %+v %v", c, err)
	}
	t.Setenv("AUTO_MIGRATE", "yes")
	if c, _ := loadConfig(); !c.AutoMigrate {
		t.Fatal("AUTO_MIGRATE=yes should be honoured in production")
	}
	t.Setenv("INSECURE_COOKIES", "1")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "INSECURE_COOKIES") {
		t.Fatalf("insecure cookies in production: %v", err)
	}
}

func TestConfigAllProblemsAtOnce(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("APP_ENV", "staging")
	t.Setenv("SESSION_SECRET", "too-short")
	t.Setenv("SHUTDOWN_TIMEOUT", "soon")
	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"APP_ENV must be one of", "SESSION_SECRET must be at least 32", "SHUTDOWN_TIMEOUT must be a duration"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "too-short") {
		t.Error("secret value printed")
	}
}

func TestConfigMaxBodySize(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("MAX_BODY_SIZE", "20MB")
	if c, err := loadConfig(); err != nil || c.MaxBodySize != 20<<20 {
		t.Fatalf("%d %v", c.MaxBodySize, err)
	}
	t.Setenv("MAX_BODY_SIZE", "huge")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "MAX_BODY_SIZE") {
		t.Fatalf("bad size accepted: %v", err)
	}
}
