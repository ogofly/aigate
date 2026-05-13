package usage

import (
	"context"
	"log/slog"
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
				flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				FlushPending(flushCtx, recorder, store)
				cancel()
				return
			case <-ticker.C:
				FlushPending(ctx, recorder, store)
			}
		}
	}()
}

func FlushPending(ctx context.Context, recorder *Recorder, store RollupStore) {
	if recorder == nil || store == nil {
		return
	}
	rollups := recorder.DrainPending()
	if len(rollups) == 0 {
		return
	}
	if err := store.UpsertUsageRollups(ctx, rollups); err != nil {
		slog.Error("usage flush failed", "error", err)
		recorder.RestorePending(rollups)
		return
	}
	slog.Info("usage flush", "rollups", len(rollups))
}
