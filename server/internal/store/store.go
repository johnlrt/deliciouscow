package store

import (
	"errors"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/types"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrConflict      = errors.New("conflict")
)

type Account struct {
	ID                     string        `json:"id"`
	Username               string        `json:"username"`
	PasswordHash           string        `json:"password_hash"`
	PrivacyNoticeVersion   string        `json:"privacy_notice_version"`
	TOTPEnabled            bool          `json:"totp_enabled"`
	TOTPSecret             string        `json:"totp_secret,omitempty"`
	PendingTOTPSetupID     string        `json:"pending_totp_setup_id,omitempty"`
	PendingTOTPSecret      string        `json:"pending_totp_secret,omitempty"`
	PendingTOTPExpiresAt   *time.Time    `json:"pending_totp_expires_at,omitempty"`
	RecoveryCodeHashes     []string      `json:"recovery_code_hashes"`
	AvailablePiconero      types.Money   `json:"available_piconero"`
	ReservedPiconero       types.Money   `json:"reserved_piconero"`
	EarningsPendingPiconero types.Money  `json:"earnings_pending_piconero"`
	EarningsAvailablePiconero types.Money `json:"earnings_available_piconero"`
	PayoutReservedPiconero types.Money   `json:"payout_reserved_piconero"`
	DepositAddress         string        `json:"deposit_address,omitempty"`
	DepositAddrCreatedAt   *time.Time    `json:"deposit_addr_created_at,omitempty"`
	PayoutAddress          *string       `json:"payout_address,omitempty"`
	PayoutChangedAt        *time.Time    `json:"payout_changed_at,omitempty"`
	PayoutEligibleAfter    *time.Time    `json:"payout_eligible_after,omitempty"`
	BalanceUpdatedAt       time.Time     `json:"balance_updated_at"`
	CreatedAt              time.Time     `json:"created_at"`
}

type Session struct {
	ID        string    `json:"id"`
	AccountID string    `json:"account_id"`
	TokenHash string    `json:"token_hash"`
	CSRFToken string    `json:"csrf_token"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
	ExpiresAt time.Time `json:"expires_at"`
	AbsExpiry time.Time `json:"abs_expiry"`
}

type StepUpToken struct {
	TokenHash string    `json:"token_hash"`
	SessionID string    `json:"session_id"`
	AccountID string    `json:"account_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
}

type APIKeyRecord struct {
	ID                    string       `json:"id"`
	AccountID             string       `json:"account_id"`
	Name                  string       `json:"name"`
	Prefix                string       `json:"prefix"`
	VerifierHash          string       `json:"verifier_hash"`
	Permissions           []string     `json:"permissions"`
	ModelIDs              []string     `json:"model_ids"`
	DailySpendCapPiconero *types.Money `json:"daily_spend_cap_piconero"`
	DailySpentDate        string       `json:"daily_spent_date"`
	DailySpentPiconero    types.Money  `json:"daily_spent_piconero"`
	CreatedAt             time.Time    `json:"created_at"`
	LastUsedAt            *time.Time   `json:"last_used_at,omitempty"`
	RevokedAt             *time.Time   `json:"revoked_at,omitempty"`
}

type Enrollment struct {
	ID              string     `json:"id"`
	UserCode        string     `json:"user_code"`
	DeviceCodeHash  string     `json:"device_code_hash"`
	HWIDHash        string     `json:"hwid_hash"`
	NodeVersion     string     `json:"node_version"`
	DeviceName      string     `json:"device_name"`
	ExpiresAt       time.Time  `json:"expires_at"`
	ApprovedBy      *string    `json:"approved_by,omitempty"`
	NodeID          *string    `json:"node_id,omitempty"`
	NodeSecretPlain *string    `json:"node_secret_plain,omitempty"`
	TokenReadUntil  *time.Time `json:"token_read_until,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type NodeRecord struct {
	ID             string            `json:"id"`
	OwnerAccountID string            `json:"owner_account_id"`
	Name           string            `json:"name"`
	SecretHash     string            `json:"secret_hash"`
	SecretVersion  int               `json:"secret_version"`
	HWIDHash       string            `json:"hwid_hash"`
	NodeVersion    string            `json:"node_version"`
	Hardware       types.Hardware    `json:"hardware"`
	Models         []types.NodeModel `json:"models"`
	RankScore      float64           `json:"rank_score"`
	ThroughputTPS  *float64          `json:"throughput_tps,omitempty"`
	ActiveSlots    int               `json:"active_slots"`
	MaxSlots       int               `json:"max_slots"`
	Status         string            `json:"status"`
	LastSeenAt     *time.Time        `json:"last_seen_at,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	RevokedAt      *time.Time        `json:"revoked_at,omitempty"`
}

type NodeChallenge struct {
	Nonce     string    `json:"nonce"`
	NodeID    string    `json:"node_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Posting struct {
	Bucket        string  `json:"bucket"`
	AccountID     *string `json:"account_id"`
	DeltaPiconero string  `json:"delta_piconero"`
}

type LedgerEntry struct {
	LedgerID     string    `json:"ledger_id"`
	OperationID  string    `json:"operation_id"`
	Kind         string    `json:"kind"`
	RequestID    *string   `json:"request_id"`
	AttemptID    *string   `json:"attempt_id"`
	PriceVersion *string   `json:"price_version"`
	CreatedAt    time.Time `json:"created_at"`
	Postings     []Posting `json:"postings"`
	ReferenceID  string    `json:"reference_id"`
	PreviousHash string    `json:"previous_hash"`
	EntryHash    string    `json:"entry_hash"`
}

type AccountLedgerRow struct {
	ID                  string      `json:"id"`
	AccountID           string      `json:"account_id"`
	Kind                string      `json:"kind"`
	RequestID           *string     `json:"request_id"`
	AmountPiconero      string      `json:"amount_piconero"`
	BalanceAfterPiconero types.Money `json:"balance_after_piconero"`
	CreatedAt           time.Time   `json:"created_at"`
	ReferenceID         string      `json:"reference_id"`
}

type OperationTombstone struct {
	OperationID string      `json:"operation_id"`
	Kind        string      `json:"kind"`
	Success     bool        `json:"success"`
	Amount      types.Money `json:"amount"`
	ErrorCode   string      `json:"error_code,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
}

type DepositRecord struct {
	ID             string      `json:"id"`
	AccountID      string      `json:"account_id"`
	TxID           string      `json:"txid"`
	AmountPiconero types.Money `json:"amount_piconero"`
	Confirmations  int         `json:"confirmations"`
	Status         string      `json:"status"`
	ObservedAt     time.Time   `json:"observed_at"`
	CreditedAt     *time.Time  `json:"credited_at,omitempty"`
}

type PayoutRecord struct {
	ID             string      `json:"id"`
	AccountID      string      `json:"account_id"`
	AmountPiconero types.Money `json:"amount_piconero"`
	Status         string      `json:"status"`
	TxIDs          []string    `json:"txids"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	HoldReason     *string     `json:"hold_reason,omitempty"`
}

type UsageRecord struct {
	RequestID       string       `json:"request_id"`
	AccountID       string       `json:"account_id"`
	APIKeyID        string       `json:"api_key_id"`
	ModelID         string       `json:"model_id"`
	Status          string       `json:"status"`
	InputTokens     int          `json:"input_tokens"`
	OutputTokens    int          `json:"output_tokens"`
	ChargedPiconero *types.Money `json:"charged_piconero,omitempty"`
	LatencyMS       *int64       `json:"latency_ms,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	FinishedAt      *time.Time   `json:"finished_at,omitempty"`
}

type IdempotencyRecord struct {
	ScopeKey   string    `json:"scope_key"`
	BodyDigest string    `json:"body_digest"`
	RequestID  string    `json:"request_id"`
	Terminal   bool      `json:"terminal"`
	CreatedAt  time.Time `json:"created_at"`
}

type LiveEvent struct {
	EventID    string `json:"event_id"`
	AccountID  string `json:"account_id"`
	Topic      string `json:"topic"`
	ResourceID string `json:"resource_id"`
	Revision   int64  `json:"revision"`
	Action     string `json:"action"`
}

// Store defines the persistence boundary (in-memory+snapshot for M1, Redis+WAITAOF in production).
type Store interface {
	WithLock(fn func(tx *Tx) error) error
	WithReadLock(fn func(tx *Tx) error) error
	Now() time.Time
}

type Tx struct {
	Accounts       map[string]*Account              `json:"accounts"`
	UsernameIndex  map[string]string                `json:"username_index"`
	Sessions       map[string]*Session              `json:"sessions"` // tokenHash -> Session
	StepUpTokens   map[string]*StepUpToken          `json:"step_up_tokens"`
	APIKeys        map[string]*APIKeyRecord         `json:"api_keys"` // id -> record
	KeyHashIndex   map[string]string                `json:"key_hash_index"`
	Enrollments    map[string]*Enrollment           `json:"enrollments"`
	UserCodeIndex  map[string]string                `json:"user_code_index"`
	DeviceCodeIdx  map[string]string                `json:"device_code_index"`
	Nodes          map[string]*NodeRecord           `json:"nodes"`
	NodeChallenges map[string]*NodeChallenge        `json:"node_challenges"`
	UsedJTIs       map[string]time.Time             `json:"used_jtis"`
	Ledger         []LedgerEntry                    `json:"ledger"`
	AccountLedger  []AccountLedgerRow               `json:"account_ledger"`
	Tombstones     map[string]*OperationTombstone   `json:"tombstones"`
	Deposits       []DepositRecord                  `json:"deposits"`
	Payouts        []PayoutRecord                   `json:"payouts"`
	Usage          []UsageRecord                    `json:"usage"`
	Idempotency    map[string]*IdempotencyRecord    `json:"idempotency"`
	PlatformEquity types.Money                      `json:"platform_equity"`
	CashHot        types.Money                      `json:"cash_hot"`
	LiveSeq        int64                            `json:"live_seq"`
	LiveEvents     map[string][]LiveEvent           `json:"live_events"` // accountID -> events
	ResourceRev    map[string]int64                 `json:"resource_rev"`
}
