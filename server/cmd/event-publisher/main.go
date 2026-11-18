package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/events"
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

	var bus events.Bus = events.NoopBus{}
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		kb, err := events.NewKafkaBus(strings.Split(brokers, ","))
		if err != nil {
			log.Fatalf("kafka init: %v", err)
		}
		defer kb.Close()
		bus = kb
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	log.Println("cordova-event-publisher started")

	var lastIdx int
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = st.WithReadLock(func(tx *store.Tx) error {
				for lastIdx < len(tx.Ledger) {
					e := tx.Ledger[lastIdx]
					_ = bus.Publish(ctx, events.TopicAccountUsage, e.OperationID, map[string]any{
						"ledger_id":    e.LedgerID,
						"operation_id": e.OperationID,
						"kind":         e.Kind,
						"request_id":   e.RequestID,
						"created_at":   e.CreatedAt.Format(time.RFC3339),
					})
					lastIdx++
				}
				return nil
			})
		}
	}
}
