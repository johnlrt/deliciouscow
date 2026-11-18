package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/wallet"
	"github.com/johnlrt/deliciouscow/server/internal/worker"
)

func main() {
	var st store.Store
	if redisAddr := os.Getenv("REDIS_ADDR"); redisAddr != "" {
		rs, err := store.NewRedisStore(redisAddr, os.Getenv("REDIS_PASSWORD"), 0, os.Getenv("REDIS_REQUIRE_REPLICA") == "1", nil)
		if err != nil {
			log.Fatalf("redis init: %v", err)
		}
		defer rs.Close()
		st = rs
	} else {
		snap := os.Getenv("SNAP_PATH")
		if snap == "" {
			snap = ".local/state.json"
		}
		ms, err := store.NewMemoryStore(snap, nil)
		if err != nil {
			log.Fatalf("store init: %v", err)
		}
		st = ms
	}

	netName := os.Getenv("MONERO_NETWORK")
	if netName == "" {
		netName = "stagenet"
	}
	var wal wallet.Wallet = wallet.NewMockWallet(netName)
	if rpcURL := os.Getenv("MONERO_WALLET_RPC"); rpcURL != "" {
		wal = wallet.NewMoneroRPCWallet(rpcURL, netName)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	log.Printf("cordova-payments worker started (network=%s)", netName)
	worker.Run(ctx, st, wal, 15*time.Second)
}
