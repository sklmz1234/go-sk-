// cart_repository 单元测试：sqlite :memory: 跑真实 GORM 逻辑。
//
// 本文件的重点考点（都是方言敏感/投影敏感的高风险代码）：
//   - AddItem 的 clause.OnConflict 原子累加——INSERT 撞联合唯一键后
//     quantity 必须是"旧值 + n"而不是覆盖（这正是选 upsert 而不是
//     先查后改的全部意义）；
//   - ListByUser 的 JOIN 投影能否把 products 的列正确填进 CartEntry
//     的外层字段（嵌套结构 + 外层投影字段的 GORM 行为）；
//   - 孤儿条目（商品已删）走 LEFT JOIN 不丢行。
package repository

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apperrors "go-ecom-admin/pkg/errors"

	"go-ecom-admin/internal/product/model"
)

func newCartTestDB(t *testing.T) (*gorm.DB, CartRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Product{}, &model.CartItem{}))
	return db, NewGormCartRepository(db)
}

func TestCartRepository_AddItem(t *testing.T) {
	t.Run("首次加购插入新行", func(t *testing.T) {
		db, repo := newCartTestDB(t)
		p := &model.Product{Name: "机械键盘", PriceCents: 29900, Stock: 10}
		require.NoError(t, db.Create(p).Error)

		require.NoError(t, repo.AddItem(context.Background(), 42, p.ID, 2))

		var count int64
		require.NoError(t, db.Model(&model.CartItem{}).Count(&count).Error)
		assert.Equal(t, int64(1), count, "首次加购只应有一行")
		item, err := repo.GetItem(context.Background(), 42, p.ID)
		require.NoError(t, err)
		assert.Equal(t, int32(2), item.Quantity)
	})

	t.Run("重复加购累加不插新行", func(t *testing.T) {
		db, repo := newCartTestDB(t)
		p := &model.Product{Name: "机械键盘", PriceCents: 29900, Stock: 10}
		require.NoError(t, db.Create(p).Error)

		require.NoError(t, repo.AddItem(context.Background(), 42, p.ID, 2))
		require.NoError(t, repo.AddItem(context.Background(), 42, p.ID, 3))

		var count int64
		require.NoError(t, db.Model(&model.CartItem{}).Count(&count).Error)
		assert.Equal(t, int64(1), count, "同用户同商品必须只有一行（联合唯一键）")
		item, err := repo.GetItem(context.Background(), 42, p.ID)
		require.NoError(t, err)
		assert.Equal(t, int32(5), item.Quantity, "quantity 应是 2+3=5，而不是被 3 覆盖")
	})

	t.Run("不同用户的同商品互不影响", func(t *testing.T) {
		db, repo := newCartTestDB(t)
		p := &model.Product{Name: "机械键盘", PriceCents: 29900, Stock: 10}
		require.NoError(t, db.Create(p).Error)

		require.NoError(t, repo.AddItem(context.Background(), 1, p.ID, 2))
		require.NoError(t, repo.AddItem(context.Background(), 2, p.ID, 3))

		i1, err := repo.GetItem(context.Background(), 1, p.ID)
		require.NoError(t, err)
		i2, err := repo.GetItem(context.Background(), 2, p.ID)
		require.NoError(t, err)
		assert.Equal(t, int32(2), i1.Quantity)
		assert.Equal(t, int32(3), i2.Quantity)
	})
}

func TestCartRepository_GetItem_NotFound(t *testing.T) {
	_, repo := newCartTestDB(t)

	_, err := repo.GetItem(context.Background(), 42, 999)
	requireAppCode(t, err, apperrors.CodeNotFound)
}

func TestCartRepository_ListByUser(t *testing.T) {
	db, repo := newCartTestDB(t)
	live := &model.Product{Name: "机械键盘", PriceCents: 29900, Stock: 10, ImageURL: "https://example.com/kb.png"}
	deleted := &model.Product{Name: "已删商品", PriceCents: 1000, Stock: 5}
	require.NoError(t, db.Create(live).Error)
	require.NoError(t, db.Create(deleted).Error)

	require.NoError(t, repo.AddItem(context.Background(), 42, live.ID, 2))
	require.NoError(t, repo.AddItem(context.Background(), 42, deleted.ID, 1))
	// 另一个用户的行不能混进来
	require.NoError(t, repo.AddItem(context.Background(), 7, live.ID, 9))

	// 商品删掉后购物车行还在（无级联清理）——LEFT JOIN 的考点。
	require.NoError(t, db.Delete(&model.Product{}, deleted.ID).Error)

	entries, err := repo.ListByUser(context.Background(), 42)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// 按加购顺序（id 序）：先加的 live 在前。
	assert.Equal(t, live.ID, entries[0].ProductID)
	assert.Equal(t, int32(2), entries[0].Quantity)
	assert.Equal(t, "机械键盘", entries[0].Name, "JOIN products 的实时展示字段")
	assert.Equal(t, int64(29900), entries[0].PriceCents)
	assert.Equal(t, "https://example.com/kb.png", entries[0].ImageURL)
	assert.Equal(t, int32(10), entries[0].Stock)

	assert.Equal(t, deleted.ID, entries[1].ProductID)
	assert.Empty(t, entries[1].Name, "商品已删：LEFT JOIN 补零值，行不能丢")
	assert.Zero(t, entries[1].Stock)
}

func TestCartRepository_UpdateQuantity(t *testing.T) {
	db, repo := newCartTestDB(t)
	p := &model.Product{Name: "机械键盘", PriceCents: 29900, Stock: 10}
	require.NoError(t, db.Create(p).Error)
	require.NoError(t, repo.AddItem(context.Background(), 42, p.ID, 2))

	t.Run("绝对值更新", func(t *testing.T) {
		require.NoError(t, repo.UpdateQuantity(context.Background(), 42, p.ID, 7))
		item, err := repo.GetItem(context.Background(), 42, p.ID)
		require.NoError(t, err)
		assert.Equal(t, int32(7), item.Quantity, "是 SET 7，不是 2+7")
	})

	t.Run("条目不存在返回NotFound", func(t *testing.T) {
		err := repo.UpdateQuantity(context.Background(), 42, 999, 7)
		requireAppCode(t, err, apperrors.CodeNotFound)
	})
}

func TestCartRepository_RemoveItems(t *testing.T) {
	db, repo := newCartTestDB(t)
	p1 := &model.Product{Name: "机械键盘", PriceCents: 29900, Stock: 10}
	p2 := &model.Product{Name: "无线鼠标", PriceCents: 9900, Stock: 50}
	require.NoError(t, db.Create(p1).Error)
	require.NoError(t, db.Create(p2).Error)
	require.NoError(t, repo.AddItem(context.Background(), 42, p1.ID, 2))
	require.NoError(t, repo.AddItem(context.Background(), 42, p2.ID, 3))

	t.Run("删勾选的、留未勾选的", func(t *testing.T) {
		require.NoError(t, repo.RemoveItems(context.Background(), 42, []uint64{p1.ID}))
		entries, err := repo.ListByUser(context.Background(), 42)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, p2.ID, entries[0].ProductID)
	})

	t.Run("幂等：重复删不报错", func(t *testing.T) {
		require.NoError(t, repo.RemoveItems(context.Background(), 42, []uint64{p1.ID}))
	})

	t.Run("别的用户的行删不掉", func(t *testing.T) {
		require.NoError(t, repo.AddItem(context.Background(), 7, p1.ID, 1))
		// 42 想删 7 的行：WHERE user_id 把它挡住。
		require.NoError(t, repo.RemoveItems(context.Background(), 42, []uint64{p1.ID}))
		other, err := repo.GetItem(context.Background(), 7, p1.ID)
		require.NoError(t, err)
		assert.Equal(t, int32(1), other.Quantity)
	})
}
