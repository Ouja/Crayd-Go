package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"crayd-go/crayd"
	"crayd-go/models"
)

type payload struct {
	Name string
	N    int
	Tags []string
}

// testCacheContract is the behaviour every models.Cache must have.
// Memory and Redis run the same checks, so swapping them is safe.
func testCacheContract(t *testing.T, c models.Cache) {
	ctx := context.Background()
	var got payload

	if found, err := c.Get(ctx, "missing", &got); found || err != nil {
		t.Fatalf("miss: found=%v err=%v", found, err)
	}
	want := payload{"x", 7, []string{"a", "b"}}
	if err := c.Set(ctx, "k", want, time.Minute); err != nil {
		t.Fatal(err)
	}
	if found, err := c.Get(ctx, "k", &got); !found || err != nil || got.Name != "x" || got.N != 7 || len(got.Tags) != 2 {
		t.Fatalf("hit: found=%v err=%v got=%+v", found, err, got)
	}
	if err := c.Set(ctx, "k", payload{Name: "y"}, time.Minute); err != nil { // overwrite
		t.Fatal(err)
	}
	got = payload{}
	if found, _ := c.Get(ctx, "k", &got); !found || got.Name != "y" {
		t.Fatalf("overwrite: got %+v", got)
	}
	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if found, _ := c.Get(ctx, "k", &got); found {
		t.Fatal("key still present after Delete")
	}
	if err := c.Delete(ctx, "never-existed"); err != nil {
		t.Fatalf("deleting a missing key should not fail: %v", err)
	}
}

func TestMemoryCacheContract(t *testing.T) { testCacheContract(t, NewMemoryCache()) }

func TestMemoryCacheTTL(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewMemoryCache()
	c.Now = func() time.Time { return now }
	ctx := context.Background()
	var v int

	c.Set(ctx, "short", 1, 10*time.Second)
	c.Set(ctx, "forever", 2, 0)
	now = now.Add(9 * time.Second)
	if found, _ := c.Get(ctx, "short", &v); !found {
		t.Fatal("expired too early")
	}
	now = now.Add(time.Second) // exactly at expiry
	if found, _ := c.Get(ctx, "short", &v); found {
		t.Fatal("should have expired")
	}
	now = now.Add(1000 * time.Hour)
	if found, _ := c.Get(ctx, "forever", &v); !found || v != 2 {
		t.Fatal("ttl 0 must never expire")
	}
}

func TestMemoryCacheReturnsCopies(t *testing.T) {
	c := NewMemoryCache()
	ctx := context.Background()
	orig := payload{Tags: []string{"a"}}
	c.Set(ctx, "k", orig, time.Minute)
	orig.Tags[0] = "changed"
	var got payload
	c.Get(ctx, "k", &got)
	if got.Tags[0] != "a" {
		t.Fatal("cache shares memory with the caller")
	}
}

func TestMemoryOrdersConcurrent(t *testing.T) {
	m := NewMemoryOrders()
	ctx := context.Background()
	var wg sync.WaitGroup
	ids := make(chan int, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := &models.Order{Customer: "c", Items: []models.Item{models.Item{Name: "i", Qty: 1, Price: 1}}}
			m.Create(ctx, o)
			ids <- o.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[int]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate ID %d", id)
		}
		seen[id] = true
	}
	if all, _ := m.All(ctx); len(all) != 100 {
		t.Fatalf("stored %d orders, want 100", len(all))
	}
}

func TestMemoryOrdersIsolation(t *testing.T) {
	m := NewMemoryOrders()
	ctx := context.Background()
	o := &models.Order{Items: []models.Item{models.Item{Name: "pen", Qty: 1, Price: 100}}}
	m.Create(ctx, o)
	o.Items[0].Name = "mutated after create"
	got, _ := m.Find(ctx, o.ID)
	if got.Items[0].Name != "pen" {
		t.Fatal("store shares Items with the caller")
	}
	got.Items[0].Name = "mutated after find"
	again, _ := m.Find(ctx, o.ID)
	if again.Items[0].Name != "pen" {
		t.Fatal("Find returns shared Items")
	}
	if _, err := m.Find(ctx, 999); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("Find missing = %v", err)
	}
}

// Runs only when a real Redis is available:
//
//	REDIS_TEST_ADDR=localhost:6379 go test ./store
func TestRedisCacheContract(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("set REDIS_TEST_ADDR to run against a real Redis")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	c := NewRedisCache(rdb)
	testCacheContract(t, c)

	// TTL is enforced by Redis itself.
	ctx := context.Background()
	c.Set(ctx, "crayd:test:ttl", 1, 1*time.Second)
	time.Sleep(1200 * time.Millisecond)
	var v int
	if found, _ := c.Get(ctx, "crayd:test:ttl", &v); found {
		t.Fatal("redis key should have expired")
	}
}

// Runs only with REDIS_TEST_ADDR: sessions through the real framework store.
func TestRedisSessions(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("set REDIS_TEST_ADDR to run against a real Redis")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()
	ctx := context.Background()
	s := crayd.ServerSessions(NewRedisSessions(rdb))

	id, err := s.Save(ctx, "", map[string]string{"_uid": "7"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := s.Load(ctx, id); err != nil || d["_uid"] != "7" {
		t.Fatalf("load: %v %v", d, err)
	}
	if n, _ := rdb.Exists(ctx, "session:"+id).Result(); n != 0 {
		t.Fatal("raw session ID used as Redis key")
	}
	if err := s.Destroy(ctx, id); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Load(ctx, id); d != nil {
		t.Fatal("session survived Destroy")
	}
}
