package models_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"crayd-go/crayd/password"
	"crayd-go/models"
	"crayd-go/store"
)

func init() { password.Iterations = 1000 }

func TestRegister(t *testing.T) {
	ctx := context.Background()
	svc := &models.UserService{Store: store.NewMemoryUsers()}

	u, err := svc.Register(ctx, "  Ada@Example.COM ", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "ada@example.com" || u.ID == 0 || strings.Contains(u.PasswordHash, "correct") {
		t.Fatalf("bad user %+v", u)
	}
	for _, tc := range []struct{ email, pw, field string }{
		{"ada@example.com", "another pw", "email"}, // taken (case-insensitive)
		{"nope", "long enough", "email"},
		{"Ada <ada@x.com>", "long enough", "email"},
		{"", "long enough", "email"},
		{"b@example.com", "short", "password"},
		{"b@example.com", strings.Repeat("x", 1025), "password"},
	} {
		_, err := svc.Register(ctx, tc.email, tc.pw)
		var verr models.ValidationError
		if !errors.As(err, &verr) || verr[tc.field] == "" {
			t.Errorf("Register(%q) = %v, want error on %s", tc.email, err, tc.field)
		}
	}
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	svc := &models.UserService{Store: store.NewMemoryUsers()}
	reg, _ := svc.Register(ctx, "ada@example.com", "correct horse")

	if u, err := svc.Authenticate(ctx, " ADA@example.com", "correct horse"); err != nil || u.ID != reg.ID {
		t.Fatalf("good login: %v %v", u, err)
	}
	for _, tc := range [][2]string{{"ada@example.com", "wrong"}, {"ada@example.com", ""}, {"ghost@example.com", "correct horse"}} {
		if _, err := svc.Authenticate(ctx, tc[0], tc[1]); !errors.Is(err, models.ErrBadCredentials) {
			t.Errorf("Authenticate(%q, %q) = %v", tc[0], tc[1], err)
		}
	}
}

func TestOldHashesUpgradedOnLogin(t *testing.T) {
	ctx := context.Background()
	users := store.NewMemoryUsers()
	svc := &models.UserService{Store: users}
	reg, _ := svc.Register(ctx, "ada@example.com", "correct horse")

	password.Iterations = 2000 // the app raised its security level
	defer func() { password.Iterations = 1000 }()
	if _, err := svc.Authenticate(ctx, "ada@example.com", "correct horse"); err != nil {
		t.Fatal(err)
	}
	after, _ := users.Find(ctx, reg.ID)
	if after.PasswordHash == reg.PasswordHash || password.NeedsRehash(after.PasswordHash) {
		t.Fatal("hash was not upgraded on login")
	}
}
