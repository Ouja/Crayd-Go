package models

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Plain Go: no HTTP, no SQL, no Redis. Everything here runs from a test or a CLI.

var ErrNotFound = errors.New("not found")

// ValidationError maps a field name to a message the user can read.
type ValidationError map[string]string

func (v ValidationError) Error() string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ": " + v[k]
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

type Item struct {
	Name  string
	Qty   int
	Price Money // per unit
}

type Order struct {
	ID        int
	Customer  string
	Items     []Item
	Subtotal  Money
	Discount  Money
	Total     Money
	CreatedAt time.Time
}

// OrderStore is implemented by the store package (memory, SQL, ...).
// Implementations return ErrNotFound when an order does not exist.
type OrderStore interface {
	Create(ctx context.Context, o *Order) error // sets o.ID
	Find(ctx context.Context, id int) (*Order, error)
	All(ctx context.Context) ([]Order, error)
	Delete(ctx context.Context, id int) error
}

const (
	maxItems          = 20
	maxQty            = 1000
	maxTextLen        = 255          // characters; matches the VARCHAR(255) columns
	discountThreshold = Money(10000) // $100.00
	cacheTTL          = 10 * time.Minute
)

// OrderService holds the business rules. Build it once in main and inject it.
type OrderService struct {
	Store OrderStore
	Cache Cache            // optional; nil disables caching
	Now   func() time.Time // optional; defaults to time.Now
}

func (s *OrderService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func orderKey(id int) string { return fmt.Sprintf("order:%d", id) }

// Checkout validates the cart, applies the discount rule and stores the order.
// Invalid input returns a ValidationError.
func (s *OrderService) Checkout(ctx context.Context, customer string, items []Item) (*Order, error) {
	errs := ValidationError{}
	customer = strings.TrimSpace(customer)
	switch {
	case customer == "":
		errs["customer"] = "Customer is required"
	case utf8.RuneCountInString(customer) > maxTextLen:
		errs["customer"] = fmt.Sprintf("Customer is too long (%d characters at most)", maxTextLen)
	}
	switch {
	case len(items) == 0:
		errs["items"] = "Add at least one item"
	case len(items) > maxItems:
		errs["items"] = fmt.Sprintf("At most %d items per order", maxItems)
	default:
		for _, it := range items {
			name := strings.TrimSpace(it.Name)
			if name == "" || utf8.RuneCountInString(name) > maxTextLen || it.Qty < 1 || it.Qty > maxQty || it.Price < 0 {
				errs["items"] = "Each item needs a name (255 characters at most), a quantity from 1 to 1000 and a price"
				break
			}
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}

	o := &Order{Customer: customer, CreatedAt: s.now()}
	for _, it := range items {
		it.Name = strings.TrimSpace(it.Name)
		o.Items = append(o.Items, it)
		o.Subtotal += it.Price * Money(it.Qty)
	}
	o.Discount = discountFor(o.Subtotal)
	o.Total = o.Subtotal - o.Discount

	if err := s.Store.Create(ctx, o); err != nil {
		return nil, fmt.Errorf("checkout: %w", err)
	}
	return o, nil
}

// discountFor: 10% off orders of $100.00 or more.
func discountFor(subtotal Money) Money {
	if subtotal >= discountThreshold {
		return subtotal / 10
	}
	return 0
}

// Get returns one order, reading through the cache.
// The cache is best effort: if it fails we fall back to the store.
func (s *OrderService) Get(ctx context.Context, id int) (*Order, error) {
	if s.Cache != nil {
		var o Order
		if found, err := s.Cache.Get(ctx, orderKey(id), &o); err == nil && found {
			return &o, nil
		}
	}
	o, err := s.Store.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.Cache != nil {
		_ = s.Cache.Set(ctx, orderKey(id), o, cacheTTL)
	}
	return o, nil
}

// List returns all orders, newest first.
func (s *OrderService) List(ctx context.Context) ([]Order, error) {
	orders, err := s.Store.All(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].ID > orders[j].ID })
	return orders, nil
}

// Delete removes an order and drops it from the cache.
func (s *OrderService) Delete(ctx context.Context, id int) error {
	if err := s.Store.Delete(ctx, id); err != nil {
		return err
	}
	if s.Cache != nil {
		_ = s.Cache.Delete(ctx, orderKey(id))
	}
	return nil
}
