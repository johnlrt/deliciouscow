package worker

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/ledger"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/types"
	"github.com/johnlrt/deliciouscow/server/internal/wallet"
)

// Run executes periodic background tasks: earnings maturity and (when MoneroRPCWallet is active) deposit scanning.
func Run(ctx context.Context, st store.Store, wal wallet.Wallet, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			MaturePendingEarnings(st)
			if rpc, ok := wal.(*wallet.MoneroRPCWallet); ok {
				ScanMoneroDeposits(ctx, st, rpc)
				ProcessPayouts(ctx, st, rpc)
			}
		}
	}
}

func ProcessPayouts(ctx context.Context, st store.Store, rpc *wallet.MoneroRPCWallet) {
	now := st.Now()
	minPayout := rpc.MinimumPayoutPiconero()

	type candidate struct {
		accountID string
		address   string
		amount    types.Money
		payoutID  string
	}
	var cands []candidate

	_ = st.WithLock(func(tx *store.Tx) error {
		for accID, acc := range tx.Accounts {
			if acc.PayoutAddress == nil || *acc.PayoutAddress == "" {
				continue
			}
			if acc.PayoutEligibleAfter != nil && now.Before(*acc.PayoutEligibleAfter) {
				continue
			}
			if acc.EarningsAvailablePiconero < minPayout {
				continue
			}
			pid := uuid.NewString()
			amt := acc.EarningsAvailablePiconero
			applied, err := ledger.ReservePayout(tx, "payout:"+pid+":reserve", accID, pid, amt, now)
			if err != nil || !applied {
				continue
			}
			tx.Payouts = append(tx.Payouts, store.PayoutRecord{
				ID:             pid,
				AccountID:      accID,
				AmountPiconero: amt,
				Status:         "queued",
				TxIDs:          []string{},
				CreatedAt:      now,
				UpdatedAt:      now,
			})
			cands = append(cands, candidate{
				accountID: accID,
				address:   *acc.PayoutAddress,
				amount:    amt,
				payoutID:  pid,
			})
		}
		return nil
	})

	for _, c := range cands {
		prep, err := rpc.PreparePayout(ctx, []map[string]any{{
			"address": c.address,
			"amount":  uint64(c.amount),
		}})
		if err != nil || len(prep.TxMetadataList) == 0 {
			_ = st.WithLock(func(tx *store.Tx) error {
				for i := range tx.Payouts {
					if tx.Payouts[i].ID == c.payoutID {
						tx.Payouts[i].Status = "unknown"
						tx.Payouts[i].UpdatedAt = st.Now()
					}
				}
				return nil
			})
			continue
		}
		var txids []string
		var totalFee types.Money
		for i, meta := range prep.TxMetadataList {
			txid, err := rpc.RelayTx(ctx, meta)
			if err == nil && txid != "" {
				txids = append(txids, txid)
			}
			if i < len(prep.FeeList) {
				totalFee += types.Money(prep.FeeList[i])
			}
		}
		_ = st.WithLock(func(tx *store.Tx) error {
			_, _ = ledger.BroadcastPayout(tx, "payout:"+c.payoutID+":broadcast", c.accountID, c.payoutID, c.amount, totalFee, st.Now())
			for i := range tx.Payouts {
				if tx.Payouts[i].ID == c.payoutID {
					tx.Payouts[i].Status = "broadcast"
					tx.Payouts[i].TxIDs = txids
					tx.Payouts[i].UpdatedAt = st.Now()
				}
			}
			return nil
		})
	}
}

func MaturePendingEarnings(st store.Store) {
	now := st.Now()
	_ = st.WithLock(func(tx *store.Tx) error {
		for accID, acc := range tx.Accounts {
			if acc.EarningsPendingPiconero > 0 {
				opID := fmt.Sprintf("mature:%s:%d", accID, now.UnixNano())
				_, _ = ledger.MatureEarnings(tx, opID, accID, acc.EarningsPendingPiconero, now)
			}
		}
		return nil
	})
}

func ScanMoneroDeposits(ctx context.Context, st store.Store, rpc *wallet.MoneroRPCWallet) {
	transfers, err := rpc.ScanIncoming(ctx, 0)
	if err != nil {
		log.Printf("monero scan: %v", err)
		return
	}
	reqConfs := uint64(rpc.ConfirmationsRequired())
	now := st.Now()
	_ = st.WithLock(func(tx *store.Tx) error {
		addrToAcc := make(map[string]string)
		for accID, acc := range tx.Accounts {
			if acc.DepositAddress != "" {
				addrToAcc[acc.DepositAddress] = accID
			}
		}
		for _, tr := range transfers {
			accID, ok := addrToAcc[tr.Address]
			if !ok || tr.Locked || tr.DoubleSpend || tr.Confirmations < reqConfs || tr.Amount == 0 {
				continue
			}
			opID := fmt.Sprintf("deposit:%s:%s:1", tr.TxID, tr.Address)
			applied, err := ledger.Deposit(tx, opID, accID, tr.TxID, types.Money(tr.Amount), now)
			if err == nil && applied {
				tx.Deposits = append(tx.Deposits, store.DepositRecord{
					ID:             uuid.NewString(),
					AccountID:      accID,
					TxID:           tr.TxID,
					AmountPiconero: types.Money(tr.Amount),
					Confirmations:  int(tr.Confirmations),
					Status:         "credited",
					ObservedAt:     now,
					CreditedAt:     &now,
				})
			}
		}
		return nil
	})
}
