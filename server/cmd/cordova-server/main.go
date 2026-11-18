package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/auth"
	"github.com/johnlrt/deliciouscow/server/internal/events"
	"github.com/johnlrt/deliciouscow/server/internal/httpapi"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/tokenizer"
	"github.com/johnlrt/deliciouscow/server/internal/wallet"
	"github.com/johnlrt/deliciouscow/server/internal/ws"
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	addr := getenv("ADDR", ":8443")
	snapPath := getenv("SNAP_PATH", ".local/state.json")
	staticDir := getenv("STATIC_DIR", "../frontend/build")
	devMode := getenv("DEV_MODE", "1") == "1"
	insecureCookies := getenv("DEV_INSECURE_COOKIES", "1") == "1"
	pepper := os.Getenv("API_KEY_PEPPER")
	netName := getenv("MONERO_NETWORK", "stagenet")

	var st store.Store
	if redisAddr := os.Getenv("REDIS_ADDR"); redisAddr != "" {
		rs, err := store.NewRedisStore(redisAddr, os.Getenv("REDIS_PASSWORD"), 0, os.Getenv("REDIS_REQUIRE_REPLICA") == "1", nil)
		if err != nil {
			log.Fatalf("redis store init: %v", err)
		}
		defer rs.Close()
		st = rs
		log.Printf("using RedisStore (%s)", redisAddr)
	} else {
		ms, err := store.NewMemoryStore(snapPath, nil)
		if err != nil {
			log.Fatalf("memory store init: %v", err)
		}
		st = ms
	}

	var bus events.Bus = events.NoopBus{}
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		kb, err := events.NewKafkaBus(strings.Split(brokers, ","))
		if err != nil {
			log.Fatalf("kafka init: %v", err)
		}
		defer kb.Close()
		bus = kb
		log.Printf("using KafkaBus (%s)", brokers)
	}
	_ = bus

	var wal wallet.Wallet = wallet.NewMockWallet(netName)
	if rpcURL := os.Getenv("MONERO_WALLET_RPC"); rpcURL != "" {
		wal = wallet.NewMoneroRPCWallet(rpcURL, netName)
		log.Printf("using MoneroRPCWallet (%s, %s)", rpcURL, netName)
	}

	var tok tokenizer.Tokenizer = tokenizer.SimpleEstimator{}
	if tokURL := os.Getenv("TOKENIZER_URL"); tokURL != "" {
		tok = tokenizer.NewHelperClient(tokURL, os.Getenv("TOKENIZER_SECRET"), "v1-m1")
		log.Printf("using trusted tokenizer helper (%s)", tokURL)
	}

	authCfg, err := auth.NewConfig(pepper, insecureCookies)
	if err != nil {
		log.Fatalf("auth init: %v", err)
	}
	catalog := tokenizer.DefaultCatalog()
	hub := ws.NewHub(st, authCfg, catalog)
	srv := httpapi.NewServer(st, authCfg, tok, wal, hub, catalog, devMode, staticDir)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("cordova-server listening on %s (dev=%v)", addr, devMode)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}
