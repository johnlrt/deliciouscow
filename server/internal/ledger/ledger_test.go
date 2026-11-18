package ledger_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/ledger"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/types"
)

func TestLedgerConservationAndIdempotency(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	st, err := store.NewMemoryStore("", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	err = st.WithLock(func(tx *store.Tx) error {
		tx.Accounts["u1"] = &store.Account{ID: "u1", Username: "alice", BalanceUpdatedAt: now}
		tx.Accounts["o1"] = &store.Account{ID: "o1", Username: "bob", BalanceUpdatedAt: now}

		// Deposit 10000
		applied, err := ledger.Deposit(tx, "deposit:d1:1", "u1", "tx1", 10000, now)
		if err != nil || !applied {
			t.Fatalf("deposit failed: applied=%v err=%v", applied, err)
		}
		// Idempotent replay
		applied2, err := ledger.Deposit(tx, "deposit:d1:1", "u1", "tx1", 10000, now)
		if err != nil || applied2 {
			t.Fatalf("expected idempotent no-op, got applied=%v err=%v", applied2, err)
		}
		if tx.Accounts["u1"].AvailablePiconero != 10000 {
			t.Fatalf("expected 10000, got %d", tx.Accounts["u1"].AvailablePiconero)
		}

		// Reserve 3000
		if _, err := ledger.Reserve(tx, "reserve:r1:1", "u1", "", "r1", "a1", 3000, now); err != nil {
			t.Fatal(err)
		}
		if tx.Accounts["u1"].AvailablePiconero != 7000 || tx.Accounts["u1"].ReservedPiconero != 3000 {
			t.Fatalf("unexpected balances after reserve: %+v", tx.Accounts["u1"])
		}

		// Settle actual=1000, owner=800
		if _, err := ledger.Settle(tx, "settle:r1", "u1", "o1", "", "r1", "a1", 3000, 1000, 800, now); err != nil {
			t.Fatal(err)
		}
		if tx.Accounts["u1"].AvailablePiconero != 9000 || tx.Accounts["u1"].ReservedPiconero != 0 {
			t.Fatalf("unexpected user balance after settle: %+v", tx.Accounts["u1"])
		}
		if tx.Accounts["o1"].EarningsPendingPiconero != 800 {
			t.Fatalf("unexpected owner earnings: %d", tx.Accounts["o1"].EarningsPendingPiconero)
		}
		if tx.PlatformEquity != 200 {
			t.Fatalf("unexpected platform equity: %d", tx.PlatformEquity)
		}

		// Conservation check: sum of all postings in every ledger entry == 0
		for i, e := range tx.Ledger {
			var sum int64
			for _, p := range e.Postings {
				v, _ := strconv.ParseInt(p.DeltaPiconero, 10, 64)
				sum += v
			}
			if sum != 0 {
				t.Fatalf("entry %d unbalanced: %d", i, sum)
			}
			if i > 0 && e.PreviousHash != tx.Ledger[i-1].EntryHash {
				t.Fatalf("broken hash chain at %d", i)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLedgerSpendCapAndInsufficientFunds(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	st, _ := store.NewMemoryStore("", func() time.Time { return now })
	capVal := types.Money(500)
	_ = st.WithLock(func(tx *store.Tx) error {
		tx.Accounts["u1"] = &store.Account{ID: "u1", AvailablePiconero: 1000, BalanceUpdatedAt: now}
		tx.APIKeys["k1"] = &store.APIKeyRecord{ID: "k1", AccountID: "u1", DailySpendCapPiconero: &capVal}
		if _, err := ledger.Reserve(tx, "reserve:r1:1", "u1", "k1", "r1", "a1", 600, now); err != ledger.ErrSpendCapExceeded {
			t.Fatalf("expected ErrSpendCapExceeded, got %v", err)
		}
		if _, err := ledger.Reserve(tx, "reserve:r2:1", "u1", "", "r2", "a1", 1200, now); err != ledger.ErrInsufficientFunds {
			t.Fatalf("expected ErrInsufficientFunds, got %v", err)
		}
		return nil
	})
}
