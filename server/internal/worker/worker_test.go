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
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "get_transfers":
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
		case "transfer_split":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"result": map[string]any{
					"tx_hash_list":     []string{"payout-tx-1"},
					"tx_metadata_list": []string{"metahex"},
					"fee_list":         []uint64{100},
					"amount_list":      []uint64{10000000000},
				},
			})
		case "relay_tx":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"result":  map[string]any{"tx_hash": "payout-tx-1"},
			})
		}
	}))
	defer srv.Close()

	rpc := wallet.NewMoneroRPCWallet(srv.URL, "stagenet")
	worker.ScanMoneroDeposits(context.Background(), st, rpc)
	// Second scan must be idempotent
	worker.ScanMoneroDeposits(context.Background(), st, rpc)

	// Set eligible payout address and earnings >= minimum (10_000_000_000)
	_ = st.WithLock(func(tx *store.Tx) error {
		tx.Accounts["u1"].PayoutAddress = &subaddr
		tx.Accounts["u1"].EarningsAvailablePiconero = 10_000_000_000
		tx.CashHot += 10_000_000_100
		tx.PlatformEquity += 100
		return nil
	})
	worker.ProcessPayouts(context.Background(), st, rpc)

	_ = st.WithReadLock(func(tx *store.Tx) error {
		acc := tx.Accounts["u1"]
		if acc.EarningsPendingPiconero != 0 || acc.EarningsAvailablePiconero != 0 || acc.PayoutReservedPiconero != 0 {
			t.Fatalf("unexpected earnings after payout: pending=%d avail=%d res=%d", acc.EarningsPendingPiconero, acc.EarningsAvailablePiconero, acc.PayoutReservedPiconero)
		}
		if acc.AvailablePiconero != 123456 {
			t.Fatalf("expected deposit 123456, got %d", acc.AvailablePiconero)
		}
		if len(tx.Deposits) != 1 || len(tx.Payouts) != 1 || tx.Payouts[0].Status != "broadcast" {
			t.Fatalf("unexpected deposits/payouts: deps=%d payouts=%+v", len(tx.Deposits), tx.Payouts)
		}
		return nil
	})
}
