// 阶段 5B：购物车的数据访问。独立成 CartRepository 接口而不是并进上面的
// Repository——那份接口被缓存/搜索两层装饰器包裹（cached_repository.go /
// search_repository.go），购物车既不需要缓存（强个人化数据没有共享收益）
// 也不需要搜索，混进去只会让装饰器被迫处理无关方法。
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
	// GetItem 取单个条目（服务层用它做"累加后是否超过 99"的前置校验）。
	GetItem(ctx context.Context, userID, productID uint64) (*model.CartItem, error)
	// AddItem 原子累加：联合唯一键撞键时 quantity = quantity + n（不插新行），
	// 并发重复加购不丢更新——不存在"先查后改"的读改写窗口。
	AddItem(ctx context.Context, userID, productID uint64, quantity int32) error
	// ListByUser 返回该用户的购物车（按加购顺序），展示字段 JOIN products 实时取。
	ListByUser(ctx context.Context, userID uint64) ([]*model.CartEntry, error)
	// UpdateQuantity 绝对值语义：把条目数量设为 quantity。条目不存在返回 NotFound。
	UpdateQuantity(ctx context.Context, userID, productID uint64, quantity int32) error
	// RemoveItems 批量删除，幂等：删不存在的条目不报错（DELETE 的
	// RowsAffected 对幂等删除没有业务含义，结算清车可以安全重试）。
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

// AddItem 用 upsert 实现原子累加。GORM 的 clause.OnConflict 会按方言生成
// 两条等价 SQL：MySQL 是 ON DUPLICATE KEY UPDATE quantity = quantity + ?，
// SQLite（单测用）是 ON CONFLICT (user_id, product_id) DO UPDATE SET
// quantity = quantity + ?——两种方言里 SET 右侧的裸列名都指"已存在那行"，
// 所以同一条 gorm.Expr 在两边都是正确的累加语义，单测和真库行为一致。
//
// 上限 99 的封顶在 service 层前置校验（见 cart_service.go 的注释）；
// 极端并发下的瞬时超限不回滚——多买两件是业务可接受误差，为它引入
// 行锁不值得。
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

// ListByUser 用 LEFT JOIN 而不是 INNER JOIN：商品被删后购物车行还在（本项目
// 没有级联清车），LEFT JOIN 让这些"孤儿条目"以 name 为空、stock 为 0 的形态
// 出现，前端可以渲染成"已下架"并禁止勾选——INNER JOIN 会让它们凭空消失，
// 用户会以为是丢数据。按 cart_items.id 排序 = 按加购先后顺序，符合直觉。
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

// UpdateQuantity 条件更新 + RowsAffected 判定，模式同 product_repository.Update：
// 一条 UPDATE 同时完成"条目存在性检查 + 更新"，省一次查询且无 TOCTOU 窗口。
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
