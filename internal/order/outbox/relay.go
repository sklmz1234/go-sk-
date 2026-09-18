// Package outbox 承载本地消息表的投递端：一个内嵌在 order-service 里的
// relay goroutine，周期性扫描 outbox_messages 把到期的消息投递给
// product-service。

package outbox

import (
	"context"
	"encoding/json"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"go-ecom-admin/internal/order/model"
	"go-ecom-admin/internal/order/repository"
	"go-ecom-admin/pkg/identity"
)

const (
	DefaultInterval = 2 * time.Second
	// DefaultBatchSize 是单轮最多投递的消息数，防单轮拖太久；
	// 积压大时每 tick 消化一批，逐步排空。
	DefaultBatchSize = 20

	deliverTimeout = 3 * time.Second
)

type Relay struct {
	outbox    repository.OutboxRepository
	products  repository.ProductClient
	log       *zap.Logger
	tracer    trace.Tracer
	interval  time.Duration
	batchSize int

	delivered metric.Int64Counter
	dead      metric.Int64Counter
	pending   metric.Int64Gauge
}

// NewRelay 组装 relay。interval/batchSize 传 0 用默认值。
func NewRelay(outbox repository.OutboxRepository, products repository.ProductClient, log *zap.Logger, interval time.Duration, batchSize int) *Relay {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}

	meter := otel.Meter("go-ecom-admin/order/outbox")

	delivered, _ := meter.Int64Counter("outbox_delivered_total")
	dead, _ := meter.Int64Counter("outbox_dead_total")
	pending, _ := meter.Int64Gauge("outbox_pending")

	return &Relay{
		outbox:    outbox,
		products:  products,
		log:       log,
		tracer:    otel.GetTracerProvider().Tracer("go-ecom-admin/order/outbox"),
		interval:  interval,
		batchSize: batchSize,
		delivered: delivered,
		dead:      dead,
		pending:   pending,
	}
}

func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("outbox relay stopped")
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Relay) tick(ctx context.Context) {
	ctx, span := r.tracer.Start(ctx, "outbox-relay.tick")
	defer span.End()

	// 先上报积压再抢消息（此时无事务持有连接）：PENDING 总量含退避
	// 等待中的，是"这个集群还有多少回没送达"的真实读数。
	if n, err := r.outbox.CountPending(ctx); err == nil && r.pending != nil {
		r.pending.Record(ctx, n)
	}

	sess, err := r.outbox.ClaimPending(ctx, r.batchSize)
	if err != nil {
		r.log.Error("outbox claim failed", zap.Error(err))
		span.SetStatus(codes.Error, "claim failed")
		return
	}
	if sess == nil {
		return // 本轮无可投递，最常见的路径
	}

	// 投递完成后无论成败都要 Close（提交标记）；漏掉 Close 会让事务
	// 悬着直到连接回收——行锁一直被持有，阻塞其他副本。
	defer func() {
		if err := sess.Close(); err != nil {
			r.log.Error("outbox session close failed", zap.Error(err))
		}
	}()

	for i := range sess.Messages {
		r.deliver(ctx, sess, &sess.Messages[i])
	}
}

// deliver 投递单条消息。成功 MarkSent；失败 MarkFailed（退避/死信在
// session 里算好，dead=true 时补 ERROR 日志）。
func (r *Relay) deliver(ctx context.Context, sess *repository.OutboxSession, m *model.OutboxMessage) {
	ctx, span := r.tracer.Start(ctx, "outbox-relay.deliver",
		trace.WithAttributes(
			attribute.String("outbox.message_id", m.MessageID),
			attribute.String("outbox.type", m.Type),
			attribute.Int("outbox.retry_count", m.RetryCount),
		))
	defer span.End()

	var p model.StockRestorePayload
	if err := json.Unmarshal([]byte(m.Payload), &p); err != nil {
		// 消息体损坏是永久性失败：重试一万次也不会好，
		// 直接死信（MarkDead 跳过退避），别浪费重试窗口。
		r.log.Error("OUTBOX DEAD: undecodable payload",
			zap.Uint64("outbox_id", m.ID), zap.String("message_id", m.MessageID), zap.Error(err))
		span.SetStatus(codes.Error, "undecodable payload")
		if err := sess.MarkDead(ctx, m.ID); err != nil {
			r.log.Error("mark dead after undecodable payload", zap.Error(err))
		}
		if r.dead != nil {
			r.dead.Add(ctx, 1)
		}
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, deliverTimeout)
	err := r.products.RestoreStock(identity.InjectOutgoingSystem(callCtx), p.ProductID, p.Quantity, m.MessageID)
	cancel()
	if err == nil {
		if err := sess.MarkSent(ctx, m.ID); err != nil {
			r.log.Error("mark sent failed", zap.Uint64("outbox_id", m.ID), zap.Error(err))
			span.SetStatus(codes.Error, "mark sent failed")
			return
		}
		if r.delivered != nil {
			r.delivered.Add(ctx, 1)
		}
		span.SetAttributes(attribute.Bool("outbox.delivered", true))
		return
	}

	dead, markErr := sess.MarkFailed(ctx, m.ID, err)
	if markErr != nil {
		r.log.Error("mark failed errored", zap.Uint64("outbox_id", m.ID), zap.Error(markErr))
		span.SetStatus(codes.Error, "mark failed errored")
		return
	}
	span.SetStatus(codes.Error, err.Error())

	if dead {
		// 死信：重试耗尽。到这里说明 product 侧持续故障（如商品已删除），
		// 自动投递放弃，换人工/重放工具处置（4b）。
		r.log.Error("OUTBOX DEAD: delivery exhausted, manual action required",
			zap.Uint64("outbox_id", m.ID),
			zap.String("message_id", m.MessageID),
			zap.Uint64("product_id", p.ProductID),
			zap.Int32("quantity", p.Quantity),
			zap.Uint64("order_id", p.OrderID),
			zap.Int("retries", m.RetryCount+1),
			zap.Error(err),
		)
		if r.dead != nil {
			r.dead.Add(ctx, 1)
		}
		return
	}
	r.log.Warn("outbox delivery failed, will retry with backoff",
		zap.Uint64("outbox_id", m.ID),
		zap.String("message_id", m.MessageID),
		zap.Int("retry_count", m.RetryCount+1),
		zap.Error(err),
	)
}
