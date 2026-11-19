package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/auth"
	"github.com/johnlrt/deliciouscow/server/internal/ledger"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/types"
	"github.com/johnlrt/deliciouscow/server/internal/wallet"
)

func (s *Server) handleBillingAddress(w http.ResponseWriter, r *http.Request) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return
	}
	now := s.Store.Now()
	var addr string
	var createdAt time.Time
	err = s.Store.WithLock(func(tx *store.Tx) error {
		acc := tx.Accounts[ses.AccountID]
		if acc.DepositAddress == "" {
			a, err := s.Wallet.CreateSubaddress(acc.ID)
			if err != nil {
				return err
			}
			acc.DepositAddress = a
			acc.DepositAddrCreatedAt = &now
		}
		addr = acc.DepositAddress
		if acc.DepositAddrCreatedAt != nil {
			createdAt = *acc.DepositAddrCreatedAt
		} else {
			createdAt = now
		}
		return nil
	})
	if err != nil {
		writeErr(w, r, "wallet_unavailable", "failed to allocate subaddress", nil)
		return
	}
	writeJSON(w, http.StatusOK, types.DepositAddress{
		Address:               addr,
		Network:               s.Wallet.Network(),
		ConfirmationsRequired: s.Wallet.ConfirmationsRequired(),
		URI:                   "monero:" + addr,
		CreatedAt:             types.FormatTS(createdAt),
	})
}

func (s *Server) handleBillingBalance(w http.ResponseWriter, r *http.Request) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return
	}
	bal, err := ledger.GetBalance(r.Context(), s.Store, ses.AccountID)
	if err != nil {
		writeErr(w, r, "server_error", "failed to load balance", nil)
		return
	}
	writeJSON(w, http.StatusOK, bal)
}

func (s *Server) handleBillingDeposits(w http.ResponseWriter, r *http.Request) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return
	}
	var out []types.Deposit
	_ = s.Store.WithReadLock(func(tx *store.Tx) error {
		for i := len(tx.Deposits) - 1; i >= 0; i-- {
			d := tx.Deposits[i]
			if d.AccountID == ses.AccountID {
				out = append(out, types.Deposit{
					ID:             d.ID,
					TxID:           d.TxID,
					AmountPiconero: d.AmountPiconero,
					Confirmations:  d.Confirmations,
					Status:         d.Status,
					ObservedAt:     types.FormatTS(d.ObservedAt),
					CreditedAt:     types.FormatTSPtr(d.CreditedAt),
				})
			}
		}
		return nil
	})
	if out == nil {
		out = []types.Deposit{}
	}
	writeJSON(w, http.StatusOK, types.Page[types.Deposit]{Data: out, NextCursor: nil})
}

func (s *Server) handleBillingHistory(w http.ResponseWriter, r *http.Request) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return
	}
	rows, err := ledger.GetAccountLedger(r.Context(), s.Store, ses.AccountID)
	if err != nil {
		writeErr(w, r, "server_error", "failed to load billing history", nil)
		return
	}
	out := make([]types.LedgerView, 0, len(rows))
	for _, row := range rows {
		out = append(out, types.LedgerView{
			ID:                   row.ID,
			Kind:                 row.Kind,
			RequestID:            row.RequestID,
			AmountPiconero:       row.AmountPiconero,
			BalanceAfterPiconero: row.BalanceAfterPiconero,
			CreatedAt:            types.FormatTS(row.CreatedAt),
			ReferenceID:          row.ReferenceID,
		})
	}
	writeJSON(w, http.StatusOK, types.Page[types.LedgerView]{Data: out, NextCursor: nil})
}

func (s *Server) handleGetPayoutSettings(w http.ResponseWriter, r *http.Request) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return
	}
	var ps types.PayoutSettings
	_ = s.Store.WithReadLock(func(tx *store.Tx) error {
		acc := tx.Accounts[ses.AccountID]
		ps = types.PayoutSettings{
			Address:         acc.PayoutAddress,
			Network:         s.Wallet.Network(),
			MinimumPiconero: s.Wallet.MinimumPayoutPiconero(),
			ChangedAt:       types.FormatTSPtr(acc.PayoutChangedAt),
			EligibleAfter:   types.FormatTSPtr(acc.PayoutEligibleAfter),
		}
		return nil
	})
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handlePutPayoutSettings(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.requireSessionCSRF(w, r)
	if !ok {
		return
	}
	var req struct {
		Address     string `json:"address"`
		StepUpToken string `json:"step_up_token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, "invalid_request", "invalid json", nil)
		return
	}
	req.Address = strings.TrimSpace(req.Address)
	if !s.Wallet.ValidateAddress(req.Address) {
		writeErr(w, r, "invalid_request", "invalid monero address for network", types.ErrPtr("address"))
		return
	}
	if req.StepUpToken == "" {
		writeErr(w, r, "verification_required", "step_up_token required", types.ErrPtr("step_up_token"))
		return
	}
	now := s.Store.Now()
	th := auth.SHA256Hex(req.StepUpToken)
	var ps types.PayoutSettings
	err := s.Store.WithLock(func(tx *store.Tx) error {
		sut, ok := tx.StepUpTokens[th]
		if !ok || sut.Used || sut.SessionID != ses.ID || sut.AccountID != ses.AccountID || now.After(sut.ExpiresAt) {
			return store.ErrNotFound
		}
		sut.Used = true
		acc := tx.Accounts[ses.AccountID]
		addr := req.Address
		elig := now.Add(24 * time.Hour)
		acc.PayoutAddress = &addr
		acc.PayoutChangedAt = &now
		acc.PayoutEligibleAfter = &elig
		store.EmitEventLocked(tx, ses.AccountID, "billing", ses.AccountID)
		ps = types.PayoutSettings{
			Address:         acc.PayoutAddress,
			Network:         s.Wallet.Network(),
			MinimumPiconero: s.Wallet.MinimumPayoutPiconero(),
			ChangedAt:       types.FormatTSPtr(acc.PayoutChangedAt),
			EligibleAfter:   types.FormatTSPtr(acc.PayoutEligibleAfter),
		}
		return nil
	})
	if err != nil {
		writeErr(w, r, "verification_required", "invalid or expired step_up_token", nil)
		return
	}
	s.Hub.NotifyAccount(ses.AccountID, "billing", ses.AccountID)
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handleBillingPayouts(w http.ResponseWriter, r *http.Request) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return
	}
	var out []types.Payout
	_ = s.Store.WithReadLock(func(tx *store.Tx) error {
		for i := len(tx.Payouts) - 1; i >= 0; i-- {
			p := tx.Payouts[i]
			if p.AccountID == ses.AccountID {
				out = append(out, types.Payout{
					ID:             p.ID,
					AmountPiconero: p.AmountPiconero,
					Status:         p.Status,
					TxIDs:          p.TxIDs,
					CreatedAt:      types.FormatTS(p.CreatedAt),
					UpdatedAt:      types.FormatTS(p.UpdatedAt),
					HoldReason:     p.HoldReason,
				})
			}
		}
		return nil
	})
	if out == nil {
		out = []types.Payout{}
	}
	writeJSON(w, http.StatusOK, types.Page[types.Payout]{Data: out, NextCursor: nil})
}

func (s *Server) handleDevDeposit(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.requireSessionCSRF(w, r)
	if !ok {
		return
	}
	var req struct {
		AmountPiconero types.Money `json:"amount_piconero"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AmountPiconero <= 0 {
		writeErr(w, r, "invalid_request", "positive amount_piconero required", types.ErrPtr("amount_piconero"))
		return
	}
	now := s.Store.Now()
	depID := uuid.NewString()
	txid := wallet.FakeTxID(depID)
	opID := fmt.Sprintf("deposit:%s:1", depID)
	applied, err := ledger.ExecDeposit(r.Context(), s.Store, opID, ses.AccountID, txid, req.AmountPiconero, now)
	if err != nil {
		writeErr(w, r, "server_error", "deposit failed", nil)
		return
	}
	if applied {
		_ = s.Store.WithLock(func(tx *store.Tx) error {
			tx.Deposits = append(tx.Deposits, store.DepositRecord{
				ID:             depID,
				AccountID:      ses.AccountID,
				TxID:           txid,
				AmountPiconero: req.AmountPiconero,
				Confirmations:  s.Wallet.ConfirmationsRequired(),
				Status:         "credited",
				ObservedAt:     now,
				CreditedAt:     &now,
			})
			return nil
		})
	}
	bal, err := ledger.GetBalance(r.Context(), s.Store, ses.AccountID)
	if err != nil {
		writeErr(w, r, "server_error", "deposit succeeded but balance lookup failed", nil)
		return
	}
	s.Hub.NotifyAccount(ses.AccountID, "billing", ses.AccountID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"deposit_id":         depID,
		"txid":               txid,
		"available_piconero": bal.AvailablePiconero,
	})
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	var accountID, keyIDFilter string
	if ses, err := s.resolveSession(r); err == nil {
		accountID = ses.AccountID
	} else if k, err := s.resolveAPIKey(r); err == nil {
		hasPerm := false
		for _, p := range k.Permissions {
			if p == "usage:read" {
				hasPerm = true
				break
			}
		}
		if !hasPerm {
			writeErr(w, r, "forbidden", "missing usage:read permission", nil)
			return
		}
		accountID = k.AccountID
		keyIDFilter = k.ID
	} else {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return
	}
	modelFilter := r.URL.Query().Get("model_id")
	var out []types.UsageRow
	_ = s.Store.WithReadLock(func(tx *store.Tx) error {
		for i := len(tx.Usage) - 1; i >= 0; i-- {
			u := tx.Usage[i]
			if u.AccountID != accountID {
				continue
			}
			if keyIDFilter != "" && u.APIKeyID != keyIDFilter {
				continue
			}
			if modelFilter != "" && u.ModelID != modelFilter {
				continue
			}
			out = append(out, types.UsageRow{
				RequestID:       u.RequestID,
				ModelID:         u.ModelID,
				Status:          u.Status,
				InputTokens:     u.InputTokens,
				OutputTokens:    u.OutputTokens,
				ChargedPiconero: u.ChargedPiconero,
				LatencyMS:       u.LatencyMS,
				CreatedAt:       types.FormatTS(u.CreatedAt),
				FinishedAt:      types.FormatTSPtr(u.FinishedAt),
			})
		}
		return nil
	})
	if out == nil {
		out = []types.UsageRow{}
	}
	writeJSON(w, http.StatusOK, types.Page[types.UsageRow]{Data: out, NextCursor: nil})
}

func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return
	}
	now := s.Store.Now()
	bucket := r.URL.Query().Get("bucket")
	if bucket != "hour" {
		bucket = "day"
	}
	to := now
	from := now.Add(-7 * 24 * time.Hour)
	if f := r.URL.Query().Get("from"); f != "" {
		if t, err := time.Parse(time.RFC3339, f); err == nil {
			from = t.UTC()
		}
	}
	if tstr := r.URL.Query().Get("to"); tstr != "" {
		if t, err := time.Parse(time.RFC3339, tstr); err == nil {
			to = t.UTC()
		}
	}
	if to.Sub(from) > 30*24*time.Hour {
		writeErr(w, r, "invalid_request", "analytics span cannot exceed 30 days", nil)
		return
	}

	type accBucket struct {
		types.AnalyticsBucket
		latencies []int64
	}
	m := make(map[string]*accBucket)
	_ = s.Store.WithReadLock(func(tx *store.Tx) error {
		for _, u := range tx.Usage {
			if u.AccountID != ses.AccountID || u.CreatedAt.Before(from) || u.CreatedAt.After(to) {
				continue
			}
			var key string
			if bucket == "hour" {
				key = u.CreatedAt.UTC().Truncate(time.Hour).Format(time.RFC3339)
			} else {
				key = u.CreatedAt.UTC().Truncate(24 * time.Hour).Format(time.RFC3339)
			}
			b, ok := m[key]
			if !ok {
				b = &accBucket{AnalyticsBucket: types.AnalyticsBucket{Start: key}}
				m[key] = b
			}
			b.Requests++
			b.InputTokens += int64(u.InputTokens)
			b.OutputTokens += int64(u.OutputTokens)
			if u.ChargedPiconero != nil {
				b.SpendPiconero += *u.ChargedPiconero
			}
			if u.Status == "settled" {
				b.Successes++
			} else if u.Status == "refunded" || u.Status == "cancelled" {
				b.Failures++
			}
			if u.LatencyMS != nil {
				b.latencies = append(b.latencies, *u.LatencyMS)
			}
		}
		return nil
	})
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buckets := make([]types.AnalyticsBucket, 0, len(keys))
	for _, k := range keys {
		b := m[k]
		if len(b.latencies) > 0 {
			sort.Slice(b.latencies, func(i, j int) bool { return b.latencies[i] < b.latencies[j] })
			p50 := b.latencies[len(b.latencies)*50/100]
			p95Idx := len(b.latencies) * 95 / 100
			if p95Idx >= len(b.latencies) {
				p95Idx = len(b.latencies) - 1
			}
			p95 := b.latencies[p95Idx]
			b.P50LatencyMS = &p50
			b.P95LatencyMS = &p95
		}
		buckets = append(buckets, b.AnalyticsBucket)
	}
	writeJSON(w, http.StatusOK, types.Analytics{
		From:    types.FormatTS(from),
		To:      types.FormatTS(to),
		Bucket:  bucket,
		Buckets: buckets,
	})
}
