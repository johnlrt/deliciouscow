package main

import (
	"context"
	"fmt"
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
	"github.com/johnlrt/deliciouscow/server/internal/worker"
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

	tok, pinnedModels, err := configureTokenizer(
		os.Getenv("TOKENIZER_URL"),
		os.Getenv("TOKENIZER_SECRET"),
		getenv("CATALOG_VERSION", "v1-m1"),
		os.Getenv("TOKENIZER_MODEL_PINS_FILE"),
	)
	if err != nil {
		log.Fatalf("tokenizer init: %v", err)
	}
	if tokURL := os.Getenv("TOKENIZER_URL"); tokURL != "" {
		log.Printf("using trusted tokenizer helper (%s, pinned_models=%d)", tokURL, pinnedModels)
	} else {
		log.Printf("using SimpleEstimator tokenizer (unverified, non-billable)")
	}

	authCfg, err := auth.NewConfig(pepper, insecureCookies)
	if err != nil {
		log.Fatalf("auth init: %v", err)
	}
	catalog := tokenizer.DefaultCatalog()
	hub := ws.NewHub(st, authCfg, catalog, bus)
	srv := httpapi.NewServer(st, authCfg, tok, wal, hub, catalog, devMode, staticDir)

	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	go worker.Run(workerCtx, st, wal, 15*time.Second)

	if metricsAddr := os.Getenv("METRICS_ADDR"); metricsAddr != "" {
		mmux := http.NewServeMux()
		mmux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			_ = st.WithReadLock(func(tx *store.Tx) error {
				var active int64
				for _, n := range tx.Nodes {
					if n.Status == "online" {
						active++
					}
				}
				srv.Metrics.ActiveNodes.Store(active)
				srv.Metrics.LedgerEntries.Store(int64(len(tx.Ledger)))
				return nil
			})
			srv.Metrics.Handler().ServeHTTP(w, r)
		})
		go func() {
			log.Printf("metrics listening on %s", metricsAddr)
			_ = http.ListenAndServe(metricsAddr, mmux)
		}()
	}

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
	cancelWorker()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

func configureTokenizer(tokURL, tokSecret, catalogVersion, pinsPath string) (tokenizer.Tokenizer, int, error) {
	if pinsPath != "" && tokURL == "" {
		return nil, 0, fmt.Errorf("TOKENIZER_MODEL_PINS_FILE requires TOKENIZER_URL (SimpleEstimator cannot verify model pins)")
	}
	if tokURL == "" {
		return tokenizer.SimpleEstimator{}, 0, nil
	}
	hc := tokenizer.NewHelperClient(tokURL, tokSecret, catalogVersion)
	if pinsPath == "" {
		return hc, 0, nil
	}
	pins, err := tokenizer.LoadModelPinsFile(pinsPath)
	if err != nil {
		return nil, 0, err
	}
	if _, err := hc.WithModelPins(pins); err != nil {
		return nil, 0, err
	}
	return hc, len(pins), nil
}
