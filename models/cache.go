package models

import (
	"context"
	"time"
)

// Cache is what the models need from a cache. Redis, an in-memory map or a fake
// can sit behind it; models never import a driver.
//
// Get reports whether the key was found. Values travel as JSON-friendly Go values.
type Cache interface {
	Get(ctx context.Context, key string, dst any) (found bool, err error)
	Set(ctx context.Context, key string, v any, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}
