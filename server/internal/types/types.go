package types

import (
	"fmt"
	"strconv"
	"time"
)

// Money represents piconero as an integer, serialized as a decimal string in JSON per C1.
type Money int64

func (m Money) String() string {
	return strconv.FormatInt(int64(m), 10)
}

func (m Money) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strconv.FormatInt(int64(m), 10) + `"`), nil
}

func (m *Money) UnmarshalJSON(data []byte) error {
	if len(data) >= 2 && data[0] == '"' && data[len(data)-1] == '"' {
		v, err := strconv.ParseInt(string(data[1:len(data)-1]), 10, 64)
		if err != nil {
			return err
		}
		*m = Money(v)
		return nil
	}
	v, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return err
	}
	*m = Money(v)
	return nil
}

type APIErrorBody struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

type APIError struct {
	Error     APIErrorBody `json:"error"`
	RequestID string       `json:"request_id"`
}

type SessionView struct {
	AccountID             string `json:"account_id"`
	Username              string `json:"username"`
	CSRFToken             string `json:"csrf_token"`
	TOTPEnabled           bool   `json:"totp_enabled"`
	PrivacyNoticeVersion  string `json:"privacy_notice_version"`
	SessionExpiresAt      string `json:"session_expires_at"`
	RecoveryCodesRemaining int   `json:"recovery_codes_remaining"`
}

type RegisterResponse struct {
	SessionView
	RecoveryCodes []string `json:"recovery_codes"`
}

type APIKey struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	Prefix                 string   `json:"prefix"`
	Permissions            []string `json:"permissions"`
	ModelIDs               []string `json:"model_ids"`
	DailySpendCapPiconero  *Money   `json:"daily_spend_cap_piconero"`
	CreatedAt              string   `json:"created_at"`
	LastUsedAt             *string  `json:"last_used_at"`
	RevokedAt              *string  `json:"revoked_at"`
}

type Hardware struct {
	CPUModel   string `json:"cpu_model"`
	CPUCores   int    `json:"cpu_cores"`
	RAMBytes   int64  `json:"ram_bytes"`
	GPUModel   string `json:"gpu_model,omitempty"`
	VRAMBytes  int64  `json:"vram_bytes,omitempty"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
}

type NodeModel struct {
	ModelID        string  `json:"model_id"`
	State          string  `json:"state"` // loaded|on_disk|absent
	ArtifactSHA256 *string `json:"artifact_sha256"`
	ActiveSlots    int     `json:"active_slots"`
	MaxSlots       int     `json:"max_slots"`
}

type Node struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Status          string      `json:"status"` // online|offline|draining|revoked
	NodeVersion     string      `json:"node_version"`
	Hardware        Hardware    `json:"hardware"`
	Models          []NodeModel `json:"models"`
	RankScore       float64     `json:"rank_score"`
	ThroughputTPS   *float64    `json:"throughput_tps"`
	ActiveSlots     int         `json:"active_slots"`
	MaxSlots        int         `json:"max_slots"`
	LastSeenAt      *string     `json:"last_seen_at"`
	CreatedAt       string      `json:"created_at"`
	RevokedAt       *string     `json:"revoked_at"`
}

type Model struct {
	ID                      string `json:"id"`
	Name                    string `json:"name"`
	ContextLength           int    `json:"context_length"`
	MaxOutputTokens         int    `json:"max_output_tokens"`
	InputPricePer1MPiconero Money  `json:"input_price_per_1m_piconero"`
	OutputPricePer1MPiconero Money `json:"output_price_per_1m_piconero"`
	OwnerShareBPS           int    `json:"owner_share_bps"` // e.g., 8000 = 80%
	Availability            string `json:"availability"`    // available|warming|unavailable
}

type Balance struct {
	AvailablePiconero       Money  `json:"available_piconero"`
	ReservedPiconero        Money  `json:"reserved_piconero"`
	EarningsPendingPiconero Money  `json:"earnings_pending_piconero"`
	EarningsAvailablePiconero Money `json:"earnings_available_piconero"`
	PayoutReservedPiconero  Money  `json:"payout_reserved_piconero"`
	Currency                string `json:"currency"`
	UpdatedAt               string `json:"updated_at"`
}

type DepositAddress struct {
	Address               string `json:"address"`
	Network               string `json:"network"` // stagenet|mainnet
	ConfirmationsRequired int    `json:"confirmations_required"`
	URI                   string `json:"uri"`
	CreatedAt             string `json:"created_at"`
}

type Deposit struct {
	ID             string  `json:"id"`
	TxID           string  `json:"txid"`
	AmountPiconero Money   `json:"amount_piconero"`
	Confirmations  int     `json:"confirmations"`
	Status         string  `json:"status"` // pending|credited|reversed|locked
	ObservedAt     string  `json:"observed_at"`
	CreditedAt     *string `json:"credited_at"`
}

type LedgerView struct {
	ID                  string  `json:"id"`
	Kind                string  `json:"kind"`
	RequestID           *string `json:"request_id"`
	AmountPiconero      string  `json:"amount_piconero"`
	BalanceAfterPiconero Money  `json:"balance_after_piconero"`
	CreatedAt           string  `json:"created_at"`
	ReferenceID         string  `json:"reference_id"`
}

type PayoutSettings struct {
	Address         *string `json:"address"`
	Network         string  `json:"network"`
	MinimumPiconero Money   `json:"minimum_piconero"`
	ChangedAt       *string `json:"changed_at"`
	EligibleAfter   *string `json:"eligible_after"`
}

type Payout struct {
	ID             string   `json:"id"`
	AmountPiconero Money    `json:"amount_piconero"`
	Status         string   `json:"status"` // queued|prepared|relaying|broadcast|confirmed|unknown|failed
	TxIDs          []string `json:"txids"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	HoldReason     *string  `json:"hold_reason"`
}

type UsageRow struct {
	RequestID       string  `json:"request_id"`
	ModelID         string  `json:"model_id"`
	Status          string  `json:"status"` // accepted|running|settled|refunded|cancelled|recovering
	InputTokens     int     `json:"input_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	ChargedPiconero *Money  `json:"charged_piconero"`
	LatencyMS       *int64  `json:"latency_ms"`
	CreatedAt       string  `json:"created_at"`
	FinishedAt      *string `json:"finished_at"`
}

type AnalyticsBucket struct {
	Start         string `json:"start"`
	Requests      int    `json:"requests"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	SpendPiconero Money  `json:"spend_piconero"`
	P50LatencyMS  *int64 `json:"p50_latency_ms"`
	P95LatencyMS  *int64 `json:"p95_latency_ms"`
	Successes     int    `json:"successes"`
	Failures      int    `json:"failures"`
}

type Analytics struct {
	From    string            `json:"from"`
	To      string            `json:"to"`
	Bucket  string            `json:"bucket"` // hour|day
	Buckets []AnalyticsBucket `json:"buckets"`
}

type Page[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

func FormatTS(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func FormatTSPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func ErrPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func SafeErrorCodeStatus(code string) (status int, errType string) {
	switch code {
	case "invalid_request", "unsupported_parameter":
		return 400, "invalid_request_error"
	case "unauthenticated":
		return 401, "authentication_error"
	case "forbidden", "csrf_failed", "verification_required":
		return 403, "permission_error"
	case "not_found", "model_not_found":
		return 404, "invalid_request_error"
	case "conflict", "replay_unavailable":
		return 409, "invalid_request_error"
	case "enrollment_pending":
		return 428, "invalid_request_error"
	case "insufficient_funds", "spend_cap_exceeded", "rate_limited":
		return 429, "rate_limit_error"
	case "model_warming", "no_capacity", "node_failed", "timeout", "storage_unavailable", "tokenizer_unavailable", "wallet_unavailable":
		return 503, "server_error"
	default:
		return 500, "server_error"
	}
}

func ParseMoney(s string) (Money, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid money %q: %w", s, err)
	}
	return Money(v), nil
}
