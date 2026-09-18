// Package model 存放 user 服务的持久化模型（GORM）。
package model

import "time"

// User 对应 MySQL 中的 users 表。

type User struct {
	ID uint64 `gorm:"primaryKey;autoIncrement"`

	Username string `gorm:"column:username;type:varchar(64);uniqueIndex;not null"`
	Email    string `gorm:"column:email;type:varchar(128);uniqueIndex;not null"`

	// PasswordHash 只存 bcrypt 哈希后的结果，永远不存明文密码。

	PasswordHash string `gorm:"column:password_hash;type:varchar(255);not null"`

	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (User) TableName() string {
	return "users"
}
