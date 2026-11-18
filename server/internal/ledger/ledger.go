package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/types"
)

const MaxMoney types.Money = 9_000_000_000_000_000_000

var (
	ErrInsufficientFunds = errors.New("insufficient_funds")
	ErrSpendCapExceeded  = errors.New("spend_cap_exceeded")
	ErrOverflow          = errors.New("arithmetic overflow")
	ErrInvalidAmount     = errors.New("invalid amount")
)

func addMoney(a, b types.Money) (types.Money, error) {
	if a < 0 || b < 0 || a > MaxMoney || b > MaxMoney {
		return 0, ErrInvalidAmount
	}
	if a > MaxMoney-b {
		return 0, ErrOverflow
	}
	return a + b, nil
}

func subMoney(a, b types.Money) (types.Money, error) {
	if a < 0 || b < 0 || a > MaxMoney || b > MaxMoney {
		return 0, ErrInvalidAmount
	}
	if a < b {
		return 0, ErrInsufficientFunds
	}
	return a - b, nil
}

func CeilDivMul(tokens int, ratePer1M types.Money) (types.Money, error) {
	if tokens < 0 || ratePer1M < 0 || ratePer1M > MaxMoney {
		return 0, ErrInvalidAmount
	}
	if tokens == 0 || ratePer1M == 0 {
		return 0, nil
	}
	prod := new(big.Int).Mul(big.NewInt(int64(tokens)), big.NewInt(int64(ratePer1M)))
	prod.Add(prod, big.NewInt(999999))
	prod.Div(prod, big.NewInt(1000000))
	if !prod.IsInt64() {
		return 0, ErrOverflow
	}
	res := types.Money(prod.Int64())
	if res > MaxMoney {
		return 0, ErrOverflow
	}
	return res, nil
}

func ComputeReservation(inputTokens, maxOutputTokens int, ri, ro types.Money) (inputCost, reservation types.Money, err error) {
	inputCost, err = CeilDivMul(inputTokens, ri)
	if err != nil {
		return 0, 0, err
	}
	maxOutCost, err := CeilDivMul(maxOutputTokens, ro)
	if err != nil {
		return 0, 0, err
	}
	total, err := addMoney(inputCost, maxOutCost)
	if err != nil {
		return 0, 0, err
	}
	return inputCost, total, nil
}

func ComputeSettlement(inputTokens, outputTokens int, ri, ro types.Money, ownerShareBPS int) (actualCost, ownerCredit, platformTake types.Money, err error) {
	inCost, err := CeilDivMul(inputTokens, ri)
	if err != nil {
		return 0, 0, 0, err
	}
	outCost, err := CeilDivMul(outputTokens, ro)
	if err != nil {
		return 0, 0, 0, err
	}
	actualCost, err = addMoney(inCost, outCost)
	if err != nil {
		return 0, 0, 0, err
	}
	if ownerShareBPS < 0 || ownerShareBPS > 10000 {
		return 0, 0, 0, ErrInvalidAmount
	}
	// actualCost <= MaxMoney (9e18); 9e18 * 10000 overflows int64, so split via quotient/remainder.
	q := int64(actualCost) / 10000
	rem := int64(actualCost) % 10000
	ownerCredit = types.Money(q*int64(ownerShareBPS) + (rem*int64(ownerShareBPS))/10000)
	platformTake = actualCost - ownerCredit
	return actualCost, ownerCredit, platformTake, nil
}

func newEntry(opID, kind string, reqID, attemptID, priceVer *string, refID, prevHash string, now time.Time, postings []store.Posting) (store.LedgerEntry, error) {
	var sum int64
	for _, p := range postings {
		v, err := strconv.ParseInt(p.DeltaPiconero, 10, 64)
		if err != nil {
			return store.LedgerEntry{}, err
		}
		if (v > 0 && sum > math.MaxInt64-v) || (v < 0 && sum < math.MinInt64-v) {
			return store.LedgerEntry{}, ErrOverflow
		}
		sum += v
	}
	if sum != 0 {
		return store.LedgerEntry{}, fmt.Errorf("unbalanced postings: sum=%d", sum)
	}
	if prevHash == "" {
		prevHash = "0000000000000000000000000000000000000000000000000000000000000000"
	}
	entry := store.LedgerEntry{
		LedgerID:     uuid.NewString(),
		OperationID:  opID,
		Kind:         kind,
		RequestID:    reqID,
		AttemptID:    attemptID,
		PriceVersion: priceVer,
		CreatedAt:    now,
		Postings:     postings,
		ReferenceID:  refID,
		PreviousHash: prevHash,
	}
	canonical, err := json.Marshal(entry)
	if err != nil {
		return store.LedgerEntry{}, err
	}
	h := sha256.Sum256(canonical)
	entry.EntryHash = hex.EncodeToString(h[:])
	return entry, nil
}

func appendEntry(tx *store.Tx, opID, kind string, reqID, attemptID, priceVer *string, refID string, now time.Time, postings []store.Posting) error {
	prevHash := "0000000000000000000000000000000000000000000000000000000000000000"
	if n := len(tx.Ledger); n > 0 {
		prevHash = tx.Ledger[n-1].EntryHash
	}
	entry, err := newEntry(opID, kind, reqID, attemptID, priceVer, refID, prevHash, now, postings)
	if err != nil {
		return err
	}
	tx.Ledger = append(tx.Ledger, entry)
	return nil
}

func fmtSigned(v types.Money) string {
	if v > 0 {
		return "+" + strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatInt(int64(v), 10)
}

func Deposit(tx *store.Tx, opID, accountID, refID string, amount types.Money, now time.Time) (bool, error) {
	if t, ok := tx.Tombstones[opID]; ok {
		return false, mapTombstoneErr(t)
	}
	if amount <= 0 || amount > MaxMoney {
		return false, ErrInvalidAmount
	}
	acc, ok := tx.Accounts[accountID]
	if !ok {
		return false, store.ErrNotFound
	}
	newAvail, err := addMoney(acc.AvailablePiconero, amount)
	if err != nil {
		return false, err
	}
	newCash, err := addMoney(tx.CashHot, amount)
	if err != nil {
		return false, err
	}

	postings := []store.Posting{
		{Bucket: "cash.hot", DeltaPiconero: fmtSigned(amount)},
		{Bucket: "user.available", AccountID: &accountID, DeltaPiconero: fmtSigned(-amount)},
	}
	if err := appendEntry(tx, opID, "deposit", nil, nil, nil, refID, now, postings); err != nil {
		return false, err
	}
	acc.AvailablePiconero = newAvail
	acc.BalanceUpdatedAt = now
	tx.CashHot = newCash

	tx.AccountLedger = append(tx.AccountLedger, store.AccountLedgerRow{
		ID:                   uuid.NewString(),
		AccountID:            accountID,
		Kind:                 "deposit",
		AmountPiconero:       fmtSigned(amount),
		BalanceAfterPiconero: acc.AvailablePiconero,
		CreatedAt:            now,
		ReferenceID:          refID,
	})
	tx.Tombstones[opID] = &store.OperationTombstone{OperationID: opID, Kind: "deposit", Success: true, Amount: amount, CreatedAt: now}
	store.EmitEventLocked(tx, accountID, "billing", accountID)
	return true, nil
}

func Reserve(tx *store.Tx, opID, accountID, apiKeyID, reqID, attemptID string, amount types.Money, now time.Time) (bool, error) {
	if t, ok := tx.Tombstones[opID]; ok {
		return false, mapTombstoneErr(t)
	}
	if amount < 0 || amount > MaxMoney {
		return false, ErrInvalidAmount
	}
	acc, ok := tx.Accounts[accountID]
	if !ok {
		return false, store.ErrNotFound
	}
	newAvail, err := subMoney(acc.AvailablePiconero, amount)
	if err != nil {
		return false, ErrInsufficientFunds
	}
	newRes, err := addMoney(acc.ReservedPiconero, amount)
	if err != nil {
		return false, err
	}
	var keyRec *store.APIKeyRecord
	var newKeySpent types.Money
	today := now.UTC().Format("2006-01-02")
	if apiKeyID != "" {
		if k, exists := tx.APIKeys[apiKeyID]; exists {
			keyRec = k
			spent := k.DailySpentPiconero
			if k.DailySpentDate != today {
				spent = 0
			}
			newKeySpent, err = addMoney(spent, amount)
			if err != nil {
				return false, err
			}
			if k.DailySpendCapPiconero != nil && newKeySpent > *k.DailySpendCapPiconero {
				return false, ErrSpendCapExceeded
			}
		}
	}

	postings := []store.Posting{
		{Bucket: "user.available", AccountID: &accountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "user.reserved", AccountID: &accountID, DeltaPiconero: fmtSigned(-amount)},
	}
	if err := appendEntry(tx, opID, "reserve", &reqID, &attemptID, nil, reqID, now, postings); err != nil {
		return false, err
	}
	acc.AvailablePiconero = newAvail
	acc.ReservedPiconero = newRes
	acc.BalanceUpdatedAt = now
	if keyRec != nil {
		keyRec.DailySpentDate = today
		keyRec.DailySpentPiconero = newKeySpent
	}
	tx.Tombstones[opID] = &store.OperationTombstone{OperationID: opID, Kind: "reserve", Success: true, Amount: amount, CreatedAt: now}
	return true, nil
}

func Refund(tx *store.Tx, opID, accountID, reqID, attemptID string, amount types.Money, now time.Time) (bool, error) {
	return RefundWithKey(tx, opID, accountID, "", "", reqID, attemptID, amount, now)
}

func RefundWithKey(tx *store.Tx, opID, accountID, apiKeyID, spendDay, reqID, attemptID string, amount types.Money, now time.Time) (bool, error) {
	if t, ok := tx.Tombstones[opID]; ok {
		return false, mapTombstoneErr(t)
	}
	if amount < 0 || amount > MaxMoney {
		return false, ErrInvalidAmount
	}
	acc, ok := tx.Accounts[accountID]
	if !ok {
		return false, store.ErrNotFound
	}
	if acc.ReservedPiconero < amount {
		return false, fmt.Errorf("reserved %d < refund %d", acc.ReservedPiconero, amount)
	}
	newRes, err := subMoney(acc.ReservedPiconero, amount)
	if err != nil {
		return false, err
	}
	newAvail, err := addMoney(acc.AvailablePiconero, amount)
	if err != nil {
		return false, err
	}

	postings := []store.Posting{
		{Bucket: "user.reserved", AccountID: &accountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "user.available", AccountID: &accountID, DeltaPiconero: fmtSigned(-amount)},
	}
	if err := appendEntry(tx, opID, "refund", &reqID, &attemptID, nil, reqID, now, postings); err != nil {
		return false, err
	}
	acc.ReservedPiconero = newRes
	acc.AvailablePiconero = newAvail
	acc.BalanceUpdatedAt = now
	if apiKeyID != "" {
		if k, exists := tx.APIKeys[apiKeyID]; exists {
			day := spendDay
			if day == "" {
				day = now.UTC().Format("2006-01-02")
			}
			if k.DailySpentDate == day {
				if k.DailySpentPiconero >= amount {
					k.DailySpentPiconero -= amount
				} else {
					k.DailySpentPiconero = 0
				}
			}
		}
	}
	tx.Tombstones[opID] = &store.OperationTombstone{OperationID: opID, Kind: "refund", Success: true, Amount: amount, CreatedAt: now}
	store.EmitEventLocked(tx, accountID, "billing", accountID)
	return true, nil
}

func Settle(tx *store.Tx, opID, userAccountID, ownerAccountID, apiKeyID, reqID, attemptID string, reserved, actualCost, ownerCredit types.Money, now time.Time) (bool, error) {
	return SettleForDay(tx, opID, userAccountID, ownerAccountID, apiKeyID, now.UTC().Format("2006-01-02"), reqID, attemptID, reserved, actualCost, ownerCredit, now)
}

func SettleForDay(tx *store.Tx, opID, userAccountID, ownerAccountID, apiKeyID, spendDay, reqID, attemptID string, reserved, actualCost, ownerCredit types.Money, now time.Time) (bool, error) {
	if t, ok := tx.Tombstones[opID]; ok {
		return false, mapTombstoneErr(t)
	}
	if reserved < 0 || reserved > MaxMoney || actualCost < 0 || actualCost > reserved || ownerCredit < 0 || ownerCredit > actualCost {
		return false, ErrInvalidAmount
	}
	userAcc, ok := tx.Accounts[userAccountID]
	if !ok {
		return false, store.ErrNotFound
	}
	ownerAcc, ok := tx.Accounts[ownerAccountID]
	if !ok {
		return false, store.ErrNotFound
	}
	if userAcc.ReservedPiconero < reserved {
		return false, fmt.Errorf("reserved %d < settle %d", userAcc.ReservedPiconero, reserved)
	}
	refund := reserved - actualCost
	platformTake := actualCost - ownerCredit

	newUserRes, err := subMoney(userAcc.ReservedPiconero, reserved)
	if err != nil {
		return false, err
	}
	newUserAvail, err := addMoney(userAcc.AvailablePiconero, refund)
	if err != nil {
		return false, err
	}
	newOwnerPending, err := addMoney(ownerAcc.EarningsPendingPiconero, ownerCredit)
	if err != nil {
		return false, err
	}
	newEquity, err := addMoney(tx.PlatformEquity, platformTake)
	if err != nil {
		return false, err
	}

	postings := []store.Posting{
		{Bucket: "user.reserved", AccountID: &userAccountID, DeltaPiconero: fmtSigned(reserved)},
		{Bucket: "user.available", AccountID: &userAccountID, DeltaPiconero: fmtSigned(-refund)},
		{Bucket: "owner.pending", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(-ownerCredit)},
		{Bucket: "equity", DeltaPiconero: fmtSigned(-platformTake)},
	}
	if err := appendEntry(tx, opID, "settle", &reqID, &attemptID, nil, reqID, now, postings); err != nil {
		return false, err
	}

	userAcc.ReservedPiconero = newUserRes
	userAcc.AvailablePiconero = newUserAvail
	userAcc.BalanceUpdatedAt = now
	ownerAcc.EarningsPendingPiconero = newOwnerPending
	ownerAcc.BalanceUpdatedAt = now
	tx.PlatformEquity = newEquity

	if apiKeyID != "" {
		if k, exists := tx.APIKeys[apiKeyID]; exists {
			if spendDay == "" {
				spendDay = now.UTC().Format("2006-01-02")
			}
			if k.DailySpentDate != spendDay {
				k.DailySpentDate = spendDay
				k.DailySpentPiconero = actualCost
			} else {
				if k.DailySpentPiconero >= reserved {
					k.DailySpentPiconero -= reserved
				} else {
					k.DailySpentPiconero = 0
				}
				if updated, err := addMoney(k.DailySpentPiconero, actualCost); err == nil {
					k.DailySpentPiconero = updated
				}
			}
		}
	}

	if actualCost > 0 {
		tx.AccountLedger = append(tx.AccountLedger, store.AccountLedgerRow{
			ID:                   uuid.NewString(),
			AccountID:            userAccountID,
			Kind:                 "inference_charge",
			RequestID:            &reqID,
			AmountPiconero:       fmtSigned(-actualCost),
			BalanceAfterPiconero: userAcc.AvailablePiconero,
			CreatedAt:            now,
			ReferenceID:          reqID,
		})
	}
	if ownerCredit > 0 {
		tx.AccountLedger = append(tx.AccountLedger, store.AccountLedgerRow{
			ID:                   uuid.NewString(),
			AccountID:            ownerAccountID,
			Kind:                 "owner_earning",
			RequestID:            &reqID,
			AmountPiconero:       fmtSigned(ownerCredit),
			BalanceAfterPiconero: ownerAcc.EarningsPendingPiconero,
			CreatedAt:            now,
			ReferenceID:          reqID,
		})
	}
	tx.Tombstones[opID] = &store.OperationTombstone{OperationID: opID, Kind: "settle", Success: true, Amount: actualCost, CreatedAt: now}
	store.EmitEventLocked(tx, userAccountID, "billing", userAccountID)
	if ownerAccountID != userAccountID {
		store.EmitEventLocked(tx, ownerAccountID, "billing", ownerAccountID)
	}
	return true, nil
}

func MatureEarnings(tx *store.Tx, opID, ownerAccountID string, amount types.Money, now time.Time) (bool, error) {
	if t, ok := tx.Tombstones[opID]; ok {
		return false, mapTombstoneErr(t)
	}
	acc, ok := tx.Accounts[ownerAccountID]
	if !ok {
		return false, store.ErrNotFound
	}
	if amount <= 0 || amount > MaxMoney || acc.EarningsPendingPiconero < amount {
		return false, ErrInvalidAmount
	}
	newPending, err := subMoney(acc.EarningsPendingPiconero, amount)
	if err != nil {
		return false, err
	}
	newAvail, err := addMoney(acc.EarningsAvailablePiconero, amount)
	if err != nil {
		return false, err
	}
	postings := []store.Posting{
		{Bucket: "owner.pending", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "owner.available", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(-amount)},
	}
	if err := appendEntry(tx, opID, "mature", nil, nil, nil, ownerAccountID, now, postings); err != nil {
		return false, err
	}
	acc.EarningsPendingPiconero = newPending
	acc.EarningsAvailablePiconero = newAvail
	acc.BalanceUpdatedAt = now
	tx.Tombstones[opID] = &store.OperationTombstone{OperationID: opID, Kind: "mature", Success: true, Amount: amount, CreatedAt: now}
	return true, nil
}

func ReservePayout(tx *store.Tx, opID, ownerAccountID, payoutID string, amount types.Money, now time.Time) (bool, error) {
	if t, ok := tx.Tombstones[opID]; ok {
		return false, mapTombstoneErr(t)
	}
	acc, ok := tx.Accounts[ownerAccountID]
	if !ok {
		return false, store.ErrNotFound
	}
	if amount <= 0 || amount > MaxMoney || acc.EarningsAvailablePiconero < amount {
		return false, ErrInvalidAmount
	}
	newAvail, err := subMoney(acc.EarningsAvailablePiconero, amount)
	if err != nil {
		return false, err
	}
	newRes, err := addMoney(acc.PayoutReservedPiconero, amount)
	if err != nil {
		return false, err
	}
	postings := []store.Posting{
		{Bucket: "owner.available", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "owner.payout_reserved", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(-amount)},
	}
	if err := appendEntry(tx, opID, "payout_reserve", nil, nil, nil, payoutID, now, postings); err != nil {
		return false, err
	}
	acc.EarningsAvailablePiconero = newAvail
	acc.PayoutReservedPiconero = newRes
	acc.BalanceUpdatedAt = now
	tx.Tombstones[opID] = &store.OperationTombstone{OperationID: opID, Kind: "payout_reserve", Success: true, Amount: amount, CreatedAt: now}
	store.EmitEventLocked(tx, ownerAccountID, "billing", payoutID)
	return true, nil
}

func BroadcastPayout(tx *store.Tx, opID, ownerAccountID, payoutID string, amount, fee types.Money, now time.Time) (bool, error) {
	if t, ok := tx.Tombstones[opID]; ok {
		return false, mapTombstoneErr(t)
	}
	acc, ok := tx.Accounts[ownerAccountID]
	if !ok {
		return false, store.ErrNotFound
	}
	if amount <= 0 || fee < 0 || amount > MaxMoney || fee > MaxMoney || acc.PayoutReservedPiconero < amount {
		return false, ErrInvalidAmount
	}
	totalOut, err := addMoney(amount, fee)
	if err != nil {
		return false, err
	}
	newRes, err := subMoney(acc.PayoutReservedPiconero, amount)
	if err != nil {
		return false, err
	}
	newCash, err := subMoney(tx.CashHot, totalOut)
	if err != nil {
		return false, err
	}
	newFeeExp, err := addMoney(tx.NetworkFeeExpense, fee)
	if err != nil {
		return false, err
	}
	postings := []store.Posting{
		{Bucket: "owner.payout_reserved", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "cash.hot", DeltaPiconero: fmtSigned(-totalOut)},
		{Bucket: "network_fee_expense", DeltaPiconero: fmtSigned(fee)},
	}
	if err := appendEntry(tx, opID, "payout_broadcast", nil, nil, nil, payoutID, now, postings); err != nil {
		return false, err
	}
	acc.PayoutReservedPiconero = newRes
	acc.BalanceUpdatedAt = now
	tx.CashHot = newCash
	tx.NetworkFeeExpense = newFeeExp
	tx.Tombstones[opID] = &store.OperationTombstone{OperationID: opID, Kind: "payout_broadcast", Success: true, Amount: amount, CreatedAt: now}
	store.EmitEventLocked(tx, ownerAccountID, "billing", payoutID)
	return true, nil
}

// Reconcile checks C8 invariant: cash_hot + network_fee_expense == sum(all user/owner liabilities) + platform_equity.
func Reconcile(tx *store.Tx) (liabilitiesPlusEquity types.Money, cashHotPlusFees types.Money, balanced bool) {
	var total types.Money
	for _, acc := range tx.Accounts {
		total, _ = addMoney(total, acc.AvailablePiconero)
		total, _ = addMoney(total, acc.ReservedPiconero)
		total, _ = addMoney(total, acc.EarningsPendingPiconero)
		total, _ = addMoney(total, acc.EarningsAvailablePiconero)
		total, _ = addMoney(total, acc.PayoutReservedPiconero)
	}
	total, _ = addMoney(total, tx.PlatformEquity)
	cashAndFees, _ := addMoney(tx.CashHot, tx.NetworkFeeExpense)
	return total, cashAndFees, total == cashAndFees
}

func mapTombstoneErr(t *store.OperationTombstone) error {
	if t.Success {
		return nil
	}
	return errors.New(t.ErrorCode)
}
