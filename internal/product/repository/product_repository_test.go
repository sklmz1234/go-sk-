// product 服务 repository 层单元测试：sqlite :memory: 跑真实 GORM 逻辑。

package repository

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apperrors "go-ecom-admin/pkg/errors"

	"go-ecom-admin/internal/product/model"
)

func newSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Product{}, &model.StockRestore{}))
	return db
}

func requireAppCode(t *testing.T, err error, want apperrors.Code) {
	t.Helper()
	require.Error(t, err)
	var appErr *apperrors.AppError
	require.True(t, errors.As(err, &appErr), "应该返回 *AppError，实际是 %T: %v", err, err)
	assert.Equal(t, want, appErr.Code)
}

func seedProduct(t *testing.T, repo Repository, name string, price int64, stock int32) *model.Product {
	t.Helper()
	p := &model.Product{Name: name, PriceCents: price, Stock: stock}
	require.NoError(t, repo.Create(context.Background(), p))
	return p
}

func TestGetByID_NotFound(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))

	p, err := repo.GetByID(context.Background(), 999)

	require.Nil(t, p)
	requireAppCode(t, err, apperrors.CodeNotFound)
}

func TestDeductStock(t *testing.T) {
	t.Run("扣减成功并返回剩余库存和价格快照", func(t *testing.T) {
		repo := NewGormRepository(newSQLiteDB(t))
		seeded := seedProduct(t, repo, "机械键盘", 19900, 10)

		p, err := repo.DeductStock(context.Background(), seeded.ID, 3)

		require.NoError(t, err)
		assert.Equal(t, int32(7), p.Stock, "10 - 3 应剩 7")
		assert.Equal(t, int64(19900), p.PriceCents, "下单方需要扣减时刻的价格快照")
		assert.Equal(t, "机械键盘", p.Name)
	})

	t.Run("库存不足返回FailedPrecondition且库存不变", func(t *testing.T) {
		repo := NewGormRepository(newSQLiteDB(t))
		seeded := seedProduct(t, repo, "机械键盘", 19900, 2)

		_, err := repo.DeductStock(context.Background(), seeded.ID, 3)

		requireAppCode(t, err, apperrors.CodeFailedPrecondition)
		p, gErr := repo.GetByID(context.Background(), seeded.ID)
		require.NoError(t, gErr)
		assert.Equal(t, int32(2), p.Stock, "扣减失败不能动库存")
	})

	t.Run("商品不存在返回NotFound而不是库存不足", func(t *testing.T) {
		repo := NewGormRepository(newSQLiteDB(t))

		_, err := repo.DeductStock(context.Background(), 999, 1)

		requireAppCode(t, err, apperrors.CodeNotFound)
	})
}

func TestRestoreStock(t *testing.T) {
	t.Run("回补后库存恢复", func(t *testing.T) {
		repo := NewGormRepository(newSQLiteDB(t))
		seeded := seedProduct(t, repo, "机械键盘", 19900, 10)

		_, err := repo.DeductStock(context.Background(), seeded.ID, 3)
		require.NoError(t, err)
		p, err := repo.RestoreStock(context.Background(), seeded.ID, 3)

		require.NoError(t, err)
		assert.Equal(t, int32(10), p.Stock)
	})

	t.Run("商品不存在返回NotFound", func(t *testing.T) {
		repo := NewGormRepository(newSQLiteDB(t))

		_, err := repo.RestoreStock(context.Background(), 999, 1)

		requireAppCode(t, err, apperrors.CodeNotFound)
	})
}

func TestDeductStock_Concurrent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Product{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	repo := NewGormRepository(db)
	seeded := seedProduct(t, repo, "秒杀商品", 9900, 10)

	const goroutines = 50
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := repo.DeductStock(context.Background(), seeded.ID, 1); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(10), successes.Load(), "50 人抢 10 件，恰好 10 人成功")
	p, err := repo.GetByID(context.Background(), seeded.ID)
	require.NoError(t, err)
	assert.Equal(t, int32(0), p.Stock, "库存终值必须是 0，不能为负")
}

func TestUpdate(t *testing.T) {
	t.Run("目标不存在时RowsAffected为0翻译成NotFound", func(t *testing.T) {
		repo := NewGormRepository(newSQLiteDB(t))

		err := repo.Update(context.Background(), &model.Product{ID: 999, Name: "幽灵商品", PriceCents: 100})

		requireAppCode(t, err, apperrors.CodeNotFound)
	})

	t.Run("更新成功且是整体替换语义", func(t *testing.T) {
		repo := NewGormRepository(newSQLiteDB(t))
		seeded := seedProduct(t, repo, "机械键盘", 29900, 10)

		err := repo.Update(context.Background(), &model.Product{
			ID: seeded.ID, Name: "机械键盘 Pro", PriceCents: 39900, Stock: 5,
		})
		require.NoError(t, err)

		updated, err := repo.GetByID(context.Background(), seeded.ID)
		require.NoError(t, err)
		assert.Equal(t, "机械键盘 Pro", updated.Name)
		assert.Equal(t, int64(39900), updated.PriceCents)
		assert.Equal(t, int32(5), updated.Stock)
	})
}

func TestDelete(t *testing.T) {
	t.Run("删除存在的记录", func(t *testing.T) {
		repo := NewGormRepository(newSQLiteDB(t))
		seeded := seedProduct(t, repo, "机械键盘", 29900, 10)

		require.NoError(t, repo.Delete(context.Background(), seeded.ID))

		_, err := repo.GetByID(context.Background(), seeded.ID)
		requireAppCode(t, err, apperrors.CodeNotFound) // 删完真的查不到了
	})

	t.Run("删除不存在的记录返回NotFound而不是成功", func(t *testing.T) {
		repo := NewGormRepository(newSQLiteDB(t))

		err := repo.Delete(context.Background(), 999)

		requireAppCode(t, err, apperrors.CodeNotFound)
	})
}

func TestList(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))
	for i := 1; i <= 25; i++ {
		seedProduct(t, repo, "商品"+string(rune('A'+i-1)), int64(i)*100, 10)
	}

	t.Run("第二页10条总数25", func(t *testing.T) {
		products, total, err := repo.List(context.Background(), 2, 10)
		require.NoError(t, err)
		assert.Equal(t, int64(25), total)
		assert.Len(t, products, 10)
	})

	t.Run("非法分页参数回退默认值", func(t *testing.T) {
		// page=0 / pageSize=0 是外部输入完全可能出现的值，
		// repository 的兜底逻辑必须被测试钉住。
		products, total, err := repo.List(context.Background(), 0, 0)
		require.NoError(t, err)
		assert.Equal(t, int64(25), total)
		assert.Len(t, products, 20) // pageSize 兜底为 20，等价于第一页
	})

	t.Run("超过总页数返回空列表但total仍正确", func(t *testing.T) {
		products, total, err := repo.List(context.Background(), 99, 10)
		require.NoError(t, err)
		assert.Equal(t, int64(25), total)
		assert.Empty(t, products)
	})
}

func TestRestoreStockIdempotent(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))
	ctx := context.Background()
	p := seedProduct(t, repo, "机械键盘", 19900, 10)

	got, err := repo.RestoreStockIdempotent(ctx, "msg-1", p.ID, 3)
	require.NoError(t, err)
	assert.Equal(t, int32(13), got.Stock)

	got, err = repo.RestoreStockIdempotent(ctx, "msg-1", p.ID, 3)
	require.NoError(t, err)
	assert.Equal(t, int32(13), got.Stock, "同一 message_id 重投不能再加库存")

	got, err = repo.RestoreStockIdempotent(ctx, "msg-2", p.ID, 3)
	require.NoError(t, err)
	assert.Equal(t, int32(16), got.Stock, "不同 message_id 是另一笔回补，正常生效")
}

// 商品不存在时返回 NotFound 且整体回滚——去重记录也不能留

func TestRestoreStockIdempotent_NotFoundRollsBack(t *testing.T) {
	db := newSQLiteDB(t)
	repo := NewGormRepository(db)
	ctx := context.Background()

	_, err := repo.RestoreStockIdempotent(ctx, "msg-x", 999, 1)
	requireAppCode(t, err, apperrors.CodeNotFound)

	var count int64
	require.NoError(t, db.Model(&model.StockRestore{}).Count(&count).Error)
	assert.Zero(t, count, "NotFound 必须整体回滚，去重表不留假'已处理'记录")
}
