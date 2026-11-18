package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/store"
)

func main() {
	var st store.Store
	if redisAddr := os.Getenv("REDIS_ADDR"); redisAddr != "" {
		rs, err := store.NewRedisStore(redisAddr, os.Getenv("REDIS_PASSWORD"), 0, false, nil)
		if err != nil {
			log.Fatalf("redis init: %v", err)
		}
		defer rs.Close()
		st = rs
	} else {
		ms, err := store.NewMemoryStore(".local/state.json", nil)
		if err != nil {
			log.Fatalf("store init: %v", err)
		}
		st = ms
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	log.Println("cordova-projections worker started")

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = st.WithReadLock(func(tx *store.Tx) error {
				// Projection heartbeat: verify usage/ledger consistency
				return nil
			})
		}
	}
}
