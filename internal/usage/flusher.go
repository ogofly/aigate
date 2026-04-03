package usage

import (
	"context"
	"log"
	"time"
)

type RollupStore interface {
	UpsertUsageRollups(ctx context.Context, rollups []Rollup) error
}

func StartFlushLoop(ctx context.Context, recorder *Recorder, store RollupStore, interval time.Duration) {
	if recorder == nil || store == nil {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				flushOnce(ctx, recorder, store)
				return
			case <-ticker.C:
				flushOnce(ctx, recorder, store)
			}
		}
	}()
}

func flushOnce(ctx context.Context, recorder *Recorder, store RollupStore) {
	rollups := recorder.DrainPending()
	if len(rollups) == 0 {
		return
	}
	if err := store.UpsertUsageRollups(ctx, rollups); err != nil {
		log.Printf("usage_flush error=%v", err)
		recorder.RestorePending(rollups)
		return
	}
	log.Printf("usage_flush rollups=%d", len(rollups))
}
