package wallet

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/johnlrt/deliciouscow/server/internal/types"
)

// Wallet defines the isolated Monero wallet RPC boundary (C8).
type Wallet interface {
	Network() string
	ConfirmationsRequired() int
	CreateSubaddress(accountID string) (string, error)
	ValidateAddress(addr string) bool
	MinimumPayoutPiconero() types.Money
}

type MockWallet struct {
	Net string // "stagenet" or "mainnet"
}

func NewMockWallet(net string) *MockWallet {
	if net == "" {
		net = "stagenet"
	}
	return &MockWallet{Net: net}
}

func (w *MockWallet) Network() string {
	return w.Net
}

func (w *MockWallet) ConfirmationsRequired() int {
	return 10
}

func (w *MockWallet) MinimumPayoutPiconero() types.Money {
	return 10_000_000_000 // 0.01 XMR
}

func (w *MockWallet) CreateSubaddress(accountID string) (string, error) {
	// Deterministic synthetic stagenet/mainnet-looking address for M1
	h := sha256.Sum256([]byte("cordova-subaddr:" + w.Net + ":" + accountID))
	prefix := "5" // stagenet primary/subaddress prefix
	if w.Net == "mainnet" {
		prefix = "8"
	}
	// Base58-safe alphabet characters
	const alpha = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < 94; i++ {
		b.WriteByte(alpha[int(h[i%len(h)])%len(alpha)])
	}
	return b.String(), nil
}

func (w *MockWallet) ValidateAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	if len(addr) != 95 && len(addr) != 106 {
		return false
	}
	if w.Net == "stagenet" {
		return addr[0] == '5' || addr[0] == '7'
	}
	return addr[0] == '4' || addr[0] == '8'
}

func FakeTxID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(h[:])
}
