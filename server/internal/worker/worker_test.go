package worker_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/wallet"
	"github.com/johnlrt/deliciouscow/server/internal/worker"
)

func TestWorkerMaturityAndMoneroScan(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	st, _ := store.NewMemoryStore("", func() time.Time { return now })
	subaddr := "5" + string(make([]byte, 94))
	_ = st.WithLock(func(tx *store.Tx) error {
		tx.Accounts["u1"] = &store.Account{
			ID:                      "u1",
			DepositAddress:          subaddr,
			EarningsPendingPiconero: 5000,
			BalanceUpdatedAt:        now,
		}
		return nil
	})

	worker.MaturePendingEarnings(st)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"result": map[string]any{
				"in": []map[string]any{{
					"txid":          "tx999",
					"address":       subaddr,
					"amount":        123456,
					"confirmations": 12,
					"locked":        false,
				}},
			},
		})
	}))
	defer srv.Close()

	rpc := wallet.NewMoneroRPCWallet(srv.URL, "stagenet")
	worker.ScanMoneroDeposits(context.Background(), st, rpc)
	// Second scan must be idempotent
	worker.ScanMoneroDeposits(context.Background(), st, rpc)

	_ = st.WithReadLock(func(tx *store.Tx) error {
		acc := tx.Accounts["u1"]
		if acc.EarningsPendingPiconero != 0 || acc.EarningsAvailablePiconero != 5000 {
			t.Fatalf("expected matured 5000, got pending=%d avail=%d", acc.EarningsPendingPiconero, acc.EarningsAvailablePiconero)
		}
		if acc.AvailablePiconero != 123456 {
			t.Fatalf("expected deposit 123456, got %d", acc.AvailablePiconero)
		}
		if len(tx.Deposits) != 1 {
			t.Fatalf("expected 1 deposit record, got %d", len(tx.Deposits))
		}
		return nil
	})
}
