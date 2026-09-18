// outbox_repository.go：本地消息表的数据访问

package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	apperrors "go-ecom-admin/pkg/errors"

	"go-ecom-admin/internal/order/model"
)

// MaxOutboxRetry 是重试上限
const MaxOutboxRetry = 10

// OutboxRepository 是本地消息表的 repo 契约。
type OutboxRepository interface {
	CancelWithOutbox(ctx context.Context, orderID uint64, from, to string, messages []*model.OutboxMessage) error

	ClaimPending(ctx context.Context, limit int) (*OutboxSession, error)

	CountPending(ctx context.Context) (int64, error)
}

type OutboxSession struct {
	// Messages 是本轮抢到的消息（按 id 升序，FIFO 投递）。
	Messages []model.OutboxMessage
	tx       *gorm.DB
}

// MarkSent 标记投递成功（SENT + sent_at）。行锁在手上，直接按 id 更新。
func (s *OutboxSession) MarkSent(ctx context.Context, id uint64) error {
	if s.tx == nil {
		return apperrors.Internal("outbox session already closed", nil)
	}
	err := s.tx.WithContext(ctx).Model(&model.OutboxMessage{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":  model.OutboxStatusSent,
			"sent_at": time.Now(),
		}).Error
	if err != nil {
		return apperrors.Internal("failed to mark outbox message sent", err)
	}
	return nil
}

func (s *OutboxSession) MarkFailed(ctx context.Context, id uint64, cause error) (dead bool, err error) {
	if s.tx == nil {
		return false, apperrors.Internal("outbox session already closed", nil)
	}
	// retry_count 取自 Claim 时读到的内存值——行锁在手上，读后无人能改，
	// 不需要再查一次库。
	var claimed *model.OutboxMessage
	for i := range s.Messages {
		if s.Messages[i].ID == id {
			claimed = &s.Messages[i]
			break
		}
	}
	if claimed == nil {
		return false, apperrors.Internal("outbox message not in this session", nil)
	}

	newRetry := claimed.RetryCount + 1
	updates := map[string]any{"retry_count": newRetry}

	if newRetry >= MaxOutboxRetry {
		updates["status"] = model.OutboxStatusDead
		dead = true
	} else {
		// 指数退避：2^retry 秒封顶 5 分钟（1 次失败 2s、2 次 4s……
		// 8 次 256s、9 次 300s）。
		backoff := time.Duration(1<<newRetry) * time.Second
		if backoff > 5*time.Minute {
			backoff = 5 * time.Minute
		}
		updates["next_retry_at"] = time.Now().Add(backoff)
	}

	if err := s.tx.WithContext(ctx).Model(&model.OutboxMessage{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return false, apperrors.Internal("failed to mark outbox message failed", err)
	}
	return dead, nil
}

// MarkDead 直接置 DEAD（跳过退避计数），给"重试也不会好"的永久性失败
// （消息体损坏、payload 语义非法）用；普通投递失败走 MarkFailed 的
// 退避路径——网络故障是暂时的，不该陪葬。
func (s *OutboxSession) MarkDead(ctx context.Context, id uint64) error {
	if s.tx == nil {
		return apperrors.Internal("outbox session already closed", nil)
	}
	err := s.tx.WithContext(ctx).Model(&model.OutboxMessage{}).
		Where("id = ?", id).
		Update("status", model.OutboxStatusDead).Error
	if err != nil {
		return apperrors.Internal("failed to mark outbox message dead", err)
	}
	return nil
}

func (s *OutboxSession) Close() error {
	if s.tx == nil {
		return nil
	}
	err := s.tx.Commit().Error
	s.tx = nil
	return err
}

func (r *gormRepository) ClaimPending(ctx context.Context, limit int) (*OutboxSession, error) {
	tx := r.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, apperrors.Internal("failed to begin outbox claim tx", tx.Error)
	}

	query := tx.Model(&model.OutboxMessage{}).
		Where("status = ? AND next_retry_at <= ?", model.OutboxStatusPending, time.Now()).
		Order("id ASC").
		Limit(limit)

	if tx.Dialector.Name() == "mysql" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	}

	var msgs []model.OutboxMessage
	if err := query.Find(&msgs).Error; err != nil {
		tx.Rollback()
		return nil, apperrors.Internal("failed to claim outbox messages", err)
	}
	if len(msgs) == 0 {
		// 本轮无可投递：回滚结束事务（空事务提交也一样，回滚更省一次 fsync 语义）。
		tx.Rollback()
		return nil, nil
	}
	return &OutboxSession{Messages: msgs, tx: tx}, nil
}

func NewOutboxRepository(db *gorm.DB) OutboxRepository {
	return &gormRepository{db: db}
}

func (r *gormRepository) CancelWithOutbox(ctx context.Context, orderID uint64, from, to string, messages []*model.OutboxMessage) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.Order{}).
			Where("id = ? AND status = ?", orderID, from).
			Update("status", to)
		if result.Error != nil {
			return apperrors.Internal("failed to update order status", result.Error)
		}
		// 迁移不命中（状态被并发改掉）：整个事务回滚，一条消息都不写。
		if result.RowsAffected == 0 {
			return apperrors.FailedPrecondition("order status changed concurrently", nil)
		}
		if len(messages) == 0 {
			return nil
		}
		if err := tx.Create(&messages).Error; err != nil {
			return apperrors.Internal("failed to write outbox messages", err)
		}
		return nil
	})
}

func (r *gormRepository) CountPending(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&model.OutboxMessage{}).
		Where("status = ?", model.OutboxStatusPending).
		Count(&n).Error; err != nil {
		return 0, apperrors.Internal("failed to count pending outbox messages", err)
	}
	return n, nil
}
