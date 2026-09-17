// cart_service 的单元测试，策略与 product_service_test 一致：
// MockRepository（商品存在性检查）+ MockCartRepository 注入假存储。
//
// 考点：
//   - 全部方法都要 metadata 身份（零信任底线，购物车是私密资源）；
//   - AddItem 的超限前置校验分支：已有 98 件再 +2 要拒；
//   - GetItem 返回 NotFound 是"没加购过"的正常分支，不能误伤；
//   - RemoveItems 空列表按 InvalidArgument 拒绝；
//   - 变更类方法统一返回最新全量购物车（cartResponse 契约）。
package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperrors "go-ecom-admin/pkg/errors"

	"go-ecom-admin/internal/product/model"
	"go-ecom-admin/internal/product/repository/mocks"
	productpb "go-ecom-admin/proto/product"
)

func newTestCartService(t *testing.T) (*CartService, *mocks.MockRepository, *mocks.MockCartRepository) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	cartRepo := mocks.NewMockCartRepository(t)
	return NewCartService(repo, cartRepo, zaptest.NewLogger(t)), repo, cartRepo
}

// TestCartService_Validation 覆盖四个方法的参数校验；mock 零期望同时证明
// 非法输入不会触达数据库。
func TestCartService_Validation(t *testing.T) {
	tests := []struct {
		name string
		call func(svc *CartService) error
	}{
		{"AddItem 缺 product_id", func(svc *CartService) error {
			_, err := svc.AddItem(ctxWithUserID(42), &productpb.AddCartItemRequest{Quantity: 1})
			return err
		}},
		{"AddItem 数量为 0", func(svc *CartService) error {
			_, err := svc.AddItem(ctxWithUserID(42), &productpb.AddCartItemRequest{ProductId: 1, Quantity: 0})
			return err
		}},
		{"AddItem 数量超 99", func(svc *CartService) error {
			_, err := svc.AddItem(ctxWithUserID(42), &productpb.AddCartItemRequest{ProductId: 1, Quantity: 100})
			return err
		}},
		{"UpdateItemQuantity 数量为负", func(svc *CartService) error {
			_, err := svc.UpdateItemQuantity(ctxWithUserID(42), &productpb.UpdateCartItemRequest{ProductId: 1, Quantity: -1})
			return err
		}},
		{"RemoveItems 空列表", func(svc *CartService) error {
			_, err := svc.RemoveItems(ctxWithUserID(42), &productpb.RemoveCartItemsRequest{ProductIds: []uint64{}})
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _ := newTestCartService(t)
			err := tt.call(svc)
			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

// TestCartService_Unauthenticated 四个方法都不允许无身份调用——
// 购物车是私密资源，比商品写路径更严格：连 List 都是私密的。
func TestCartService_Unauthenticated(t *testing.T) {
	svc, _, _ := newTestCartService(t)

	_, err1 := svc.AddItem(context.Background(), &productpb.AddCartItemRequest{ProductId: 1, Quantity: 1})
	_, err2 := svc.ListCart(context.Background(), &productpb.ListCartRequest{})
	_, err3 := svc.UpdateItemQuantity(context.Background(), &productpb.UpdateCartItemRequest{ProductId: 1, Quantity: 1})
	_, err4 := svc.RemoveItems(context.Background(), &productpb.RemoveCartItemsRequest{ProductIds: []uint64{1}})

	for _, err := range []error{err1, err2, err3, err4} {
		require.Error(t, err)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	}
}

// TestCartService_AddItem_ProductNotFound 加购不存在的商品是 404 语义
// （链接失效/商品已删），并且不应该写购物车表。
func TestCartService_AddItem_ProductNotFound(t *testing.T) {
	svc, repo, _ := newTestCartService(t)

	repo.EXPECT().GetByID(mock.Anything, uint64(404)).
		Return(nil, apperrors.NotFound("product not found", nil))

	resp, err := svc.AddItem(ctxWithUserID(42), &productpb.AddCartItemRequest{ProductId: 404, Quantity: 1})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// TestCartService_AddItem_ExceedLimit 已有 98 件再 +2：前置校验直接拒，
// AddItem 不该被调到（Run 里断言不到——mockery 默认缺期望即失败，够了）。
func TestCartService_AddItem_ExceedLimit(t *testing.T) {
	svc, repo, cartRepo := newTestCartService(t)

	repo.EXPECT().GetByID(mock.Anything, uint64(1)).
		Return(&model.Product{ID: 1, Name: "机械键盘", Stock: 10}, nil)
	cartRepo.EXPECT().GetItem(mock.Anything, uint64(42), uint64(1)).
		Return(&model.CartItem{UserID: 42, ProductID: 1, Quantity: 98}, nil)

	resp, err := svc.AddItem(ctxWithUserID(42), &productpb.AddCartItemRequest{ProductId: 1, Quantity: 2})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestCartService_AddItem_Success 完整成功路径：商品存在 -> 当前没加购过
// （GetItem NotFound 是正常分支）-> 原子累加 -> 返回最新全量购物车。
func TestCartService_AddItem_Success(t *testing.T) {
	svc, repo, cartRepo := newTestCartService(t)

	repo.EXPECT().GetByID(mock.Anything, uint64(1)).
		Return(&model.Product{ID: 1, Name: "机械键盘", PriceCents: 29900, Stock: 10}, nil)
	// 没加购过：NotFound 必须放行走累加，这条分支是本测试的灵魂断言。
	cartRepo.EXPECT().GetItem(mock.Anything, uint64(42), uint64(1)).
		Return(nil, apperrors.NotFound("cart item not found", nil))
	cartRepo.EXPECT().AddItem(mock.Anything, uint64(42), uint64(1), int32(2)).Return(nil)
	cartRepo.EXPECT().ListByUser(mock.Anything, uint64(42)).
		Return([]*model.CartEntry{
			{CartItem: model.CartItem{UserID: 42, ProductID: 1, Quantity: 2},
				Name: "机械键盘", PriceCents: 29900, Stock: 10},
		}, nil)

	resp, err := svc.AddItem(ctxWithUserID(42), &productpb.AddCartItemRequest{ProductId: 1, Quantity: 2})

	require.NoError(t, err)
	require.Len(t, resp.GetItems(), 1)
	assert.Equal(t, int32(2), resp.GetTotalQuantity())
	assert.Equal(t, "机械键盘", resp.GetItems()[0].GetName())
	assert.Equal(t, int64(29900), resp.GetItems()[0].GetPriceCents())
}

// TestCartService_ListCart_DeletedProduct 孤儿条目（商品已删，LEFT JOIN
// 补零值）：不丢行、不报错，name 空 + stock 0 交给前端渲染"已下架"。
func TestCartService_ListCart_DeletedProduct(t *testing.T) {
	svc, _, cartRepo := newTestCartService(t)

	cartRepo.EXPECT().ListByUser(mock.Anything, uint64(42)).
		Return([]*model.CartEntry{
			{CartItem: model.CartItem{UserID: 42, ProductID: 7, Quantity: 1}}, // 商品已删：展示字段零值
			{CartItem: model.CartItem{UserID: 42, ProductID: 1, Quantity: 3},
				Name: "机械键盘", PriceCents: 29900, Stock: 10},
		}, nil)

	resp, err := svc.ListCart(ctxWithUserID(42), &productpb.ListCartRequest{})

	require.NoError(t, err)
	require.Len(t, resp.GetItems(), 2)
	assert.Equal(t, int32(4), resp.GetTotalQuantity()) // 1 + 3，角标数字按数量合计
	assert.Empty(t, resp.GetItems()[0].GetName())
	assert.Zero(t, resp.GetItems()[0].GetStock())
}

// TestCartService_UpdateItemQuantity_NotFound 改数量打到不存在的条目：
// repo 的 RowsAffected==0 翻译成 NotFound，404 而不是静默成功。
func TestCartService_UpdateItemQuantity_NotFound(t *testing.T) {
	svc, _, cartRepo := newTestCartService(t)

	cartRepo.EXPECT().UpdateQuantity(mock.Anything, uint64(42), uint64(9), int32(5)).
		Return(apperrors.NotFound("cart item not found", nil))

	resp, err := svc.UpdateItemQuantity(ctxWithUserID(42), &productpb.UpdateCartItemRequest{ProductId: 9, Quantity: 5})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// TestCartService_RemoveItems_Success 结算清车：批量删除幂等，响应是删后
// 的最新购物车（被删的 1 号不在，留下的 2 号 quantity 计入角标）。
func TestCartService_RemoveItems_Success(t *testing.T) {
	svc, _, cartRepo := newTestCartService(t)

	cartRepo.EXPECT().RemoveItems(mock.Anything, uint64(42), []uint64{1}).Return(nil)
	cartRepo.EXPECT().ListByUser(mock.Anything, uint64(42)).
		Return([]*model.CartEntry{
			{CartItem: model.CartItem{UserID: 42, ProductID: 2, Quantity: 3},
				Name: "无线鼠标", PriceCents: 9900, Stock: 50},
		}, nil)

	resp, err := svc.RemoveItems(ctxWithUserID(42), &productpb.RemoveCartItemsRequest{ProductIds: []uint64{1}})

	require.NoError(t, err)
	require.Len(t, resp.GetItems(), 1)
	assert.Equal(t, uint64(2), resp.GetItems()[0].GetProductId())
	assert.Equal(t, int32(3), resp.GetTotalQuantity())
}
