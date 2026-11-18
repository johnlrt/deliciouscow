package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/types"
)

var (
	ErrInsufficientFunds = errors.New("insufficient_funds")
	ErrSpendCapExceeded  = errors.New("spend_cap_exceeded")
	ErrOverflow          = errors.New("arithmetic overflow")
	ErrInvalidAmount     = errors.New("invalid amount")
)

func CeilDivMul(tokens int, ratePer1M types.Money) (types.Money, error) {
	if tokens < 0 || ratePer1M < 0 {
		return 0, ErrInvalidAmount
	}
	if tokens == 0 || ratePer1M == 0 {
		return 0, nil
	}
	t := int64(tokens)
	r := int64(ratePer1M)
	if t > (math.MaxInt64-999999)/r {
		return 0, ErrOverflow
	}
	return types.Money((t*r + 999999) / 1000000), nil
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
	if inputCost > types.Money(math.MaxInt64)-maxOutCost {
		return 0, 0, ErrOverflow
	}
	return inputCost, inputCost + maxOutCost, nil
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
	if inCost > types.Money(math.MaxInt64)-outCost {
		return 0, 0, 0, ErrOverflow
	}
	actualCost = inCost + outCost
	if ownerShareBPS < 0 || ownerShareBPS > 10000 {
		return 0, 0, 0, ErrInvalidAmount
	}
	ownerCredit = types.Money((int64(actualCost) * int64(ownerShareBPS)) / 10000)
	platformTake = actualCost - ownerCredit
	return actualCost, ownerCredit, platformTake, nil
}

func appendEntry(tx *store.Tx, opID, kind string, reqID, attemptID, priceVer *string, refID string, now time.Time, postings []store.Posting) error {
	// Verify postings sum to zero
	var sum int64
	for _, p := range postings {
		v, err := strconv.ParseInt(p.DeltaPiconero, 10, 64)
		if err != nil {
			return err
		}
		sum += v
	}
	if sum != 0 {
		return fmt.Errorf("unbalanced postings: sum=%d", sum)
	}
	prevHash := "0000000000000000000000000000000000000000000000000000000000000000"
	if n := len(tx.Ledger); n > 0 {
		prevHash = tx.Ledger[n-1].EntryHash
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
	canonical, _ := json.Marshal(entry)
	h := sha256.Sum256(canonical)
	entry.EntryHash = hex.EncodeToString(h[:])
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
	if amount <= 0 {
		return false, ErrInvalidAmount
	}
	acc, ok := tx.Accounts[accountID]
	if !ok {
		return false, store.ErrNotFound
	}
	acc.AvailablePiconero += amount
	acc.BalanceUpdatedAt = now
	tx.CashHot += amount

	postings := []store.Posting{
		{Bucket: "cash.hot", DeltaPiconero: fmtSigned(amount)},
		{Bucket: "user.available", AccountID: &accountID, DeltaPiconero: fmtSigned(-amount)},
	}
	if err := appendEntry(tx, opID, "deposit", nil, nil, nil, refID, now, postings); err != nil {
		return false, err
	}
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
	if amount < 0 {
		return false, ErrInvalidAmount
	}
	acc, ok := tx.Accounts[accountID]
	if !ok {
		return false, store.ErrNotFound
	}
	if acc.AvailablePiconero < amount {
		return false, ErrInsufficientFunds
	}
	if apiKeyID != "" {
		if k, exists := tx.APIKeys[apiKeyID]; exists && k.DailySpendCapPiconero != nil {
			today := now.UTC().Format("2006-01-02")
			spent := k.DailySpentPiconero
			if k.DailySpentDate != today {
				spent = 0
			}
			if spent+amount > *k.DailySpendCapPiconero {
				return false, ErrSpendCapExceeded
			}
		}
	}
	acc.AvailablePiconero -= amount
	acc.ReservedPiconero += amount
	acc.BalanceUpdatedAt = now

	postings := []store.Posting{
		{Bucket: "user.available", AccountID: &accountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "user.reserved", AccountID: &accountID, DeltaPiconero: fmtSigned(-amount)},
	}
	if err := appendEntry(tx, opID, "reserve", &reqID, &attemptID, nil, reqID, now, postings); err != nil {
		return false, err
	}
	tx.Tombstones[opID] = &store.OperationTombstone{OperationID: opID, Kind: "reserve", Success: true, Amount: amount, CreatedAt: now}
	return true, nil
}

func Refund(tx *store.Tx, opID, accountID, reqID, attemptID string, amount types.Money, now time.Time) (bool, error) {
	if t, ok := tx.Tombstones[opID]; ok {
		return false, mapTombstoneErr(t)
	}
	if amount < 0 {
		return false, ErrInvalidAmount
	}
	acc, ok := tx.Accounts[accountID]
	if !ok {
		return false, store.ErrNotFound
	}
	if acc.ReservedPiconero < amount {
		return false, fmt.Errorf("reserved %d < refund %d", acc.ReservedPiconero, amount)
	}
	acc.ReservedPiconero -= amount
	acc.AvailablePiconero += amount
	acc.BalanceUpdatedAt = now

	postings := []store.Posting{
		{Bucket: "user.reserved", AccountID: &accountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "user.available", AccountID: &accountID, DeltaPiconero: fmtSigned(-amount)},
	}
	if err := appendEntry(tx, opID, "refund", &reqID, &attemptID, nil, reqID, now, postings); err != nil {
		return false, err
	}
	tx.Tombstones[opID] = &store.OperationTombstone{OperationID: opID, Kind: "refund", Success: true, Amount: amount, CreatedAt: now}
	store.EmitEventLocked(tx, accountID, "billing", accountID)
	return true, nil
}

func Settle(tx *store.Tx, opID, userAccountID, ownerAccountID, apiKeyID, reqID, attemptID string, reserved, actualCost, ownerCredit types.Money, now time.Time) (bool, error) {
	if t, ok := tx.Tombstones[opID]; ok {
		return false, mapTombstoneErr(t)
	}
	if actualCost < 0 || actualCost > reserved || ownerCredit < 0 || ownerCredit > actualCost {
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

	userAcc.ReservedPiconero -= reserved
	userAcc.AvailablePiconero += refund
	userAcc.BalanceUpdatedAt = now

	// In M1, credit owner earnings directly to available (or pending->available) so owner payout flow is testable;
	// keep both fields accurate: credit pending, and immediately mature in dev or separate call.
	ownerAcc.EarningsPendingPiconero += ownerCredit
	ownerAcc.BalanceUpdatedAt = now
	tx.PlatformEquity += platformTake

	if apiKeyID != "" {
		if k, exists := tx.APIKeys[apiKeyID]; exists {
			today := now.UTC().Format("2006-01-02")
			if k.DailySpentDate != today {
				k.DailySpentDate = today
				k.DailySpentPiconero = 0
			}
			k.DailySpentPiconero += actualCost
		}
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
			BalanceAfterPiconero: ownerAcc.AvailablePiconero,
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
	if amount <= 0 || acc.EarningsPendingPiconero < amount {
		return false, ErrInvalidAmount
	}
	acc.EarningsPendingPiconero -= amount
	acc.EarningsAvailablePiconero += amount
	acc.BalanceUpdatedAt = now
	postings := []store.Posting{
		{Bucket: "owner.pending", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "owner.available", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(-amount)},
	}
	if err := appendEntry(tx, opID, "mature", nil, nil, nil, ownerAccountID, now, postings); err != nil {
		return false, err
	}
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
	if amount <= 0 || acc.EarningsAvailablePiconero < amount {
		return false, ErrInvalidAmount
	}
	acc.EarningsAvailablePiconero -= amount
	acc.PayoutReservedPiconero += amount
	acc.BalanceUpdatedAt = now
	postings := []store.Posting{
		{Bucket: "owner.available", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "owner.payout_reserved", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(-amount)},
	}
	if err := appendEntry(tx, opID, "payout_reserve", nil, nil, nil, payoutID, now, postings); err != nil {
		return false, err
	}
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
	if amount <= 0 || fee < 0 || acc.PayoutReservedPiconero < amount {
		return false, ErrInvalidAmount
	}
	acc.PayoutReservedPiconero -= amount
	acc.BalanceUpdatedAt = now
	tx.CashHot -= (amount + fee)
	postings := []store.Posting{
		{Bucket: "owner.payout_reserved", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "cash.hot", DeltaPiconero: fmtSigned(-(amount + fee))},
		{Bucket: "network_fee_expense", DeltaPiconero: fmtSigned(fee)},
	}
	if err := appendEntry(tx, opID, "payout_broadcast", nil, nil, nil, payoutID, now, postings); err != nil {
		return false, err
	}
	tx.Tombstones[opID] = &store.OperationTombstone{OperationID: opID, Kind: "payout_broadcast", Success: true, Amount: amount, CreatedAt: now}
	store.EmitEventLocked(tx, ownerAccountID, "billing", payoutID)
	return true, nil
}

// Reconcile checks C8 invariant: cash_hot == sum(all user/owner liabilities + platform_equity) when no network fees exist.
func Reconcile(tx *store.Tx) (liabilitiesPlusEquity types.Money, cashHot types.Money, balanced bool) {
	var total types.Money
	for _, acc := range tx.Accounts {
		total += acc.AvailablePiconero
		total += acc.ReservedPiconero
		total += acc.EarningsPendingPiconero
		total += acc.EarningsAvailablePiconero
		total += acc.PayoutReservedPiconero
	}
	total += tx.PlatformEquity
	return total, tx.CashHot, total == tx.CashHot
}

func mapTombstoneErr(t *store.OperationTombstone) error {
	if t.Success {
		return nil
	}
	return errors.New(t.ErrorCode)
}
