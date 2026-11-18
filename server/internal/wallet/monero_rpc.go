package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/types"
)

// MoneroRPCWallet implements Wallet and C8 Monero JSON-RPC 2.0 operations against monero-wallet-rpc.
type MoneroRPCWallet struct {
	Endpoint string
	Net      string
	Client   *http.Client
	reqID    atomic.Uint64
}

func NewMoneroRPCWallet(endpoint, net string) *MoneroRPCWallet {
	if net == "" {
		net = "stagenet"
	}
	return &MoneroRPCWallet{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Net:      net,
		Client:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (w *MoneroRPCWallet) Network() string            { return w.Net }
func (w *MoneroRPCWallet) ConfirmationsRequired() int { return 10 }
func (w *MoneroRPCWallet) MinimumPayoutPiconero() types.Money {
	return 10_000_000_000 // 0.01 XMR
}

func (w *MoneroRPCWallet) ValidateAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	if len(addr) != 95 && len(addr) != 106 {
		return false
	}
	if w.Net == "stagenet" {
		return addr[0] == '5' || addr[0] == '7'
	}
	return addr[0] == '4' || addr[0] == '8'
}

func (w *MoneroRPCWallet) call(ctx context.Context, method string, params any, result any) error {
	id := w.reqID.Add(1)
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      fmt.Sprintf("%d", id),
		"method":  method,
		"params":  params,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.Endpoint+"/json_rpc", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("monero-wallet-rpc http %d", resp.StatusCode)
	}
	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("monero rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if result != nil && len(rpcResp.Result) > 0 {
		return json.Unmarshal(rpcResp.Result, result)
	}
	return nil
}

func (w *MoneroRPCWallet) CreateSubaddress(accountID string) (string, error) {
	var res struct {
		Address      string `json:"address"`
		AddressIndex uint32 `json:"address_index"`
	}
	err := w.call(context.Background(), "create_address", map[string]any{
		"account_index": 0,
		"label":         accountID,
	}, &res)
	if err != nil {
		return "", err
	}
	if res.Address == "" {
		return "", errors.New("empty subaddress returned")
	}
	return res.Address, nil
}

type IncomingTransfer struct {
	TxID          string `json:"txid"`
	Address       string `json:"address"`
	Amount        uint64 `json:"amount"`
	Confirmations uint64 `json:"confirmations"`
	Height        uint64 `json:"height"`
	Locked        bool   `json:"locked"`
	DoubleSpend   bool   `json:"double_spend_seen"`
}

func (w *MoneroRPCWallet) ScanIncoming(ctx context.Context, minHeight uint64) ([]IncomingTransfer, error) {
	var res struct {
		In []IncomingTransfer `json:"in"`
	}
	err := w.call(ctx, "get_transfers", map[string]any{
		"in":             true,
		"pool":           false, // C8: never credit mempool-only funds
		"filter_by_height": minHeight > 0,
		"min_height":     minHeight,
		"account_index":  0,
	}, &res)
	return res.In, err
}

type PreparedSplit struct {
	TxHashList     []string `json:"tx_hash_list"`
	TxMetadataList []string `json:"tx_metadata_list"`
	FeeList        []uint64 `json:"fee_list"`
	AmountList     []uint64 `json:"amount_list"`
}

// PreparePayout calls transfer_split with do_not_relay=true and get_tx_metadata=true per C8.
func (w *MoneroRPCWallet) PreparePayout(ctx context.Context, destinations []map[string]any) (*PreparedSplit, error) {
	var res PreparedSplit
	err := w.call(ctx, "transfer_split", map[string]any{
		"destinations":    destinations,
		"account_index":   0,
		"do_not_relay":    true,
		"get_tx_metadata": true,
	}, &res)
	return &res, err
}

// RelayTx broadcasts previously persisted transaction metadata per C8.
func (w *MoneroRPCWallet) RelayTx(ctx context.Context, txMetadataHex string) (string, error) {
	var res struct {
		TxHash string `json:"tx_hash"`
	}
	err := w.call(ctx, "relay_tx", map[string]any{
		"hex": txMetadataHex,
	}, &res)
	return res.TxHash, err
}

func (w *MoneroRPCWallet) GetBalance(ctx context.Context) (balance, unlocked uint64, err error) {
	var res struct {
		Balance         uint64 `json:"balance"`
		UnlockedBalance uint64 `json:"unlocked_balance"`
	}
	err = w.call(ctx, "get_balance", map[string]any{"account_index": 0}, &res)
	return res.Balance, res.UnlockedBalance, err
}
