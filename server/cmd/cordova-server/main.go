package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/auth"
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

	st, err := store.NewMemoryStore(snapPath, nil)
	if err != nil {
		log.Fatalf("store init: %v", err)
	}
	authCfg, err := auth.NewConfig(pepper, insecureCookies)
	if err != nil {
		log.Fatalf("auth init: %v", err)
	}
	catalog := tokenizer.DefaultCatalog()
	hub := ws.NewHub(st, authCfg, catalog)
	srv := httpapi.NewServer(st, authCfg, tokenizer.SimpleEstimator{}, wallet.NewMockWallet(netName), hub, catalog, devMode, staticDir)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("cordova-server listening on %s (dev=%v, snapshot=%s)", addr, devMode, snapPath)
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
