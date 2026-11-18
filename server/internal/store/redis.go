package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore implements Store backed by Redis 7.2+ with Lua scripts and WAITAOF durability (C5/C7).
type RedisStore struct {
	client   *redis.Client
	key      string
	localAOF int
	replAOF  int
	clock    func() time.Time
	mu       sync.Mutex
}

const defaultStateKey = "cordova:v1:state"

// LuaReserveScript atomically checks operation tombstone + available balance and reserves funds in Redis.
var LuaReserveScript = redis.NewScript(`
local opKey = KEYS[1]
local balKey = KEYS[2]
local streamKey = KEYS[3]
local amount = tonumber(ARGV[1])
local opID = ARGV[2]
local reqID = ARGV[3]
local now = ARGV[4]

if redis.call("EXISTS", opKey) == 1 then
  return {0, "idempotent"}
end
local avail = tonumber(redis.call("HGET", balKey, "available") or "0")
if avail < amount then
  return {-1, "insufficient_funds"}
end
redis.call("HINCRBY", balKey, "available", -amount)
redis.call("HINCRBY", balKey, "reserved", amount)
redis.call("HSET", opKey, "kind", "reserve", "amount", amount, "created_at", now)
redis.call("XADD", streamKey, "*", "op_id", opID, "kind", "reserve", "req_id", reqID, "amount", amount, "ts", now)
return {1, "ok"}
`)

// LuaSettleScript atomically settles reservation -> refund + owner pending + platform equity.
var LuaSettleScript = redis.NewScript(`
local opKey = KEYS[1]
local userBalKey = KEYS[2]
local ownerBalKey = KEYS[3]
local streamKey = KEYS[4]
local reserved = tonumber(ARGV[1])
local actual = tonumber(ARGV[2])
local ownerCredit = tonumber(ARGV[3])
local opID = ARGV[4]
local reqID = ARGV[5]
local now = ARGV[6]

if redis.call("EXISTS", opKey) == 1 then
  return {0, "idempotent"}
end
local curRes = tonumber(redis.call("HGET", userBalKey, "reserved") or "0")
if curRes < reserved then
  return {-1, "insufficient_reserved"}
end
local refund = reserved - actual
redis.call("HINCRBY", userBalKey, "reserved", -reserved)
redis.call("HINCRBY", userBalKey, "available", refund)
redis.call("HINCRBY", ownerBalKey, "earnings_pending", ownerCredit)
redis.call("HSET", opKey, "kind", "settle", "amount", actual, "created_at", now)
redis.call("XADD", streamKey, "*", "op_id", opID, "kind", "settle", "req_id", reqID, "amount", actual, "ts", now)
return {1, "ok"}
`)

func NewRedisStore(addr, password string, db int, requireReplica bool, clock func() time.Time) (*RedisStore, error) {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}
	repl := 0
	if requireReplica {
		repl = 1
	}
	return &RedisStore{
		client:   rdb,
		key:      defaultStateKey,
		localAOF: 1,
		replAOF:  repl,
		clock:    clock,
	}, nil
}

func (r *RedisStore) Client() *redis.Client { return r.client }
func (r *RedisStore) Now() time.Time       { return r.clock() }

func (r *RedisStore) Close() error {
	return r.client.Close()
}

func (r *RedisStore) loadTx(ctx context.Context) (*Tx, error) {
	raw, err := r.client.Get(ctx, r.key).Bytes()
	if errors.Is(err, redis.Nil) {
		return newEmptyTx(), nil
	}
	if err != nil {
		return nil, err
	}
	var t Tx
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	(&MemoryStore{}).ensureMaps(&t)
	return &t, nil
}

func (r *RedisStore) WithReadLock(fn func(tx *Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := r.loadTx(ctx)
	if err != nil {
		return err
	}
	return fn(tx)
}

func (r *RedisStore) WithLock(fn func(tx *Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := r.loadTx(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	b, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	if err := r.client.Set(ctx, r.key, b, 0).Err(); err != nil {
		return err
	}
	// C5 durability gate: WAITAOF 1 <replica> 3000
	aofRes, err := r.client.WaitAOF(ctx, r.localAOF, r.replAOF, 3000*time.Millisecond).Result()
	if err != nil {
		return fmt.Errorf("waitaof failed: %w", err)
	}
	if len(aofRes) < 2 || aofRes[0] < int64(r.localAOF) || aofRes[1] < int64(r.replAOF) {
		return fmt.Errorf("waitaof insufficient fsync: %v", aofRes)
	}
	return nil
}
