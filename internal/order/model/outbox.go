// Package model 的 outbox.go：本地消息表模型（阶段 4）。
package model

import "time"

type OutboxMessage struct {
	// ID 自增主键，同时是投递顺序依据（FIFO）。
	ID uint64 `gorm:"primaryKey;autoIncrement"`

	MessageID string `gorm:"column:message_id;type:varchar(36);not null;uniqueIndex"`
	Type      string `gorm:"column:type;type:varchar(32);not null"`
	// Payload 存 JSON（product_id/quantity/order_id）：消息体不与表结构耦合，
	// 未来加新消息类型不用 ALTER。用 datatypes.JSON 语义上更准，
	// 但 string + json.Marshal 更直白，也不用引 gorm.io/datatypes 依赖。
	Payload    string `gorm:"column:payload;type:json;not null"`
	Status     string `gorm:"column:status;type:varchar(16);not null;index"`
	RetryCount int    `gorm:"column:retry_count;not null;default:0"`
	// NextRetryAt 是退避重试的"下次可投递时间"：Claim 只扫
	// next_retry_at <= NOW() 的行，失败后按指数退避推远。
	// sqlite/MySQL 都用 time.Time 映射 datetime。
	NextRetryAt time.Time  `gorm:"column:next_retry_at;not null"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime"`
	SentAt      *time.Time `gorm:"column:sent_at"`
}

func (OutboxMessage) TableName() string {
	return "outbox_messages"
}

// 消息状态机。status 用 string 存（与订单状态同一取舍：库里直接可读）。
const (
	OutboxStatusPending = "PENDING"
	OutboxStatusSent    = "SENT"
	OutboxStatusDead    = "DEAD"
)

const (
	OutboxTypeStockRestore = "stock.restore"
)

// StockRestorePayload 是 OutboxTypeStockRestore 的消息体。
type StockRestorePayload struct {
	ProductID uint64 `json:"product_id"`
	Quantity  int32  `json:"quantity"`
	OrderID   uint64 `json:"order_id"`
}
