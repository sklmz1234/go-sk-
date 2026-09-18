package database

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ConnectConfig 控制连接重试行为。零值可用：默认重试总时长 2 分钟，
// 退避从 1s 起指数增长、封顶 10s。
type ConnectConfig struct {
	// MaxWait
	MaxWait time.Duration
	// MinDelay
	MinDelay time.Duration
	// MaxDelay
	MaxDelay time.Duration
	// Log
	Log *zap.Logger
}

func (c *ConnectConfig) fillDefaults() {
	if c.MaxWait <= 0 {
		c.MaxWait = 2 * time.Minute
	}
	if c.MinDelay <= 0 {
		c.MinDelay = time.Second
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = 10 * time.Second
	}
	if c.MaxDelay < c.MinDelay {
		c.MaxDelay = c.MinDelay
	}
}

func ConnectWithRetry(ctx context.Context, open func() (*gorm.DB, error), cfg ConnectConfig) (*gorm.DB, error) {
	cfg.fillDefaults()
	deadline := time.Now().Add(cfg.MaxWait)
	delay := cfg.MinDelay

	for attempt := 1; ; attempt++ {
		db, err := open()
		if err == nil {
			if attempt > 1 && cfg.Log != nil {
				cfg.Log.Info("mysql connected after retries", zap.Int("attempts", attempt))
			}
			return db, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("mysql unreachable after %d attempts over %s: %w", attempt, cfg.MaxWait, err)
		}

		wait := min(delay, remaining)
		if cfg.Log != nil {
			cfg.Log.Warn("mysql not ready, will retry",
				zap.Int("attempt", attempt),
				zap.Duration("retry_in", wait),
				zap.Error(err))
		}

		if !sleep(ctx, wait) {
			return nil, fmt.Errorf("mysql connect retry canceled (attempt %d): %w", attempt, err)
		}
		delay = min(delay*2, cfg.MaxDelay)
	}
}

var sleep = func(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
