// user 服务 repository 层单元测试：用 sqlite :memory: 跑真实 GORM 逻辑。

package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apperrors "go-ecom-admin/pkg/errors"

	"go-ecom-admin/internal/user/model"
)

func newSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	return db
}

func requireAppCode(t *testing.T, err error, want apperrors.Code) {
	t.Helper()
	require.Error(t, err)
	var appErr *apperrors.AppError
	require.True(t, errors.As(err, &appErr), "应该返回 *AppError，实际是 %T: %v", err, err)
	assert.Equal(t, want, appErr.Code)
}

func TestCreate_Success(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))

	u := &model.User{Username: "sklmz", Email: "sklmz@example.com", PasswordHash: "hash"}
	require.NoError(t, repo.Create(context.Background(), u))

	assert.NotZero(t, u.ID)
}

func TestGetByID_NotFound(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))

	u, err := repo.GetByID(context.Background(), 999)

	require.Nil(t, u)
	requireAppCode(t, err, apperrors.CodeNotFound)
}

func TestGetByUsername(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))
	seed := &model.User{Username: "sklmz", Email: "sklmz@example.com", PasswordHash: "hash"}
	require.NoError(t, repo.Create(context.Background(), seed))

	t.Run("命中", func(t *testing.T) {
		u, err := repo.GetByUsername(context.Background(), "sklmz")
		require.NoError(t, err)
		require.NotNil(t, u)
		assert.Equal(t, seed.ID, u.ID)
		assert.Equal(t, "sklmz@example.com", u.Email)
	})

	t.Run("未命中翻译成业务NotFound", func(t *testing.T) {
		u, err := repo.GetByUsername(context.Background(), "nobody")
		require.Nil(t, u)
		requireAppCode(t, err, apperrors.CodeNotFound)
	})
}
