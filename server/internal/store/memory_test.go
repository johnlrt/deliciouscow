package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/store"
)

func TestMemoryStoreSnapshotPersistence(t *testing.T) {
	dir := t.TempDir()
	snap := filepath.Join(dir, "state.json")
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	st1, err := store.NewMemoryStore(snap, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	err = st1.WithLock(func(tx *store.Tx) error {
		tx.Accounts["a1"] = &store.Account{
			ID:                "a1",
			Username:          "alice",
			AvailablePiconero: 42000,
			BalanceUpdatedAt:  now,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Reload from snapshot
	st2, err := store.NewMemoryStore(snap, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	_ = st2.WithReadLock(func(tx *store.Tx) error {
		acc := tx.Accounts["a1"]
		if acc == nil || acc.AvailablePiconero != 42000 {
			t.Fatalf("unexpected reloaded account: %+v", acc)
		}
		return nil
	})
}
