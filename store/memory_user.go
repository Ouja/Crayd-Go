package store

import (
	"context"
	"sync"

	"crayd-go/models"
)

// MemoryUsers is an in-process UserStore (lost on restart). SQL arrives in Milestone 4.
type MemoryUsers struct {
	mu      sync.Mutex
	nextID  int
	byID    map[int]models.User
	byEmail map[string]int
}

func NewMemoryUsers() *MemoryUsers {
	return &MemoryUsers{nextID: 1, byID: map[int]models.User{}, byEmail: map[string]int{}}
}

func (m *MemoryUsers) Create(_ context.Context, u *models.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.byEmail[u.Email]; taken {
		return models.ErrEmailTaken
	}
	u.ID = m.nextID
	m.nextID++
	m.byID[u.ID] = *u
	m.byEmail[u.Email] = u.ID
	return nil
}

func (m *MemoryUsers) Find(_ context.Context, id int) (*models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return nil, models.ErrNotFound
	}
	return &u, nil
}

func (m *MemoryUsers) FindByEmail(ctx context.Context, email string) (*models.User, error) {
	m.mu.Lock()
	id, ok := m.byEmail[email]
	m.mu.Unlock()
	if !ok {
		return nil, models.ErrNotFound
	}
	return m.Find(ctx, id)
}

func (m *MemoryUsers) UpdatePasswordHash(_ context.Context, id int, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return models.ErrNotFound
	}
	u.PasswordHash = hash
	m.byID[id] = u
	return nil
}
