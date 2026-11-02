package store

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// MemoryCache is an in-process models.Cache with TTL. It stores JSON, like Redis does,
// so behaviour matches: what you Get is a copy, never the original value.
// Use it for local development and tests.
type MemoryCache struct {
	Now func() time.Time // optional clock, for tests

	mu      sync.Mutex
	entries map[string]memEntry
}

type memEntry struct {
	data    []byte
	expires time.Time // zero = never
}

func NewMemoryCache() *MemoryCache { return &MemoryCache{entries: map[string]memEntry{}} }

func (c *MemoryCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *MemoryCache) Get(_ context.Context, key string, dst any) (bool, error) {
	c.mu.Lock()
	e, ok := c.entries[key]
	if ok && !e.expires.IsZero() && !c.now().Before(e.expires) {
		delete(c.entries, key)
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(e.data, dst)
}

func (c *MemoryCache) Set(_ context.Context, key string, v any, ttl time.Duration) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	e := memEntry{data: data}
	if ttl > 0 {
		e.expires = c.now().Add(ttl)
	}
	c.mu.Lock()
	c.entries[key] = e
	c.mu.Unlock()
	return nil
}

func (c *MemoryCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
	return nil
}
