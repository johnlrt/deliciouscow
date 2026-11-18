package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/ranking"
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	log.Println("cordova-ranker worker started")

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := st.Now()
			_ = st.WithLock(func(tx *store.Tx) error {
				for _, n := range tx.Nodes {
					n.RankScore = ranking.ComputeRankScore(n, now)
				}
				return nil
			})
		}
	}
}
