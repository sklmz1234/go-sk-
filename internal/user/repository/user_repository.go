// Package repository 负责 user 服务的数据访问，是唯一允许出现 GORM/SQL 细节的地方。

package repository

import (
	"context"
	"errors"
	"math/rand"

	"gorm.io/gorm"

	apperrors "go-ecom-admin/pkg/errors"

	"go-ecom-admin/internal/user/model"
)

// Repository 是 user 服务的数据访问接口。
type Repository interface {
	Create(ctx context.Context, u *model.User) error
	GetByID(ctx context.Context, id uint64) (*model.User, error)
	GetByUsername(ctx context.Context, username string) (*model.User, error)

	GetRandomAddress(ctx context.Context) (*model.Address, error)
}

type gormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

func (r *gormRepository) Create(ctx context.Context, u *model.User) error {
	if err := r.db.WithContext(ctx).Create(u).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return apperrors.AlreadyExists("username or email already exists", err)
		}
		return apperrors.Internal("failed to create user", err)
	}
	return nil
}

func (r *gormRepository) GetByID(ctx context.Context, id uint64) (*model.User, error) {
	var u model.User
	if err := r.db.WithContext(ctx).First(&u, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NotFound("user not found", err)
		}
		return nil, apperrors.Internal("failed to query user", err)
	}
	return &u, nil
}

func (r *gormRepository) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	var u model.User
	if err := r.db.WithContext(ctx).Where("username = ?", username).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NotFound("user not found", err)
		}
		return nil, apperrors.Internal("failed to query user", err)
	}
	return &u, nil
}

func (r *gormRepository) GetRandomAddress(ctx context.Context) (*model.Address, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.Address{}).Count(&count).Error; err != nil {
		return nil, apperrors.Internal("failed to count addresses", err)
	}
	if count == 0 {
		return nil, apperrors.NotFound("no addresses available", nil)
	}

	var a model.Address
	if err := r.db.WithContext(ctx).
		Offset(rand.Intn(int(count))).
		First(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// COUNT 和 First 之间有行被删的窗口：按池空处理，
			// 调用方重试一次即可拿到。
			return nil, apperrors.NotFound("no addresses available", err)
		}
		return nil, apperrors.Internal("failed to query random address", err)
	}
	return &a, nil
}
