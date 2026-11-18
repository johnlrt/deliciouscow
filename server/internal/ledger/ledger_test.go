package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
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

func TestLedgerAbove2Pow53AndOverflow(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	st, _ := store.NewMemoryStore("", func() time.Time { return now })
	ctx := context.Background()

	_ = st.WithLock(func(tx *store.Tx) error {
		tx.Accounts["u1"] = &store.Account{ID: "u1", Username: "whale", BalanceUpdatedAt: now}
		tx.Accounts["o1"] = &store.Account{ID: "o1", Username: "owner", BalanceUpdatedAt: now}
		return nil
	})

	// 2^53 + 1 = 9,007,199,254,740,993 (would lose 1 piconero under IEEE-754 float64)
	const above2Pow53 types.Money = 9_007_199_254_740_993
	if _, err := ledger.ExecDeposit(ctx, st, "dep:whale:1", "u1", "txw1", above2Pow53, now); err != nil {
		t.Fatalf("deposit above 2^53 failed: %v", err)
	}
	// Reserve odd 1-piconero amount above 2^53
	const resAmt types.Money = 9_007_199_254_740_991
	if _, err := ledger.ExecReserve(ctx, st, "res:whale:1", "u1", "", "rw1", "aw1", resAmt, now); err != nil {
		t.Fatalf("reserve above 2^53 failed: %v", err)
	}
	bal, err := ledger.GetBalance(ctx, st, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if bal.AvailablePiconero != 2 || bal.ReservedPiconero != resAmt {
		t.Fatalf("precision loss above 2^53: avail=%d reserved=%d", bal.AvailablePiconero, bal.ReservedPiconero)
	}

	// Settle with odd 1-piconero cost
	if _, err := ledger.ExecSettle(ctx, st, "settle:rw1", "u1", "o1", "", "2026-11-18", "rw1", "aw1", resAmt, 8_000_000_000_000_001, 6_400_000_000_000_000, now); err != nil {
		t.Fatalf("settle above 2^53 failed: %v", err)
	}

	// ComputeSettlement near MaxMoney must not overflow int64
	actual, ownerCredit, platTake, err := ledger.ComputeSettlement(1_000_000, 1_000_000, 4_000_000_000_000_000_000, 4_000_000_000_000_000_000, 8000)
	if err != nil {
		t.Fatalf("ComputeSettlement near MaxMoney failed: %v", err)
	}
	if actual != 8_000_000_000_000_000_000 || ownerCredit != 6_400_000_000_000_000_000 || platTake != 1_600_000_000_000_000_000 {
		t.Fatalf("unexpected large settlement: actual=%d owner=%d plat=%d", actual, ownerCredit, platTake)
	}

	// Overflow checks
	if _, err := ledger.ExecDeposit(ctx, st, "dep:overflow:1", "u1", "txov1", ledger.MaxMoney, now); !errors.Is(err, ledger.ErrOverflow) {
		t.Fatalf("expected ErrOverflow on deposit exceeding MaxMoney, got %v", err)
	}
	if _, err := ledger.ExecDeposit(ctx, st, "dep:overflow:2", "u1", "txov2", types.Money(math.MaxInt64), now); !errors.Is(err, ledger.ErrInvalidAmount) {
		t.Fatalf("expected ErrInvalidAmount above MaxMoney, got %v", err)
	}
	if _, _, err := ledger.ComputeReservation(1_000_000, 1_000_000, ledger.MaxMoney, ledger.MaxMoney); !errors.Is(err, ledger.ErrOverflow) {
		t.Fatalf("expected ErrOverflow in ComputeReservation, got %v", err)
	}

	l, c, balanced, err := ledger.ExecReconcile(ctx, st)
	if err != nil || !balanced {
		t.Fatalf("reconcile failed: l=%d c=%d balanced=%v err=%v", l, c, balanced, err)
	}
}

func TestConcurrentReserveAndSpendCap(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	st, _ := store.NewMemoryStore("", func() time.Time { return now })
	ctx := context.Background()
	capVal := types.Money(5000)

	_ = st.WithLock(func(tx *store.Tx) error {
		tx.Accounts["u1"] = &store.Account{ID: "u1", AvailablePiconero: 10000, BalanceUpdatedAt: now}
		tx.CashHot = 10000
		tx.APIKeys["k1"] = &store.APIKeyRecord{ID: "k1", AccountID: "u1", DailySpendCapPiconero: &capVal}
		return nil
	})

	const workers = 16
	const reqAmount = types.Money(1000)
	var succeeded atomic.Int32
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer wg.Done()
			opID := fmt.Sprintf("reserve:conc-%d:1", idx)
			reqID := fmt.Sprintf("req-%d", idx)
			ok, err := ledger.ExecReserve(ctx, st, opID, "u1", "k1", reqID, "a1", reqAmount, now)
			if err == nil && ok {
				succeeded.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := succeeded.Load(); got != 5 {
		t.Fatalf("expected exactly 5 reservations within 5000 cap, got %d", got)
	}
	bal, _ := ledger.GetBalance(ctx, st, "u1")
	if bal.ReservedPiconero != 5000 || bal.AvailablePiconero != 5000 {
		t.Fatalf("unexpected balances after concurrent reserve: %+v", bal)
	}
	l, c, balanced, err := ledger.ExecReconcile(ctx, st)
	if err != nil || !balanced {
		t.Fatalf("reconcile failed: l=%d c=%d balanced=%v err=%v", l, c, balanced, err)
	}
}

func TestConcurrentWorkersDepositMaturityPayout(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	st, _ := store.NewMemoryStore("", func() time.Time { return now })
	ctx := context.Background()

	_ = st.WithLock(func(tx *store.Tx) error {
		tx.Accounts["u1"] = &store.Account{ID: "u1", BalanceUpdatedAt: now}
		tx.Accounts["o1"] = &store.Account{ID: "o1", EarningsPendingPiconero: 10000, BalanceUpdatedAt: now}
		tx.CashHot = 10000
		return nil
	})

	// Simulate 2 workers racing to credit the exact same deposit opID
	var depApplied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			applied, err := ledger.ExecDeposit(ctx, st, "deposit:tx1:addr1:1", "u1", "tx1", 5000, now)
			if err == nil && applied {
				depApplied.Add(1)
			}
		}()
	}
	wg.Wait()
	if depApplied.Load() != 1 {
		t.Fatalf("expected exactly 1 deposit credit, got %d", depApplied.Load())
	}

	// Simulate 2 workers racing to mature o1's pending earnings with different opIDs
	var matApplied atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			applied, err := ledger.ExecMatureEarnings(ctx, st, fmt.Sprintf("mature:o1:%d", idx), "o1", 10000, now)
			if err == nil && applied {
				matApplied.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if matApplied.Load() != 1 {
		t.Fatalf("expected exactly 1 maturity, got %d", matApplied.Load())
	}

	// Reserve & broadcast payout with network fee
	if _, err := ledger.ExecReservePayout(ctx, st, "payout:p1:reserve", "o1", "p1", 10000, now); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ExecBroadcastPayout(ctx, st, "payout:p1:broadcast", "o1", "p1", 10000, 150, now); err != nil {
		t.Fatal(err)
	}
	l, c, balanced, err := ledger.ExecReconcile(ctx, st)
	if err != nil || !balanced {
		t.Fatalf("reconcile after payout+fee failed: l=%d c=%d balanced=%v err=%v", l, c, balanced, err)
	}
}

func TestFailureAndRecoveryLifecycle(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	st, _ := store.NewMemoryStore("", func() time.Time { return now })
	ctx := context.Background()
	capVal := types.Money(1000)

	_ = st.WithLock(func(tx *store.Tx) error {
		tx.Accounts["u1"] = &store.Account{ID: "u1", AvailablePiconero: 2000, BalanceUpdatedAt: now}
		tx.Accounts["o1"] = &store.Account{ID: "o1", BalanceUpdatedAt: now}
		tx.CashHot = 2000
		tx.APIKeys["k1"] = &store.APIKeyRecord{ID: "k1", AccountID: "u1", DailySpendCapPiconero: &capVal}
		return nil
	})

	// Reserve 1000 (consumes full daily cap)
	if _, err := ledger.ExecReserve(ctx, st, "reserve:req1:1", "u1", "k1", "req1", "att1", 1000, now); err != nil {
		t.Fatal(err)
	}
	// Another request is blocked by daily cap
	if _, err := ledger.ExecReserve(ctx, st, "reserve:req2:1", "u1", "k1", "req2", "att2", 500, now); !errors.Is(err, ledger.ErrSpendCapExceeded) {
		t.Fatalf("expected ErrSpendCapExceeded while req1 reserved, got %v", err)
	}
	// Simulate crash/failure recovery: refund req1 with deterministic opID
	applied, err := ledger.ExecRefund(ctx, st, "refund:req1:1", "u1", "k1", "2026-11-18", "req1", "att1", 1000, now)
	if err != nil || !applied {
		t.Fatalf("refund failed: applied=%v err=%v", applied, err)
	}
	// Replay of same recovery refund must be idempotent no-op
	applied2, err := ledger.ExecRefund(ctx, st, "refund:req1:1", "u1", "k1", "2026-11-18", "req1", "att1", 1000, now)
	if err != nil || applied2 {
		t.Fatalf("expected idempotent no-op on replay, got applied=%v err=%v", applied2, err)
	}
	// Now req2 can reserve because refund released the daily cap
	if _, err := ledger.ExecReserve(ctx, st, "reserve:req2:1", "u1", "k1", "req2", "att2", 500, now); err != nil {
		t.Fatalf("expected req2 reserve to succeed after refund, got %v", err)
	}
	// Settle req2 for 300
	if _, err := ledger.ExecSettle(ctx, st, "settle:req2", "u1", "o1", "k1", "2026-11-18", "req2", "att2", 500, 300, 240, now); err != nil {
		t.Fatal(err)
	}
	bal, _ := ledger.GetBalance(ctx, st, "u1")
	if bal.AvailablePiconero != 1700 || bal.ReservedPiconero != 0 {
		t.Fatalf("unexpected user balance after recovery+settle: %+v", bal)
	}
	l, c, balanced, err := ledger.ExecReconcile(ctx, st)
	if err != nil || !balanced {
		t.Fatalf("reconcile failed: l=%d c=%d balanced=%v err=%v", l, c, balanced, err)
	}
}
