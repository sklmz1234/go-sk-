// Package logger 封装 Zap 的初始化逻辑。

package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Config struct {
	Level       string
	Encoding    string
	OutputPaths []string
}

// New 根据 Config 构建一个可用的 *zap.Logger。
func New(cfg Config) (*zap.Logger, error) {
	level, err := zapcore.ParseLevel(defaultIfEmpty(cfg.Level, "info"))
	if err != nil {
		return nil, fmt.Errorf("logger: parse level %q: %w", cfg.Level, err)
	}

	encoding := defaultIfEmpty(cfg.Encoding, "console")
	outputPaths := cfg.OutputPaths
	if len(outputPaths) == 0 {
		outputPaths = []string{"stdout"}
	}

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder

	zapCfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Development:      false,
		Encoding:         encoding,
		EncoderConfig:    encoderCfg,
		OutputPaths:      outputPaths,
		ErrorOutputPaths: []string{"stderr"},
	}

	l, err := zapCfg.Build(zap.AddCallerSkip(0))
	if err != nil {
		return nil, fmt.Errorf("logger: build zap logger: %w", err)
	}
	return l, nil
}

func defaultIfEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
