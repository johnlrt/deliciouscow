package wallet_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/johnlrt/deliciouscow/server/internal/wallet"
)

func TestMoneroRPCWallet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "create_address":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"result":  map[string]any{"address": "5" + string(make([]byte, 94)), "address_index": 1},
			})
		case "transfer_split":
			if req.Params["do_not_relay"] != true || req.Params["get_tx_metadata"] != true {
				t.Fatalf("expected do_not_relay & get_tx_metadata true, got %+v", req.Params)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"result": map[string]any{
					"tx_hash_list":     []string{"abc123"},
					"tx_metadata_list": []string{"deadbeef"},
					"fee_list":         []uint64{1000},
					"amount_list":      []uint64{50000},
				},
			})
		case "relay_tx":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"result":  map[string]any{"tx_hash": "abc123"},
			})
		default:
			http.Error(w, "unknown method", 400)
		}
	}))
	defer srv.Close()

	w := wallet.NewMoneroRPCWallet(srv.URL, "stagenet")
	addr, err := w.CreateSubaddress("acc-1")
	if err != nil || len(addr) != 95 {
		t.Fatalf("create_address failed: addr=%q err=%v", addr, err)
	}
	prep, err := w.PreparePayout(context.Background(), []map[string]any{{"address": addr, "amount": 50000}})
	if err != nil || len(prep.TxMetadataList) != 1 {
		t.Fatalf("prepare failed: %+v err=%v", prep, err)
	}
	txh, err := w.RelayTx(context.Background(), prep.TxMetadataList[0])
	if err != nil || txh != "abc123" {
		t.Fatalf("relay failed: %q err=%v", txh, err)
	}
}
