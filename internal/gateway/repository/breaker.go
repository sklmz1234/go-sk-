// Package repository 的熔断器
// 状态机，gobreaker 实现，和 Hystrix 同一模型
package repository

import (
	"time"

	"github.com/sony/gobreaker"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newBreaker(name string, log *zap.Logger) *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        name,
		MaxRequests: 3,
		Interval:    10 * time.Second,
		Timeout:     10 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= 5
		},
		// 状态迁移是"事故信号"：打开意味着下游出事了，闭合意味着恢复，
		// 都必须能在日志里搜到（WARN 级，本地 console 编码下肉眼可见）。
		OnStateChange: func(name string, from, to gobreaker.State) {
			log.Warn("circuit breaker state changed",
				zap.String("breaker", name),
				zap.String("from", from.String()),
				zap.String("to", to.String()))
		},
	})
}

func isInfraError(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		// 非 gRPC 错误（如连接层失败）视为基础设施问题，计入。
		return true
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Internal:
		return true
	default:
		return false
	}
}

// callWithBreaker 用熔断器包一次下游调用，泛型 T 是调用的返回值类型。

func callWithBreaker[T any](cb *gobreaker.CircuitBreaker, fn func() (T, error)) (T, error) {
	type result struct {
		val T
		err error // 业务错误：透传但不计熔断失败
	}

	out, err := cb.Execute(func() (interface{}, error) {
		val, callErr := fn()
		switch {
		case callErr == nil:
			return result{val: val}, nil
		case isInfraError(callErr):
			return nil, callErr // 基础设施错误：计入失败，触发熔断统计
		default:
			return result{val: val, err: callErr}, nil // 业务错误：向熔断器报成功
		}
	})
	if err != nil {
		var zero T
		if err == gobreaker.ErrOpenState || err == gobreaker.ErrTooManyRequests {
			return zero, status.Error(codes.Unavailable, "service temporarily unavailable, please retry later")
		}
		return zero, err
	}
	res := out.(result)
	return res.val, res.err
}
