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
			}
		}
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
