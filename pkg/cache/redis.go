// Package cache 提供 Redis 客户端的构造与通用工具。

package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Config 直接映射 pkg/config 的 RedisConfig，cache 包不感知 Viper 的存在——
// 和 logger.Config 的做法一致（依赖倒置：配置包依赖这里，这里不依赖配置包）。
type Config struct {
	Addr     string
	Password string
	DB       int
}

func New(cfg Config) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("cache: ping redis %s: %w", cfg.Addr, err)
	}
	return rdb, nil
}
