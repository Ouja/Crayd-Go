package store

import (
	"context"
	"sync"

	"crayd-go/models"
)

// MemoryOrders is an in-process OrderStore. Good for the example app and tests;
// data is lost on restart. A SQL store arrives in Milestone 4.
type MemoryOrders struct {
	mu     sync.Mutex
	nextID int
	orders map[int]models.Order
}

func NewMemoryOrders() *MemoryOrders {
	return &MemoryOrders{nextID: 1, orders: map[int]models.Order{}}
}

func (m *MemoryOrders) Create(_ context.Context, o *models.Order) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o.ID = m.nextID
	m.nextID++
	m.orders[o.ID] = copyOrder(*o)
	return nil
}

func (m *MemoryOrders) Find(_ context.Context, id int) (*models.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orders[id]
	if !ok {
		return nil, models.ErrNotFound
	}
	c := copyOrder(o)
	return &c, nil
}

func (m *MemoryOrders) All(_ context.Context) ([]models.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.Order, 0, len(m.orders))
	for _, o := range m.orders {
		out = append(out, copyOrder(o))
	}
	return out, nil
}

func (m *MemoryOrders) Delete(_ context.Context, id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.orders[id]; !ok {
		return models.ErrNotFound
	}
	delete(m.orders, id)
	return nil
}

// copyOrder stops callers from sharing the Items slice with the store.
func copyOrder(o models.Order) models.Order {
	o.Items = append([]models.Item(nil), o.Items...)
	return o
}
