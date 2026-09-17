// 阶段 5B：购物车条目的持久化模型。与 Product 同库（cart_items 表），
// ListCart 的 JOIN 在单库内完成，网关免跨服务聚合。
package model

import "time"

// CartItem 对应 MySQL 中的 cart_items 表。
//
// 设计要点（对照《阶段5B-购物车方案》）：
//   - (user_id, product_id) 联合唯一：同一用户同一商品只有一行，
//     重复加购走"数量累加"而不是插新行——唯一键是并发累加的兜底，
//     撞键时用 ON DUPLICATE KEY UPDATE 原子加，不存在"先查后改"的丢更新窗口。
//   - 不存价格快照：价格/名称/图片/库存全部 JOIN products 实时取。
//     快照的正确位置是订单（下单时刻 DeductStock 给出），车里存一份
//     只会多出一份要维护一致性的数据。
//   - quantity 上限 99（一个人不会往车里放一百件同一商品，单行封顶
//     也让 int32 与展示组件都不会溢出语义）。
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

// CartEntry 不是一张表，是 ListByUser 的 JOIN 投影：购物车行内嵌 +
// products 的实时展示字段。嵌套 CartItem 让 GORM 能把 cart_items.* 的列
// 正确回填进嵌套结构，SELECT 里显式点名的 products 列则落到外层字段。
// 不落库、不迁移，只存在于 repository -> service 的返回路径上。
type CartEntry struct {
	CartItem
	// 以下四个字段来自 JOIN products。商品被删时（LEFT JOIN）它们是零值：
	// Name 为空 + Stock 为 0 即"已下架"的信号，前端据此禁勾选。
	Name       string `gorm:"column:name"`
	PriceCents int64  `gorm:"column:price_cents"`
	ImageURL   string `gorm:"column:image_url"`
	Stock      int32  `gorm:"column:stock"`
}
