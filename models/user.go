package models

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"crayd-go/crayd/password"
)

var (
	ErrBadCredentials = errors.New("wrong email or password")
	ErrEmailTaken     = errors.New("email already registered")
)

type User struct {
	ID           int
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

// UserStore is implemented by the store package.
// Create returns ErrEmailTaken for duplicates; finders return ErrNotFound.
type UserStore interface {
	Create(ctx context.Context, u *User) error // sets u.ID
	Find(ctx context.Context, id int) (*User, error)
	FindByEmail(ctx context.Context, email string) (*User, error)
	UpdatePasswordHash(ctx context.Context, id int, hash string) error
}

type UserService struct{ Store UserStore }

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// Register validates input and creates a user with a hashed password.
func (s *UserService) Register(ctx context.Context, email, pw string) (*User, error) {
	email = normalizeEmail(email)
	errs := ValidationError{}
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
		errs["email"] = "Enter a valid email address"
	}
	switch {
	case len(pw) < 8:
		errs["password"] = "Use at least 8 characters"
	case len(pw) > 1024:
		errs["password"] = "That password is too long"
	}
	if len(errs) > 0 {
		return nil, errs
	}
	hash, err := password.Hash(pw)
	if err != nil {
		return nil, err
	}
	u := &User{Email: email, PasswordHash: hash, CreatedAt: time.Now()}
	if err := s.Store.Create(ctx, u); errors.Is(err, ErrEmailTaken) {
		return nil, ValidationError{"email": "That email is already registered"}
	} else if err != nil {
		return nil, err
	}
	return u, nil
}

// Authenticate returns the user for a correct email and password, or ErrBadCredentials.
// Unknown emails take as long as wrong passwords, so attackers can't probe for accounts by timing.
func (s *UserService) Authenticate(ctx context.Context, email, pw string) (*User, error) {
	u, err := s.Store.FindByEmail(ctx, normalizeEmail(email))
	if errors.Is(err, ErrNotFound) {
		password.Check("", pw)
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if !password.Check(u.PasswordHash, pw) {
		return nil, ErrBadCredentials
	}
	if password.NeedsRehash(u.PasswordHash) { // upgrade old hashes on login
		if h, err := password.Hash(pw); err == nil && s.Store.UpdatePasswordHash(ctx, u.ID, h) == nil {
			u.PasswordHash = h
		}
	}
	return u, nil
}

func (s *UserService) Get(ctx context.Context, id int) (*User, error) { return s.Store.Find(ctx, id) }
