package ledger_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/ledger"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/types"
	"github.com/redis/go-redis/v9"
)

func setupRealRedisStores(t *testing.T, count int) ([]*store.RedisStore, string) {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR not set; skipping real Redis 7.2+ integration tests (Lua decimal arithmetic, WAITAOF, multi-client WATCH)")
	}
	prefix := "test:cv:" + uuid.NewString() + ":"
	stores := make([]*store.RedisStore, 0, count)
	for i := 0; i < count; i++ {
		rdb := redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: os.Getenv("REDIS_PASSWORD"),
		})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := rdb.Ping(ctx).Err(); err != nil {
			cancel()
			t.Fatalf("redis ping failed: %v", err)
		}
		cancel()
		rs := store.NewRedisStoreFromClient(rdb, prefix, os.Getenv("REDIS_REQUIRE_REPLICA") == "1", nil)
		stores = append(stores, rs)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if len(stores) > 0 {
			keys, _ := stores[0].Client().Keys(ctx, prefix+"*").Result()
			if len(keys) > 0 {
				_ = stores[0].Client().Del(ctx, keys...).Err()
			}
		}
		for _, rs := range stores {
			_ = rs.Close()
		}
	})
	return stores, prefix
}

func TestRealRedisMultiClientConcurrencyAndAbove2Pow53(t *testing.T) {
	stores, prefix := setupRealRedisStores(t, 2)
	s1, s2 := stores[0], stores[1]
	ctx := context.Background()
	now := time.Now().UTC()

	// Seed accounts via s1.WithLock
	if err := s1.WithLock(func(tx *store.Tx) error {
		tx.Accounts["u1"] = &store.Account{ID: "u1", Username: "alice", BalanceUpdatedAt: now}
		tx.Accounts["o1"] = &store.Account{ID: "o1", Username: "bob", BalanceUpdatedAt: now}
		return nil
	}); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}

	// 1. Multi-client concurrent WithLock metadata updates must not lose updates
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = s1.WithLock(func(tx *store.Tx) error {
			tx.Nodes["n1"] = &store.NodeRecord{ID: "n1", OwnerAccountID: "o1", Name: "node-1", CreatedAt: now}
			return nil
		})
	}()
	go func() {
		defer wg.Done()
		_ = s2.WithLock(func(tx *store.Tx) error {
			tx.Nodes["n2"] = &store.NodeRecord{ID: "n2", OwnerAccountID: "o1", Name: "node-2", CreatedAt: now}
			return nil
		})
	}()
	wg.Wait()

	_ = s1.WithReadLock(func(tx *store.Tx) error {
		if tx.Nodes["n1"] == nil || tx.Nodes["n2"] == nil {
			t.Fatalf("lost update in concurrent WithLock: n1=%v n2=%v", tx.Nodes["n1"], tx.Nodes["n2"])
		}
		return nil
	})

	// 2. Exact > 2^53 piconero arithmetic in real Redis Lua (no tonumber float rounding)
	const whaleDeposit types.Money = 9_007_199_254_740_993 // 2^53 + 1
	applied, err := ledger.ExecDeposit(ctx, s1, "dep:whale:1", "u1", "tx-whale", whaleDeposit, now)
	if err != nil || !applied {
		t.Fatalf("ExecDeposit > 2^53 failed: applied=%v err=%v", applied, err)
	}
	// Verify raw Redis string is exact "9007199254740993", not "9007199254740992" or "9.007e+15"
	rawAvail, err := s2.Client().HGet(ctx, prefix+"balance:u1", "available").Result()
	if err != nil || rawAvail != "9007199254740993" {
		t.Fatalf("expected exact decimal string 9007199254740993 in Redis, got %q (err=%v)", rawAvail, err)
	}

	// 3. Idempotent replay across separate Redis clients
	applied2, err := ledger.ExecDeposit(ctx, s2, "dep:whale:1", "u1", "tx-whale", whaleDeposit, now)
	if err != nil || applied2 {
		t.Fatalf("expected idempotent no-op from s2, got applied=%v err=%v", applied2, err)
	}
	ttl, err := s1.Client().TTL(ctx, prefix+"operation:dep:whale:1").Result()
	if err != nil || ttl != -1 {
		t.Fatalf("expected operation tombstone to have no TTL (-1), got %v (err=%v)", ttl, err)
	}

	// 4. Multi-client concurrent Reserve + Settle race
	const raceCount = 10
	var okCount atomic.Int32
	wg.Add(raceCount)
	for i := 0; i < raceCount; i++ {
		go func(idx int) {
			defer wg.Done()
			st := s1
			if idx%2 == 1 {
				st = s2
			}
			reqID := fmt.Sprintf("req-%d", idx)
			if ok, err := ledger.ExecReserve(ctx, st, "res:"+reqID+":1", "u1", "", reqID, "att1", 1000, now); err == nil && ok {
				if ok2, err2 := ledger.ExecSettle(ctx, st, "settle:"+reqID, "u1", "o1", "", "2026-11-18", reqID, "att1", 1000, 300, 240, now); err2 == nil && ok2 {
					okCount.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()

	if okCount.Load() != raceCount {
		t.Fatalf("expected all %d concurrent reserve+settle ops to succeed, got %d", raceCount, okCount.Load())
	}

	// 5. Reconcile & hash-chain integrity in Redis Stream
	l, c, balanced, err := ledger.ExecReconcile(ctx, s2)
	if err != nil || !balanced {
		t.Fatalf("redis reconcile failed: l=%d c=%d balanced=%v err=%v", l, c, balanced, err)
	}
	entries, err := s1.Client().XRange(ctx, prefix+"ledger", "-", "+").Result()
	if err != nil || len(entries) != 1+2*raceCount {
		t.Fatalf("unexpected ledger stream length: %d (err=%v)", len(entries), err)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Values["prev_hash"] != entries[i-1].Values["entry_hash"] {
			t.Fatalf("broken Redis ledger hash chain at %d", i)
		}
	}

	// 6. Wrong key type prevalidation must fail without mutating ledger
	badAcc := "badtype"
	_ = s1.WithLock(func(tx *store.Tx) error {
		tx.Accounts[badAcc] = &store.Account{ID: badAcc, BalanceUpdatedAt: now}
		return nil
	})
	_ = s1.Client().Set(ctx, prefix+"balance:"+badAcc, "corrupted-string-type", 0).Err()
	if _, err := ledger.ExecDeposit(ctx, s1, "dep:badtype:1", badAcc, "tx-bad", 100, now); err == nil {
		t.Fatalf("expected error on wrong key type")
	}
	if exists, _ := s1.Client().Exists(ctx, prefix+"operation:dep:badtype:1").Result(); exists != 0 {
		t.Fatalf("expected operation tombstone not written on prevalidation failure")
	}
}
