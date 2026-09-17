// 阶段 5B：收货地址的 mock 数据池。刻意不做 user_id 归属——它不是
// 用户的地址簿，而是 seed 灌的一池假数据，GetRandomAddress 随机取一条
// 给结算/支付页展示。真实地址簿（归属 + CRUD + 默认地址）是以后独立的
// 小阶段，到时在这张表上加 user_id 列即可演进。
package model

import "time"

// Address 对应 MySQL 中的 addresses 表（mock 收货信息池）。
type Address struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	ReceiverName string    `gorm:"column:receiver_name;type:varchar(32);not null"`
	Phone        string    `gorm:"column:phone;type:varchar(20);not null"`
	Address      string    `gorm:"column:address;type:varchar(255);not null"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (Address) TableName() string {
	return "addresses"
}
