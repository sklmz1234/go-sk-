// 购物车的数据访问。独立成 CartRepository 接口而不是并进上面的

package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	apperrors "go-ecom-admin/pkg/errors"

	"go-ecom-admin/internal/product/model"
)

// CartRepository 是购物车的数据访问接口。
type CartRepository interface {
	GetItem(ctx context.Context, userID, productID uint64) (*model.CartItem, error)

	AddItem(ctx context.Context, userID, productID uint64, quantity int32) error
	// ListByUser 返回该用户的购物车（按加购顺序），展示字段 JOIN products 实时取。
	ListByUser(ctx context.Context, userID uint64) ([]*model.CartEntry, error)

	UpdateQuantity(ctx context.Context, userID, productID uint64, quantity int32) error

	RemoveItems(ctx context.Context, userID uint64, productIDs []uint64) error
}

type gormCartRepository struct {
	db *gorm.DB
}

func NewGormCartRepository(db *gorm.DB) CartRepository {
	return &gormCartRepository{db: db}
}

func (r *gormCartRepository) GetItem(ctx context.Context, userID, productID uint64) (*model.CartItem, error) {
	var item model.CartItem
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND product_id = ?", userID, productID).
		First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NotFound("cart item not found", err)
		}
		return nil, apperrors.Internal("failed to query cart item", err)
	}
	return &item, nil
}

// AddItem 用 upsert 实现原子累加
func (r *gormCartRepository) AddItem(ctx context.Context, userID, productID uint64, quantity int32) error {
	item := &model.CartItem{UserID: userID, ProductID: productID, Quantity: quantity}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "product_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"quantity": gorm.Expr("quantity + ?", quantity),
		}),
	}).Create(item).Error
	if err != nil {
		return apperrors.Internal("failed to add cart item", err)
	}
	return nil
}

// ListByUser 用 LEFT JOIN
func (r *gormCartRepository) ListByUser(ctx context.Context, userID uint64) ([]*model.CartEntry, error) {
	var entries []*model.CartEntry
	err := r.db.WithContext(ctx).
		Model(&model.CartItem{}).
		Select("cart_items.*, products.name, products.price_cents, products.image_url, products.stock").
		Joins("LEFT JOIN products ON products.id = cart_items.product_id").
		Where("cart_items.user_id = ?", userID).
		Order("cart_items.id").
		Find(&entries).Error
	if err != nil {
		return nil, apperrors.Internal("failed to list cart", err)
	}
	return entries, nil
}

func (r *gormCartRepository) UpdateQuantity(ctx context.Context, userID, productID uint64, quantity int32) error {
	result := r.db.WithContext(ctx).
		Model(&model.CartItem{}).
		Where("user_id = ? AND product_id = ?", userID, productID).
		Update("quantity", quantity)
	if result.Error != nil {
		return apperrors.Internal("failed to update cart item", result.Error)
	}
	if result.RowsAffected == 0 {
		return apperrors.NotFound("cart item not found", nil)
	}
	return nil
}

func (r *gormCartRepository) RemoveItems(ctx context.Context, userID uint64, productIDs []uint64) error {
	if len(productIDs) == 0 {
		// 空列表在 service 层就会被拦（InvalidArgument），这里是防御：
		// 不带条件的 DELETE 会清掉整个用户的购物车，宁可短路。
		return nil
	}
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND product_id IN ?", userID, productIDs).
		Delete(&model.CartItem{}).Error; err != nil {
		return apperrors.Internal("failed to remove cart items", err)
	}
	return nil
}
