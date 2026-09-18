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
