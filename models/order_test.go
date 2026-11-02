package models_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"crayd-go/models"
	"crayd-go/store"
)

// countingStore wraps a real in-memory store and counts reads.
type countingStore struct {
	models.OrderStore
	finds int
}

func (c *countingStore) Find(ctx context.Context, id int) (*models.Order, error) {
	c.finds++
	return c.OrderStore.Find(ctx, id)
}

// brokenCache fails every call, to prove the cache is best effort.
type brokenCache struct{}

var errCache = errors.New("cache down")

func (brokenCache) Get(context.Context, string, any) (bool, error)        { return false, errCache }
func (brokenCache) Set(context.Context, string, any, time.Duration) error { return errCache }
func (brokenCache) Delete(context.Context, string) error                  { return errCache }

func newService(cache models.Cache) (*models.OrderService, *countingStore) {
	cs := &countingStore{OrderStore: store.NewMemoryOrders()}
	return &models.OrderService{Store: cs, Cache: cache}, cs
}

func TestCheckoutTotalsAndDiscount(t *testing.T) {
	tests := []struct {
		name                      string
		items                     []models.Item
		subtotal, discount, total models.Money
	}{
		{"under threshold", []models.Item{models.Item{Name: "Pen", Qty: 2, Price: 150}}, 300, 0, 300},
		{"just under", []models.Item{models.Item{Name: "Book", Qty: 1, Price: 9999}}, 9999, 0, 9999},
		{"exactly at threshold", []models.Item{models.Item{Name: "Book", Qty: 1, Price: 10000}}, 10000, 1000, 9000},
		{"multiple lines", []models.Item{models.Item{Name: "A", Qty: 2, Price: 5000}, models.Item{Name: "B", Qty: 1, Price: 2500}}, 12500, 1250, 11250},
		{"free item", []models.Item{models.Item{Name: "Sticker", Qty: 1, Price: 0}}, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newService(nil)
			o, err := svc.Checkout(context.Background(), "Ada", tt.items)
			if err != nil {
				t.Fatal(err)
			}
			if o.Subtotal != tt.subtotal || o.Discount != tt.discount || o.Total != tt.total {
				t.Fatalf("got subtotal=%d discount=%d total=%d; want %d %d %d",
					o.Subtotal, o.Discount, o.Total, tt.subtotal, tt.discount, tt.total)
			}
			if o.ID == 0 {
				t.Fatal("order was not given an ID")
			}
		})
	}
}

func TestCheckoutValidation(t *testing.T) {
	ok := []models.Item{models.Item{Name: "Pen", Qty: 1, Price: 100}}
	tests := []struct {
		name     string
		customer string
		items    []models.Item
		fields   []string
	}{
		{"blank customer", "   ", ok, []string{"customer"}},
		{"no items", "Ada", nil, []string{"items"}},
		{"blank item name", "Ada", []models.Item{models.Item{Name: " ", Qty: 1, Price: 100}}, []string{"items"}},
		{"zero qty", "Ada", []models.Item{models.Item{Name: "Pen", Qty: 0, Price: 100}}, []string{"items"}},
		{"huge qty", "Ada", []models.Item{models.Item{Name: "Pen", Qty: 5000, Price: 100}}, []string{"items"}},
		{"negative price", "Ada", []models.Item{models.Item{Name: "Pen", Qty: 1, Price: -1}}, []string{"items"}},
		{"too many items", "Ada", make([]models.Item, 21), []string{"items"}},
		{"customer too long", strings.Repeat("é", 256), ok, []string{"customer"}},
		{"item name too long", "Ada", []models.Item{{Name: strings.Repeat("x", 256), Qty: 1, Price: 1}}, []string{"items"}},
		{"everything wrong", "", nil, []string{"customer", "items"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newService(nil)
			o, err := svc.Checkout(context.Background(), tt.customer, tt.items)
			var verr models.ValidationError
			if !errors.As(err, &verr) || o != nil {
				t.Fatalf("want ValidationError, got order=%v err=%v", o, err)
			}
			for _, f := range tt.fields {
				if verr[f] == "" {
					t.Errorf("missing message for field %q in %v", f, verr)
				}
			}
			if len(verr) != len(tt.fields) {
				t.Errorf("fields = %v, want exactly %v", verr, tt.fields)
			}
			if all, _ := svc.List(context.Background()); len(all) != 0 {
				t.Errorf("invalid checkout stored %d orders", len(all))
			}
		})
	}
}

func TestCheckoutTrimsAndUsesClock(t *testing.T) {
	svc, _ := newService(nil)
	fixed := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return fixed }
	o, err := svc.Checkout(context.Background(), "  Ada  ", []models.Item{models.Item{Name: "  Pen  ", Qty: 1, Price: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if o.Customer != "Ada" || o.Items[0].Name != "Pen" || !o.CreatedAt.Equal(fixed) {
		t.Fatalf("unexpected order: %+v", o)
	}
}

func TestGetReadsThroughCache(t *testing.T) {
	ctx := context.Background()
	svc, cs := newService(store.NewMemoryCache())
	o, _ := svc.Checkout(ctx, "Ada", []models.Item{models.Item{Name: "Pen", Qty: 3, Price: 250}})

	for i := 0; i < 3; i++ {
		got, err := svc.Get(ctx, o.ID)
		if err != nil || got.Customer != "Ada" || got.Total != 750 || len(got.Items) != 1 {
			t.Fatalf("Get #%d = %+v, %v", i, got, err)
		}
	}
	if cs.finds != 1 {
		t.Fatalf("store was read %d times for 3 Gets; want 1 (cache hit afterwards)", cs.finds)
	}
}

func TestDeleteInvalidatesCache(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(store.NewMemoryCache())
	o, _ := svc.Checkout(ctx, "Ada", []models.Item{models.Item{Name: "Pen", Qty: 1, Price: 100}})
	svc.Get(ctx, o.ID) // warm the cache

	if err := svc.Delete(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, o.ID); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound (stale cache?)", err)
	}
	if err := svc.Delete(ctx, o.ID); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestGetMissingIsNotCached(t *testing.T) {
	ctx := context.Background()
	svc, cs := newService(store.NewMemoryCache())
	svc.Get(ctx, 42)
	svc.Get(ctx, 42)
	if cs.finds != 2 {
		t.Fatalf("finds = %d, want 2 (misses must not be cached)", cs.finds)
	}
}

func TestServiceWorksWithBrokenOrNoCache(t *testing.T) {
	ctx := context.Background()
	for name, cache := range map[string]models.Cache{"broken": brokenCache{}, "none": nil} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newService(cache)
			o, err := svc.Checkout(ctx, "Ada", []models.Item{models.Item{Name: "Pen", Qty: 1, Price: 100}})
			if err != nil {
				t.Fatal(err)
			}
			if got, err := svc.Get(ctx, o.ID); err != nil || got.ID != o.ID {
				t.Fatalf("Get = %+v, %v", got, err)
			}
			if err := svc.Delete(ctx, o.ID); err != nil {
				t.Fatalf("Delete = %v", err)
			}
		})
	}
}

func TestListNewestFirst(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(nil)
	for _, n := range []string{"A", "B", "C"} {
		svc.Checkout(ctx, n, []models.Item{models.Item{Name: "x", Qty: 1, Price: 1}})
	}
	all, err := svc.List(ctx)
	if err != nil || len(all) != 3 || all[0].Customer != "C" || all[2].Customer != "A" {
		t.Fatalf("List = %+v, %v", all, err)
	}
}

func TestMoney(t *testing.T) {
	for m, want := range map[models.Money]string{0: "$0.00", 5: "$0.05", 1999: "$19.99", 123456: "$1234.56", -250: "-$2.50"} {
		if got := m.String(); got != want {
			t.Errorf("Money(%d) = %q, want %q", int64(m), got, want)
		}
	}
	for in, want := range map[string]models.Money{"19.99": 1999, "5": 500, " 0.1 ": 10, "1.005": 100, "0": 0} {
		if got, ok := models.ParseMoney(in); !ok || got != want {
			t.Errorf("ParseMoney(%q) = %d, %v; want %d", in, int64(got), ok, int64(want))
		}
	}
	for _, in := range []string{"", "abc", "-1", "NaN", "Inf", "1e12", "1,5"} {
		if _, ok := models.ParseMoney(in); ok {
			t.Errorf("ParseMoney(%q) should fail", in)
		}
	}
}
