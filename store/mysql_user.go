package store

import (
	"context"

	"crayd-go/crayd/db"
	"crayd-go/models"
)

// UserSQL is the MySQL/MariaDB models.UserStore. Table: migrations/001_create_users.sql.
type UserSQL struct{ DB *db.DB }

func NewUserSQL(conn *db.DB) *UserSQL { return &UserSQL{DB: conn} }

const (
	insertUser = "INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)"
	selectUser = "SELECT id, email, password_hash, created_at FROM users "
)

func (s *UserSQL) Create(ctx context.Context, u *models.User) error {
	id, err := s.DB.Insert(ctx, insertUser, u.Email, u.PasswordHash, u.CreatedAt.UTC())
	if isDuplicateKey(err) {
		return models.ErrEmailTaken
	}
	if err != nil {
		return wrap("create user", err)
	}
	u.ID = int(id)
	return nil
}

func (s *UserSQL) Find(ctx context.Context, id int) (*models.User, error) {
	var u models.User
	if err := s.DB.Get(ctx, &u, selectUser+"WHERE id = ?", id); err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

func (s *UserSQL) FindByEmail(ctx context.Context, email string) (*models.User, error) {
	var u models.User
	if err := s.DB.Get(ctx, &u, selectUser+"WHERE email = ?", email); err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

func (s *UserSQL) UpdatePasswordHash(ctx context.Context, id int, hash string) error {
	n, err := s.DB.Exec(ctx, "UPDATE users SET password_hash = ? WHERE id = ?", hash, id)
	if err != nil {
		return wrap("update password", err)
	}
	if n == 0 {
		return models.ErrNotFound
	}
	return nil
}
