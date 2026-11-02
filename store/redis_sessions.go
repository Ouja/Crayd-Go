package store

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisSessions implements crayd.SessionBackend, so sessions survive restarts
// and are shared between servers:
//
//	app.Sessions(crayd.SessionOptions{Store: crayd.ServerSessions(store.NewRedisSessions(rdb))})
type RedisSessions struct{ rdb *redis.Client }

func NewRedisSessions(rdb *redis.Client) *RedisSessions { return &RedisSessions{rdb: rdb} }

func (s *RedisSessions) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b, err := s.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (s *RedisSessions) Set(ctx context.Context, key string, v []byte, ttl time.Duration) error {
	return s.rdb.Set(ctx, key, v, ttl).Err()
}

func (s *RedisSessions) Delete(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, key).Err()
}
