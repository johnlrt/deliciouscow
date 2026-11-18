package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrStorageDurability = errors.New("storage_unavailable: waitaof durability gate failed")

// RedisStore implements Store backed by Redis 7.2+ with optimistic WATCH/MULTI/EXEC
// for metadata snapshots and dedicated-connection Lua + WAITAOF for financial mutations (C5/C7).
type RedisStore struct {
	client    *redis.Client
	key       string
	keyPrefix string
	localAOF  int
	replAOF   int
	clock     func() time.Time
}

const (
	defaultStateKey  = "cordova:v1:state"
	defaultKeyPrefix = "cv:v1:"
	maxWatchRetries  = 32
)

func NewRedisStore(addr, password string, db int, requireReplica bool, clock func() time.Time) (*RedisStore, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}
	return NewRedisStoreFromClient(rdb, defaultKeyPrefix, requireReplica, clock), nil
}

// NewRedisStoreFromClient constructs a RedisStore using an existing *redis.Client.
// If keyPrefix is empty, defaultKeyPrefix ("cv:v1:") is used.
func NewRedisStoreFromClient(rdb *redis.Client, keyPrefix string, requireReplica bool, clock func() time.Time) *RedisStore {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if keyPrefix == "" {
		keyPrefix = defaultKeyPrefix
	}
	repl := 0
	if requireReplica {
		repl = 1
	}
	stateKey := defaultStateKey
	if keyPrefix != defaultKeyPrefix {
		stateKey = keyPrefix + "state"
	}
	return &RedisStore{
		client:    rdb,
		key:       stateKey,
		keyPrefix: keyPrefix,
		localAOF:  1,
		replAOF:   repl,
		clock:     clock,
	}
}

func (r *RedisStore) Client() *redis.Client { return r.client }
func (r *RedisStore) KeyPrefix() string     { return r.keyPrefix }
func (r *RedisStore) LocalAOF() int         { return r.localAOF }
func (r *RedisStore) ReplAOF() int          { return r.replAOF }
func (r *RedisStore) Now() time.Time        { return r.clock() }

func (r *RedisStore) Close() error {
	return r.client.Close()
}

func (r *RedisStore) loadTx(ctx context.Context, cmdable redis.Cmdable) (*Tx, error) {
	raw, err := cmdable.Get(ctx, r.key).Bytes()
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := r.loadTx(ctx, r.client)
	if err != nil {
		return err
	}
	return fn(tx)
}

// WithLock performs an optimistic WATCH/MULTI/EXEC update of the non-financial state key
// on a dedicated Redis connection followed by WAITAOF on that same connection.
// Financial mutations MUST NOT rely on WithLock; they use EvalFinancialScript.
func (r *RedisStore) WithLock(fn func(tx *Tx) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for attempt := 0; attempt < maxWatchRetries; attempt++ {
		var aofRes []int64
		var aofErr error
		err := r.client.Watch(ctx, func(wtx *redis.Tx) error {
			state, err := r.loadTx(ctx, wtx)
			if err != nil {
				return err
			}
			if err := fn(state); err != nil {
				return err
			}
			b, err := json.Marshal(state)
			if err != nil {
				return err
			}
			_, err = wtx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, r.key, b, 0)
				return nil
			})
			if err != nil {
				return err
			}
			aofRes, aofErr = wtx.WaitAOF(ctx, r.localAOF, r.replAOF, 3000*time.Millisecond).Result()
			return nil
		}, r.key)

		if err == nil {
			if aofErr != nil {
				return fmt.Errorf("%w: %v", ErrStorageDurability, aofErr)
			}
			if len(aofRes) < 2 || aofRes[0] < int64(r.localAOF) || aofRes[1] < int64(r.replAOF) {
				return fmt.Errorf("%w: insufficient fsync counts %v (want >= [%d %d])", ErrStorageDurability, aofRes, r.localAOF, r.replAOF)
			}
			return nil
		}
		if errors.Is(err, redis.TxFailedErr) {
			// Concurrent modification by another process/goroutine; retry with fresh state.
			time.Sleep(time.Duration(attempt+1) * time.Millisecond)
			continue
		}
		return err
	}
	return fmt.Errorf("%w: watch retry budget exhausted", ErrConflict)
}

// EvalFinancialScript executes a Lua script on a dedicated Redis connection and,
// when waitAOF is true, enforces WAITAOF on that exact same connection (C5).
func (r *RedisStore) EvalFinancialScript(ctx context.Context, script *redis.Script, waitAOF bool, keys []string, args ...any) (any, error) {
	conn := r.client.Conn()
	defer conn.Close()

	res, err := script.Run(ctx, conn, keys, args...).Result()
	if err != nil {
		return nil, err
	}
	if waitAOF {
		if err := r.waitAOFOnConn(ctx, conn); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (r *RedisStore) waitAOFOnConn(ctx context.Context, conn *redis.Conn) error {
	aofRes, err := conn.WaitAOF(ctx, r.localAOF, r.replAOF, 3000*time.Millisecond).Result()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStorageDurability, err)
	}
	if len(aofRes) < 2 || aofRes[0] < int64(r.localAOF) || aofRes[1] < int64(r.replAOF) {
		return fmt.Errorf("%w: insufficient fsync counts %v (want >= [%d %d])", ErrStorageDurability, aofRes, r.localAOF, r.replAOF)
	}
	return nil
}
