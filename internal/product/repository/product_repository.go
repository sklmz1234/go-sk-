// Package repository 负责 product 服务的数据访问
package repository

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	apperrors "go-ecom-admin/pkg/errors"

	"go-ecom-admin/internal/product/model"
)

type Repository interface {
	Create(ctx context.Context, p *model.Product) error
	GetByID(ctx context.Context, id uint64) (*model.Product, error)
	Update(ctx context.Context, p *model.Product) error
	Delete(ctx context.Context, id uint64) error
	List(ctx context.Context, page, pageSize int) ([]*model.Product, int64, error)

	SearchByKeyword(ctx context.Context, keyword string, page, pageSize int) ([]*model.Product, int64, error)
	// ListByIDs 按 id 批量取商品，供搜索回表用。
	// 注意不保证返回顺序——调用方负责按召回顺序重排。
	ListByIDs(ctx context.Context, ids []uint64) ([]*model.Product, error)

	DeductStock(ctx context.Context, productID uint64, quantity int32) (*model.Product, error)
	RestoreStock(ctx context.Context, productID uint64, quantity int32) (*model.Product, error)

	RestoreStockIdempotent(ctx context.Context, messageID string, productID uint64, quantity int32) (*model.Product, error)
}

type gormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

func (r *gormRepository) Create(ctx context.Context, p *model.Product) error {
	if err := r.db.WithContext(ctx).Create(p).Error; err != nil {
		return apperrors.Internal("failed to create product", err)
	}
	return nil
}

func (r *gormRepository) GetByID(ctx context.Context, id uint64) (*model.Product, error) {
	var p model.Product
	if err := r.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NotFound("product not found", err)
		}
		return nil, apperrors.Internal("failed to query product", err)
	}
	return &p, nil
}

func (r *gormRepository) Update(ctx context.Context, p *model.Product) error {
	result := r.db.WithContext(ctx).Model(&model.Product{}).Where("id = ?", p.ID).Updates(map[string]any{
		"name":        p.Name,
		"price_cents": p.PriceCents,
		"stock":       p.Stock,
		// 阶段 5A：整体替换语义必须覆盖全部可写字段，否则前端改图/描述会静默不生效。
		// owner_id 依然不在其中——归属不可转让。
		"description": p.Description,
		"image_url":   p.ImageURL,
	})
	if result.Error != nil {
		return apperrors.Internal("failed to update product", result.Error)
	}
	if result.RowsAffected == 0 {
		return apperrors.NotFound("product not found", nil)
	}
	return nil
}

// Delete 同样靠 RowsAffected 判断目标是否存在，理由和 Update 一致。
func (r *gormRepository) Delete(ctx context.Context, id uint64) error {
	result := r.db.WithContext(ctx).Delete(&model.Product{}, id)
	if result.Error != nil {
		return apperrors.Internal("failed to delete product", result.Error)
	}
	if result.RowsAffected == 0 {
		return apperrors.NotFound("product not found", nil)
	}
	return nil
}

// List 用最朴素的 offset/limit 分页——阶段 1 数据量小，等以后接入真实业务
// 再按需要换成游标分页，不提前做过度设计。
func (r *gormRepository) List(ctx context.Context, page, pageSize int) ([]*model.Product, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}

	var products []*model.Product
	var total int64

	db := r.db.WithContext(ctx).Model(&model.Product{})
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, apperrors.Internal("failed to count products", err)
	}

	offset := (page - 1) * pageSize
	if err := db.Offset(offset).Limit(pageSize).Find(&products).Error; err != nil {
		return nil, 0, apperrors.Internal("failed to list products", err)
	}

	return products, total, nil
}

// SearchByKeyword 是搜索的降级实现
func (r *gormRepository) SearchByKeyword(ctx context.Context, keyword string, page, pageSize int) ([]*model.Product, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}

	escaped := strings.NewReplacer(`!`, `!!`, `%`, `!%`, `_`, `!_`).Replace(keyword)
	like := "%" + escaped + "%"

	var products []*model.Product
	var total int64

	db := r.db.WithContext(ctx).Model(&model.Product{}).
		Where(`(name LIKE ? ESCAPE '!' OR description LIKE ? ESCAPE '!')`, like, like)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, apperrors.Internal("failed to count products by keyword", err)
	}

	offset := (page - 1) * pageSize
	if err := db.Offset(offset).Limit(pageSize).Find(&products).Error; err != nil {
		return nil, 0, apperrors.Internal("failed to search products by keyword", err)
	}

	return products, total, nil
}

func (r *gormRepository) ListByIDs(ctx context.Context, ids []uint64) ([]*model.Product, error) {
	if len(ids) == 0 {
		return []*model.Product{}, nil
	}
	var products []*model.Product
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&products).Error; err != nil {
		return nil, apperrors.Internal("failed to list products by ids", err)
	}
	return products, nil
}

func (r *gormRepository) DeductStock(ctx context.Context, productID uint64, quantity int32) (*model.Product, error) {
	result := r.db.WithContext(ctx).Model(&model.Product{}).
		Where("id = ? AND stock >= ?", productID, quantity).
		Update("stock", gorm.Expr("stock - ?", quantity))
	if result.Error != nil {
		return nil, apperrors.Internal("failed to deduct stock", result.Error)
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := r.db.WithContext(ctx).Model(&model.Product{}).Where("id = ?", productID).Count(&count).Error; err != nil {
			return nil, apperrors.Internal("failed to check product existence", err)
		}
		if count == 0 {
			return nil, apperrors.NotFound("product not found", nil)
		}
		return nil, apperrors.FailedPrecondition("insufficient stock", nil)
	}

	// 读回更新后的完整记录：调用方需要 remaining stock，order-service 还需要
	// price_cents/name 做下单时刻的快照（见 proto DeductStockResponse 的注释）。
	return r.GetByID(ctx, productID)
}

// RestoreStock 是 DeductStock 的补偿操作：无条件加回库存。
// 它不幂等（重复调用会重复加），幂等性由调用方（order-service 的编排逻辑）
// 保证。消息驱动的回补（可能重复投递）应该走 RestoreStockIdempotent。
func (r *gormRepository) RestoreStock(ctx context.Context, productID uint64, quantity int32) (*model.Product, error) {
	result := r.db.WithContext(ctx).Model(&model.Product{}).
		Where("id = ?", productID).
		Update("stock", gorm.Expr("stock + ?", quantity))
	if result.Error != nil {
		return nil, apperrors.Internal("failed to restore stock", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, apperrors.NotFound("product not found", nil)
	}
	return r.GetByID(ctx, productID)
}

// RestoreStockIdempotent 是消息驱动回补的幂等版本
func (r *gormRepository) RestoreStockIdempotent(ctx context.Context, messageID string, productID uint64, quantity int32) (*model.Product, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model.StockRestore{
			MessageID: messageID,
			ProductID: productID,
			Quantity:  quantity,
		}).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				// 已处理过：事务里什么都不做直接提交（空事务），
				// 幂等语义=重复调用与第一次调用的可观察结果一致。
				return nil
			}
			return apperrors.Internal("failed to record stock restore", err)
		}
		result := tx.Model(&model.Product{}).
			Where("id = ?", productID).
			Update("stock", gorm.Expr("stock + ?", quantity))
		if result.Error != nil {
			return apperrors.Internal("failed to restore stock", result.Error)
		}
		if result.RowsAffected == 0 {
			return apperrors.NotFound("product not found", nil)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 无论本次是真回补还是去重命中，读回当前库存返回即可——
	// 调用方（relay）只关心"成功了没有"，remaining 用于日志。
	return r.GetByID(ctx, productID)
}
