package store

import (
	"errors"
	"time"
)

// RedisStore is a placeholder for the production Redis + Lua + WAITAOF persistence adapter (C7).
// TODO(M2/infra): Implement using github.com/redis/go-redis/v9 with Lua scripts for atomic ledger
// reserve/settle/refund and WAITAOF 1 1 3000 durability checks.
type RedisStore struct {
	Addr string
}

func NewRedisStore(addr string) (*RedisStore, error) {
	return nil, errors.New("redis store not implemented yet: use MemoryStore for M1")
}

func (r *RedisStore) WithLock(fn func(tx *Tx) error) error {
	return errors.New("redis store not implemented")
}

func (r *RedisStore) WithReadLock(fn func(tx *Tx) error) error {
	return errors.New("redis store not implemented")
}

func (r *RedisStore) Now() time.Time {
	return time.Now().UTC()
}
