package store

import (
	"context"

	"crayd-go/crayd/db"
	"crayd-go/models"
)

// OrderSQL is the MySQL/MariaDB models.OrderStore. Tables: migrations/002_create_orders.sql.
// Items live in order_items and are deleted with their order (ON DELETE CASCADE).
type OrderSQL struct{ DB *db.DB }

func NewOrderSQL(conn *db.DB) *OrderSQL { return &OrderSQL{DB: conn} }

const (
	insertOrder = "INSERT INTO orders (customer, subtotal, discount, total, created_at) VALUES (?, ?, ?, ?, ?)"
	selectOrder = "SELECT id, customer, subtotal, discount, total, created_at FROM orders "
)

// itemsPerQuery caps how many order IDs go into one IN (...) list, to stay far below
// the 65,535 placeholder limit of MySQL. A variable so tests can make it tiny.
var itemsPerQuery = 500

// Create stores the order and its items in one transaction, then sets o.ID.
func (s *OrderSQL) Create(ctx context.Context, o *models.Order) error {
	var id int64
	err := s.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		if id, err = tx.Insert(ctx, insertOrder, o.Customer, o.Subtotal, o.Discount, o.Total, o.CreatedAt.UTC()); err != nil {
			return err
		}
		for i, it := range o.Items {
			if _, err := tx.Exec(ctx, "INSERT INTO order_items (order_id, line_no, name, qty, price) VALUES (?, ?, ?, ?, ?)",
				id, i, it.Name, it.Qty, it.Price); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return wrap("create order", err)
	}
	o.ID = int(id)
	return nil
}

func (s *OrderSQL) Find(ctx context.Context, id int) (*models.Order, error) {
	var o models.Order // columns map by name; Items has no column and is filled below
	if err := s.DB.Get(ctx, &o, selectOrder+"WHERE id = ?", id); err != nil {
		return nil, notFound(err)
	}
	if err := s.DB.Select(ctx, &o.Items, "SELECT name, qty, price FROM order_items WHERE order_id = ? ORDER BY line_no", id); err != nil {
		return nil, wrap("load items", err)
	}
	return &o, nil
}

// All returns every order, newest first, with their items.
func (s *OrderSQL) All(ctx context.Context) ([]models.Order, error) {
	var orders []models.Order
	if err := s.DB.Select(ctx, &orders, selectOrder+"ORDER BY id DESC"); err != nil {
		return nil, wrap("list orders", err)
	}
	pos := make(map[int]int, len(orders)) // order ID -> index in orders
	ids := make([]int, len(orders))
	for i, o := range orders {
		pos[o.ID], ids[i] = i, o.ID
	}
	for start := 0; start < len(ids); start += itemsPerQuery {
		chunk := ids[start:min(start+itemsPerQuery, len(ids))]
		var rows []struct {
			OrderID int
			models.Item
		}
		if err := s.DB.Select(ctx, &rows, "SELECT order_id, name, qty, price FROM order_items WHERE order_id IN (?) ORDER BY order_id, line_no", chunk); err != nil {
			return nil, wrap("load items", err)
		}
		for _, r := range rows {
			i := pos[r.OrderID]
			orders[i].Items = append(orders[i].Items, r.Item)
		}
	}
	return orders, nil
}

func (s *OrderSQL) Delete(ctx context.Context, id int) error {
	n, err := s.DB.Exec(ctx, "DELETE FROM orders WHERE id = ?", id)
	if err != nil {
		return wrap("delete order", err)
	}
	if n == 0 {
		return models.ErrNotFound
	}
	return nil
}
