package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/types"
	"github.com/redis/go-redis/v9"
)

const maxLedgerHeadRetries = 32

type accLedgerRowTemplate struct {
	ID                   string    `json:"id"`
	AccountID            string    `json:"account_id"`
	Kind                 string    `json:"kind"`
	RequestID            *string   `json:"request_id"`
	AmountPiconero       string    `json:"amount_piconero"`
	BalanceAfterPiconero string    `json:"balance_after_piconero"`
	CreatedAt            time.Time `json:"created_at"`
	ReferenceID          string    `json:"reference_id"`
}

func redisKeys(rs *store.RedisStore) (opPrefix, balPrefix, platKey, keySpendPrefix, ledgerKey, headKey, accLedgerPrefix, outboxKey string) {
	p := rs.KeyPrefix()
	return p + "operation:", p + "balance:", p + "platform-balance", p + "key-spend:", p + "ledger", p + "ledger-head", p + "account-ledger:", p + "outbox:financial"
}

func parseScriptResult(res any) (code int64, msg string, extra string, err error) {
	arr, ok := res.([]any)
	if !ok || len(arr) < 2 {
		return 0, "", "", fmt.Errorf("unexpected lua result: %T %v", res, res)
	}
	c, ok := arr[0].(int64)
	if !ok {
		return 0, "", "", fmt.Errorf("unexpected lua code type: %T", arr[0])
	}
	m, _ := arr[1].(string)
	if len(arr) >= 3 {
		extra, _ = arr[2].(string)
	}
	return c, m, extra, nil
}

func mapLuaError(msg string) error {
	switch msg {
	case "insufficient_funds":
		return ErrInsufficientFunds
	case "spend_cap_exceeded":
		return ErrSpendCapExceeded
	case "overflow":
		return ErrOverflow
	case "invalid_amount":
		return ErrInvalidAmount
	case "insufficient_reserved":
		return fmt.Errorf("insufficient reserved balance: %w", ErrInsufficientFunds)
	case "wrong_key_type", "corrupt_balance":
		return fmt.Errorf("redis ledger invariant violation: %s", msg)
	default:
		return errors.New(msg)
	}
}

func getRedisHead(ctx context.Context, rs *store.RedisStore, headKey string) (string, error) {
	head, err := rs.Client().Get(ctx, headKey).Result()
	if errors.Is(err, redis.Nil) || head == "" {
		return genesisHash, nil
	}
	if err != nil {
		return "", err
	}
	return head, nil
}

func runWithHeadRetry(ctx context.Context, rs *store.RedisStore, headKey string, attemptFn func(prevHead string) (res any, err error)) (bool, error) {
	head, err := getRedisHead(ctx, rs, headKey)
	if err != nil {
		return false, err
	}
	for i := 0; i < maxLedgerHeadRetries; i++ {
		res, execErr := attemptFn(head)
		if execErr != nil {
			// Note: if WAITAOF failed after Lua committed, res may still be non-nil,
			// and execErr is ErrStorageDurability. Never treat as rollback.
			return false, execErr
		}
		code, msg, extra, parseErr := parseScriptResult(res)
		if parseErr != nil {
			return false, parseErr
		}
		switch code {
		case 1:
			return true, nil
		case 0:
			return false, nil
		case -2:
			if extra != "" {
				head = extra
			} else {
				head, err = getRedisHead(ctx, rs, headKey)
				if err != nil {
					return false, err
				}
			}
			continue
		default:
			return false, mapLuaError(msg)
		}
	}
	return false, fmt.Errorf("%w: ledger head retry budget exhausted", store.ErrConflict)
}

func ExecDeposit(ctx context.Context, st store.Store, opID, accountID, refID string, amount types.Money, now time.Time) (bool, error) {
	rs, ok := st.(*store.RedisStore)
	if !ok {
		var applied bool
		err := st.WithLock(func(tx *store.Tx) error {
			var e error
			applied, e = Deposit(tx, opID, accountID, refID, amount, now)
			return e
		})
		return applied, err
	}

	if amount <= 0 || amount > MaxMoney {
		return false, ErrInvalidAmount
	}
	var exists bool
	if err := st.WithReadLock(func(tx *store.Tx) error {
		_, exists = tx.Accounts[accountID]
		return nil
	}); err != nil {
		return false, err
	}
	if !exists {
		return false, store.ErrNotFound
	}

	opPref, balPref, platKey, _, ledgerKey, headKey, accLedgerPref, outboxKey := redisKeys(rs)
	postings := []store.Posting{
		{Bucket: "cash.hot", DeltaPiconero: fmtSigned(amount)},
		{Bucket: "user.available", AccountID: &accountID, DeltaPiconero: fmtSigned(-amount)},
	}
	postingsJSON, _ := json.Marshal(postings)
	rowJSON, _ := json.Marshal(accLedgerRowTemplate{
		ID:                   uuid.NewString(),
		AccountID:            accountID,
		Kind:                 "deposit",
		AmountPiconero:       fmtSigned(amount),
		BalanceAfterPiconero: "__BALANCE_AFTER__",
		CreatedAt:            now,
		ReferenceID:          refID,
	})
	nowStr := now.UTC().Format(time.RFC3339Nano)
	keys := []string{opPref + opID, balPref + accountID, platKey, ledgerKey, headKey, accLedgerPref + accountID, outboxKey}

	return runWithHeadRetry(ctx, rs, headKey, func(prevHead string) (any, error) {
		entry, err := newEntry(opID, "deposit", nil, nil, nil, refID, prevHead, now, postings)
		if err != nil {
			return nil, err
		}
		return rs.EvalFinancialScript(ctx, LuaDepositScript, true, keys,
			amount.String(), opID, refID, nowStr, prevHead, entry.EntryHash, entry.LedgerID, string(postingsJSON), string(rowJSON))
	})
}

func ExecReserve(ctx context.Context, st store.Store, opID, accountID, apiKeyID, reqID, attemptID string, amount types.Money, now time.Time) (bool, error) {
	rs, ok := st.(*store.RedisStore)
	if !ok {
		var applied bool
		err := st.WithLock(func(tx *store.Tx) error {
			var e error
			applied, e = Reserve(tx, opID, accountID, apiKeyID, reqID, attemptID, amount, now)
			return e
		})
		return applied, err
	}

	if amount < 0 || amount > MaxMoney {
		return false, ErrInvalidAmount
	}
	var accExists bool
	hasAPIKey := "0"
	hasKeyCap := "0"
	dailyCap := "0"
	if err := st.WithReadLock(func(tx *store.Tx) error {
		_, accExists = tx.Accounts[accountID]
		if apiKeyID != "" {
			if k, exists := tx.APIKeys[apiKeyID]; exists {
				hasAPIKey = "1"
				if k.DailySpendCapPiconero != nil {
					hasKeyCap = "1"
					dailyCap = k.DailySpendCapPiconero.String()
				}
			}
		}
		return nil
	}); err != nil {
		return false, err
	}
	if !accExists {
		return false, store.ErrNotFound
	}

	opPref, balPref, _, keySpendPref, ledgerKey, headKey, _, outboxKey := redisKeys(rs)
	today := now.UTC().Format("2006-01-02")
	keySpendKey := keySpendPref + "none:" + today
	if hasAPIKey == "1" {
		keySpendKey = keySpendPref + apiKeyID + ":" + today
	}
	postings := []store.Posting{
		{Bucket: "user.available", AccountID: &accountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "user.reserved", AccountID: &accountID, DeltaPiconero: fmtSigned(-amount)},
	}
	postingsJSON, _ := json.Marshal(postings)
	nowStr := now.UTC().Format(time.RFC3339Nano)
	keys := []string{opPref + opID, balPref + accountID, keySpendKey, ledgerKey, headKey, outboxKey}

	return runWithHeadRetry(ctx, rs, headKey, func(prevHead string) (any, error) {
		entry, err := newEntry(opID, "reserve", &reqID, &attemptID, nil, reqID, prevHead, now, postings)
		if err != nil {
			return nil, err
		}
		return rs.EvalFinancialScript(ctx, LuaReserveScript, true, keys,
			amount.String(), opID, reqID, attemptID, nowStr, prevHead, entry.EntryHash, entry.LedgerID, string(postingsJSON), hasKeyCap, dailyCap, hasAPIKey)
	})
}

func ExecRefund(ctx context.Context, st store.Store, opID, accountID, apiKeyID, spendDay, reqID, attemptID string, amount types.Money, now time.Time) (bool, error) {
	rs, ok := st.(*store.RedisStore)
	if !ok {
		var applied bool
		err := st.WithLock(func(tx *store.Tx) error {
			var e error
			applied, e = RefundWithKey(tx, opID, accountID, apiKeyID, spendDay, reqID, attemptID, amount, now)
			return e
		})
		return applied, err
	}

	if amount < 0 || amount > MaxMoney {
		return false, ErrInvalidAmount
	}
	var accExists bool
	if err := st.WithReadLock(func(tx *store.Tx) error {
		_, accExists = tx.Accounts[accountID]
		return nil
	}); err != nil {
		return false, err
	}
	if !accExists {
		return false, store.ErrNotFound
	}

	opPref, balPref, _, keySpendPref, ledgerKey, headKey, _, outboxKey := redisKeys(rs)
	if spendDay == "" {
		spendDay = now.UTC().Format("2006-01-02")
	}
	hasAPIKey := "0"
	keySpendKey := keySpendPref + "none:" + spendDay
	if apiKeyID != "" {
		hasAPIKey = "1"
		keySpendKey = keySpendPref + apiKeyID + ":" + spendDay
	}
	postings := []store.Posting{
		{Bucket: "user.reserved", AccountID: &accountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "user.available", AccountID: &accountID, DeltaPiconero: fmtSigned(-amount)},
	}
	postingsJSON, _ := json.Marshal(postings)
	nowStr := now.UTC().Format(time.RFC3339Nano)
	keys := []string{opPref + opID, balPref + accountID, keySpendKey, ledgerKey, headKey, outboxKey}

	return runWithHeadRetry(ctx, rs, headKey, func(prevHead string) (any, error) {
		entry, err := newEntry(opID, "refund", &reqID, &attemptID, nil, reqID, prevHead, now, postings)
		if err != nil {
			return nil, err
		}
		return rs.EvalFinancialScript(ctx, LuaRefundScript, true, keys,
			amount.String(), opID, reqID, attemptID, nowStr, prevHead, entry.EntryHash, entry.LedgerID, string(postingsJSON), hasAPIKey)
	})
}

func ExecSettle(ctx context.Context, st store.Store, opID, userAccountID, ownerAccountID, apiKeyID, spendDay, reqID, attemptID string, reserved, actualCost, ownerCredit types.Money, now time.Time) (bool, error) {
	rs, ok := st.(*store.RedisStore)
	if !ok {
		var applied bool
		err := st.WithLock(func(tx *store.Tx) error {
			var e error
			applied, e = SettleForDay(tx, opID, userAccountID, ownerAccountID, apiKeyID, spendDay, reqID, attemptID, reserved, actualCost, ownerCredit, now)
			return e
		})
		return applied, err
	}

	if reserved < 0 || reserved > MaxMoney || actualCost < 0 || actualCost > reserved || ownerCredit < 0 || ownerCredit > actualCost {
		return false, ErrInvalidAmount
	}
	var userExists, ownerExists bool
	if err := st.WithReadLock(func(tx *store.Tx) error {
		_, userExists = tx.Accounts[userAccountID]
		_, ownerExists = tx.Accounts[ownerAccountID]
		return nil
	}); err != nil {
		return false, err
	}
	if !userExists || !ownerExists {
		return false, store.ErrNotFound
	}

	refund := reserved - actualCost
	platformTake := actualCost - ownerCredit
	opPref, balPref, platKey, keySpendPref, ledgerKey, headKey, accLedgerPref, outboxKey := redisKeys(rs)
	if spendDay == "" {
		spendDay = now.UTC().Format("2006-01-02")
	}
	hasAPIKey := "0"
	keySpendKey := keySpendPref + "none:" + spendDay
	if apiKeyID != "" {
		hasAPIKey = "1"
		keySpendKey = keySpendPref + apiKeyID + ":" + spendDay
	}
	sameAccount := "0"
	if userAccountID == ownerAccountID {
		sameAccount = "1"
	}

	postings := []store.Posting{
		{Bucket: "user.reserved", AccountID: &userAccountID, DeltaPiconero: fmtSigned(reserved)},
		{Bucket: "user.available", AccountID: &userAccountID, DeltaPiconero: fmtSigned(-refund)},
		{Bucket: "owner.pending", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(-ownerCredit)},
		{Bucket: "equity", DeltaPiconero: fmtSigned(-platformTake)},
	}
	postingsJSON, _ := json.Marshal(postings)

	var userRowStr, ownerRowStr string
	if actualCost > 0 {
		b, _ := json.Marshal(accLedgerRowTemplate{
			ID:                   uuid.NewString(),
			AccountID:            userAccountID,
			Kind:                 "inference_charge",
			RequestID:            &reqID,
			AmountPiconero:       fmtSigned(-actualCost),
			BalanceAfterPiconero: "__BALANCE_AFTER__",
			CreatedAt:            now,
			ReferenceID:          reqID,
		})
		userRowStr = string(b)
	}
	if ownerCredit > 0 {
		b, _ := json.Marshal(accLedgerRowTemplate{
			ID:                   uuid.NewString(),
			AccountID:            ownerAccountID,
			Kind:                 "owner_earning",
			RequestID:            &reqID,
			AmountPiconero:       fmtSigned(ownerCredit),
			BalanceAfterPiconero: "__BALANCE_AFTER__",
			CreatedAt:            now,
			ReferenceID:          reqID,
		})
		ownerRowStr = string(b)
	}
	nowStr := now.UTC().Format(time.RFC3339Nano)
	keys := []string{
		opPref + opID,
		balPref + userAccountID,
		balPref + ownerAccountID,
		platKey,
		keySpendKey,
		ledgerKey,
		headKey,
		accLedgerPref + userAccountID,
		accLedgerPref + ownerAccountID,
		outboxKey,
	}

	return runWithHeadRetry(ctx, rs, headKey, func(prevHead string) (any, error) {
		entry, err := newEntry(opID, "settle", &reqID, &attemptID, nil, reqID, prevHead, now, postings)
		if err != nil {
			return nil, err
		}
		return rs.EvalFinancialScript(ctx, LuaSettleScript, true, keys,
			reserved.String(), actualCost.String(), ownerCredit.String(), opID, reqID, attemptID, nowStr, prevHead, entry.EntryHash, entry.LedgerID, string(postingsJSON), userRowStr, ownerRowStr, hasAPIKey, sameAccount)
	})
}

func ExecMatureEarnings(ctx context.Context, st store.Store, opID, ownerAccountID string, amount types.Money, now time.Time) (bool, error) {
	rs, ok := st.(*store.RedisStore)
	if !ok {
		var applied bool
		err := st.WithLock(func(tx *store.Tx) error {
			var e error
			applied, e = MatureEarnings(tx, opID, ownerAccountID, amount, now)
			return e
		})
		return applied, err
	}

	if amount <= 0 || amount > MaxMoney {
		return false, ErrInvalidAmount
	}
	opPref, balPref, _, _, ledgerKey, headKey, _, outboxKey := redisKeys(rs)
	postings := []store.Posting{
		{Bucket: "owner.pending", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "owner.available", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(-amount)},
	}
	postingsJSON, _ := json.Marshal(postings)
	nowStr := now.UTC().Format(time.RFC3339Nano)
	keys := []string{opPref + opID, balPref + ownerAccountID, ledgerKey, headKey, outboxKey}

	return runWithHeadRetry(ctx, rs, headKey, func(prevHead string) (any, error) {
		entry, err := newEntry(opID, "mature", nil, nil, nil, ownerAccountID, prevHead, now, postings)
		if err != nil {
			return nil, err
		}
		return rs.EvalFinancialScript(ctx, LuaMatureScript, true, keys,
			amount.String(), opID, ownerAccountID, nowStr, prevHead, entry.EntryHash, entry.LedgerID, string(postingsJSON))
	})
}

func ExecReservePayout(ctx context.Context, st store.Store, opID, ownerAccountID, payoutID string, amount types.Money, now time.Time) (bool, error) {
	rs, ok := st.(*store.RedisStore)
	if !ok {
		var applied bool
		err := st.WithLock(func(tx *store.Tx) error {
			var e error
			applied, e = ReservePayout(tx, opID, ownerAccountID, payoutID, amount, now)
			return e
		})
		return applied, err
	}

	if amount <= 0 || amount > MaxMoney {
		return false, ErrInvalidAmount
	}
	opPref, balPref, _, _, ledgerKey, headKey, _, outboxKey := redisKeys(rs)
	postings := []store.Posting{
		{Bucket: "owner.available", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "owner.payout_reserved", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(-amount)},
	}
	postingsJSON, _ := json.Marshal(postings)
	nowStr := now.UTC().Format(time.RFC3339Nano)
	keys := []string{opPref + opID, balPref + ownerAccountID, ledgerKey, headKey, outboxKey}

	return runWithHeadRetry(ctx, rs, headKey, func(prevHead string) (any, error) {
		entry, err := newEntry(opID, "payout_reserve", nil, nil, nil, payoutID, prevHead, now, postings)
		if err != nil {
			return nil, err
		}
		return rs.EvalFinancialScript(ctx, LuaReservePayoutScript, true, keys,
			amount.String(), opID, payoutID, nowStr, prevHead, entry.EntryHash, entry.LedgerID, string(postingsJSON))
	})
}

func ExecBroadcastPayout(ctx context.Context, st store.Store, opID, ownerAccountID, payoutID string, amount, fee types.Money, now time.Time) (bool, error) {
	rs, ok := st.(*store.RedisStore)
	if !ok {
		var applied bool
		err := st.WithLock(func(tx *store.Tx) error {
			var e error
			applied, e = BroadcastPayout(tx, opID, ownerAccountID, payoutID, amount, fee, now)
			return e
		})
		return applied, err
	}

	if amount <= 0 || fee < 0 || amount > MaxMoney || fee > MaxMoney {
		return false, ErrInvalidAmount
	}
	totalOut, err := addMoney(amount, fee)
	if err != nil {
		return false, err
	}
	opPref, balPref, platKey, _, ledgerKey, headKey, _, outboxKey := redisKeys(rs)
	postings := []store.Posting{
		{Bucket: "owner.payout_reserved", AccountID: &ownerAccountID, DeltaPiconero: fmtSigned(amount)},
		{Bucket: "cash.hot", DeltaPiconero: fmtSigned(-totalOut)},
		{Bucket: "network_fee_expense", DeltaPiconero: fmtSigned(fee)},
	}
	postingsJSON, _ := json.Marshal(postings)
	nowStr := now.UTC().Format(time.RFC3339Nano)
	keys := []string{opPref + opID, balPref + ownerAccountID, platKey, ledgerKey, headKey, outboxKey}

	return runWithHeadRetry(ctx, rs, headKey, func(prevHead string) (any, error) {
		entry, err := newEntry(opID, "payout_broadcast", nil, nil, nil, payoutID, prevHead, now, postings)
		if err != nil {
			return nil, err
		}
		return rs.EvalFinancialScript(ctx, LuaBroadcastPayoutScript, true, keys,
			amount.String(), fee.String(), opID, payoutID, nowStr, prevHead, entry.EntryHash, entry.LedgerID, string(postingsJSON))
	})
}

func parseMoneyField(m map[string]string, field string) (types.Money, error) {
	v, ok := m[field]
	if !ok || v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, err
	}
	return types.Money(n), nil
}

func GetBalance(ctx context.Context, st store.Store, accountID string) (types.Balance, error) {
	rs, ok := st.(*store.RedisStore)
	if !ok {
		var bal types.Balance
		err := st.WithReadLock(func(tx *store.Tx) error {
			acc, exists := tx.Accounts[accountID]
			if !exists {
				return store.ErrNotFound
			}
			bal = types.Balance{
				AvailablePiconero:         acc.AvailablePiconero,
				ReservedPiconero:          acc.ReservedPiconero,
				EarningsPendingPiconero:   acc.EarningsPendingPiconero,
				EarningsAvailablePiconero: acc.EarningsAvailablePiconero,
				PayoutReservedPiconero:    acc.PayoutReservedPiconero,
				Currency:                  "XMR",
				UpdatedAt:                 types.FormatTS(acc.BalanceUpdatedAt),
			}
			return nil
		})
		return bal, err
	}

	_, balPref, _, _, _, _, _, _ := redisKeys(rs)
	fields, err := rs.Client().HGetAll(ctx, balPref+accountID).Result()
	if err != nil {
		return types.Balance{}, err
	}
	avail, err := parseMoneyField(fields, "available")
	if err != nil {
		return types.Balance{}, err
	}
	res, err := parseMoneyField(fields, "reserved")
	if err != nil {
		return types.Balance{}, err
	}
	pend, err := parseMoneyField(fields, "earnings_pending")
	if err != nil {
		return types.Balance{}, err
	}
	earnAvail, err := parseMoneyField(fields, "earnings_available")
	if err != nil {
		return types.Balance{}, err
	}
	payRes, err := parseMoneyField(fields, "payout_reserved")
	if err != nil {
		return types.Balance{}, err
	}
	upd := fields["updated_at"]
	if upd == "" {
		upd = types.FormatTS(rs.Now())
	} else if t, err := time.Parse(time.RFC3339Nano, upd); err == nil {
		upd = types.FormatTS(t)
	}
	return types.Balance{
		AvailablePiconero:         avail,
		ReservedPiconero:          res,
		EarningsPendingPiconero:   pend,
		EarningsAvailablePiconero: earnAvail,
		PayoutReservedPiconero:    payRes,
		Currency:                  "XMR",
		UpdatedAt:                 upd,
	}, nil
}

func GetAccountLedger(ctx context.Context, st store.Store, accountID string) ([]store.AccountLedgerRow, error) {
	rs, ok := st.(*store.RedisStore)
	if !ok {
		var rows []store.AccountLedgerRow
		err := st.WithReadLock(func(tx *store.Tx) error {
			for i := len(tx.AccountLedger) - 1; i >= 0; i-- {
				if tx.AccountLedger[i].AccountID == accountID {
					rows = append(rows, tx.AccountLedger[i])
				}
			}
			return nil
		})
		return rows, err
	}

	_, _, _, _, _, _, accLedgerPref, _ := redisKeys(rs)
	rawRows, err := rs.Client().LRange(ctx, accLedgerPref+accountID, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]store.AccountLedgerRow, 0, len(rawRows))
	for i := len(rawRows) - 1; i >= 0; i-- {
		var row store.AccountLedgerRow
		if err := json.Unmarshal([]byte(rawRows[i]), &row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func ExecReconcile(ctx context.Context, st store.Store) (liabilitiesPlusEquity types.Money, cashHotPlusFees types.Money, balanced bool, err error) {
	rs, ok := st.(*store.RedisStore)
	if !ok {
		var l, c types.Money
		var b bool
		err := st.WithReadLock(func(tx *store.Tx) error {
			l, c, b = Reconcile(tx)
			return nil
		})
		return l, c, b, err
	}

	_, balPref, platKey, _, _, _, _, _ := redisKeys(rs)
	var total types.Money
	var cursor uint64
	for {
		keys, next, err := rs.Client().Scan(ctx, cursor, balPref+"*", 200).Result()
		if err != nil {
			return 0, 0, false, err
		}
		for _, k := range keys {
			if strings.HasPrefix(k, balPref) {
				fields, err := rs.Client().HGetAll(ctx, k).Result()
				if err != nil {
					return 0, 0, false, err
				}
				for _, f := range []string{"available", "reserved", "earnings_pending", "earnings_available", "payout_reserved"} {
					v, err := parseMoneyField(fields, f)
					if err != nil {
						return 0, 0, false, err
					}
					total, err = addMoney(total, v)
					if err != nil {
						return 0, 0, false, err
					}
				}
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}

	platFields, err := rs.Client().HGetAll(ctx, platKey).Result()
	if err != nil {
		return 0, 0, false, err
	}
	equity, err := parseMoneyField(platFields, "equity")
	if err != nil {
		return 0, 0, false, err
	}
	cashHot, err := parseMoneyField(platFields, "cash_hot")
	if err != nil {
		return 0, 0, false, err
	}
	feeExp, err := parseMoneyField(platFields, "network_fee_expense")
	if err != nil {
		return 0, 0, false, err
	}
	total, err = addMoney(total, equity)
	if err != nil {
		return 0, 0, false, err
	}
	cashAndFees, err := addMoney(cashHot, feeExp)
	if err != nil {
		return 0, 0, false, err
	}
	return total, cashAndFees, total == cashAndFees, nil
}
