// 购物车条目的持久化模型
package model

import "time"

// CartItem 对应 MySQL 中的 cart_items 表。

type CartItem struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	UserID    uint64    `gorm:"column:user_id;not null;uniqueIndex:idx_user_product"`
	ProductID uint64    `gorm:"column:product_id;not null;uniqueIndex:idx_user_product"`
	Quantity  int32     `gorm:"column:quantity;not null"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (CartItem) TableName() string {
	return "cart_items"
}

type CartEntry struct {
	CartItem
	// 以下四个字段来自 JOIN products。商品被删时（LEFT JOIN）它们是零值：
	// Name 为空 + Stock 为 0 即"已下架"的信号，前端据此禁勾选。
	Name       string `gorm:"column:name"`
	PriceCents int64  `gorm:"column:price_cents"`
	ImageURL   string `gorm:"column:image_url"`
	Stock      int32  `gorm:"column:stock"`
}
