// order 服务的 service 层单元测试
package service

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apperrors "go-ecom-admin/pkg/errors"
	"go-ecom-admin/pkg/identity"

	"go-ecom-admin/internal/order/model"
	"go-ecom-admin/internal/order/repository/mocks"
	orderpb "go-ecom-admin/proto/order"
	productpb "go-ecom-admin/proto/product"
)

func newTestService(t *testing.T) (*Service, *mocks.MockRepository, *mocks.MockOutboxRepository, *mocks.MockProductClient) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	outbox := mocks.NewMockOutboxRepository(t)
	products := mocks.NewMockProductClient(t)
	return New(repo, outbox, products, zaptest.NewLogger(t)), repo, outbox, products
}

// ctxWithUserID 模拟网关验完 JWT 注入的 incoming metadata（与 product 测试同款）。
func ctxWithUserID(uid uint64) context.Context {
	md := metadata.Pairs(identity.MetadataKeyUserID, strconv.FormatUint(uid, 10))
	return metadata.NewIncomingContext(context.Background(), md)
}

// outgoingUserID 从 service 传给 product client 的 ctx 里取出 outgoing
// metadata 的 user_id，验证身份接力真的发生了。
func outgoingUserID(t *testing.T, ctx context.Context) string {
	t.Helper()
	md, ok := metadata.FromOutgoingContext(ctx)
	require.True(t, ok, "调 product-service 的 ctx 必须带 outgoing metadata")
	vals := md.Get(identity.MetadataKeyUserID)
	require.NotEmpty(t, vals, "outgoing metadata 必须带 user_id（身份接力）")
	return vals[0]
}

func TestCreateOrder_Success_MultiItems(t *testing.T) {
	svc, repo, _, products := newTestService(t)

	// 两个商品各扣一次；mock 里校验 ctx 带了接力身份。
	products.EXPECT().DeductStock(mock.Anything, uint64(1), int32(2)).
		Run(func(ctx context.Context, productID uint64, quantity int32) {
			assert.Equal(t, "42", outgoingUserID(t, ctx))
		}).
		Return(&productpb.DeductStockResponse{RemainingStock: 8, PriceCents: 19900, Name: "机械键盘"}, nil)
	products.EXPECT().DeductStock(mock.Anything, uint64(2), int32(1)).
		Return(&productpb.DeductStockResponse{RemainingStock: 4, PriceCents: 199900, Name: "显示器"}, nil)

	repo.EXPECT().Create(mock.Anything, mock.AnythingOfType("*model.Order")).
		Run(func(ctx context.Context, o *model.Order) {
			assert.Equal(t, uint64(42), o.UserID)
			assert.Equal(t, model.StatusPending, o.Status)
			require.Len(t, o.Items, 2)
			// 快照：价格来自扣减响应，不是请求（请求里根本没有价格字段）。
			assert.Equal(t, int64(19900), o.Items[0].UnitPriceCents)
			assert.Equal(t, "机械键盘", o.Items[0].ProductName)
			assert.Equal(t, int64(199900), o.Items[1].UnitPriceCents)
			// 总额 = 19900×2 + 199900×1 = 239700
			assert.Equal(t, int64(239700), o.TotalCents)
			o.ID = 1001
			o.CreatedAt = time.Now()
		}).
		Return(nil)

	resp, err := svc.CreateOrder(ctxWithUserID(42), &orderpb.CreateOrderRequest{Items: []*orderpb.CreateOrderItem{
		{ProductId: 1, Quantity: 2},
		{ProductId: 2, Quantity: 1},
	}})

	require.NoError(t, err)
	assert.Equal(t, uint64(1001), resp.GetOrder().GetId())
	assert.Equal(t, orderpb.OrderStatus_ORDER_STATUS_PENDING, resp.GetOrder().GetStatus())
	assert.Equal(t, int64(239700), resp.GetOrder().GetTotalCents())
}

// 决策 2 的核心路径：第 2 项扣减失败（库存不足）→ 回补第 1 项，订单不落库，
// 原始 FailedPrecondition 透传给调用方。
func TestCreateOrder_SecondItemFails_CompensatesFirst(t *testing.T) {
	svc, repo, _, products := newTestService(t)

	products.EXPECT().DeductStock(mock.Anything, uint64(1), int32(2)).
		Return(&productpb.DeductStockResponse{RemainingStock: 8, PriceCents: 19900, Name: "机械键盘"}, nil)
	products.EXPECT().DeductStock(mock.Anything, uint64(2), int32(1)).
		Return(nil, apperrors.ToGRPCStatus(apperrors.FailedPrecondition("insufficient stock", nil)))
	// 补偿：只回补已扣的第 1 项。repo.Create 零期望——订单绝不能落库。
	products.EXPECT().RestoreStock(mock.Anything, uint64(1), int32(2), "").Return(nil)

	_, err := svc.CreateOrder(ctxWithUserID(42), &orderpb.CreateOrderRequest{Items: []*orderpb.CreateOrderItem{
		{ProductId: 1, Quantity: 2},
		{ProductId: 2, Quantity: 1},
	}})

	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

// 落库失败 → 回补全部已扣项（两项都补）。
func TestCreateOrder_PersistFails_CompensatesAll(t *testing.T) {
	svc, repo, _, products := newTestService(t)

	products.EXPECT().DeductStock(mock.Anything, uint64(1), int32(1)).
		Return(&productpb.DeductStockResponse{RemainingStock: 9, PriceCents: 100, Name: "A"}, nil)
	products.EXPECT().DeductStock(mock.Anything, uint64(2), int32(3)).
		Return(&productpb.DeductStockResponse{RemainingStock: 7, PriceCents: 200, Name: "B"}, nil)
	repo.EXPECT().Create(mock.Anything, mock.AnythingOfType("*model.Order")).
		Return(apperrors.Internal("db gone", nil))
	products.EXPECT().RestoreStock(mock.Anything, uint64(1), int32(1), "").Return(nil)
	products.EXPECT().RestoreStock(mock.Anything, uint64(2), int32(3), "").Return(nil)

	_, err := svc.CreateOrder(ctxWithUserID(42), &orderpb.CreateOrderRequest{Items: []*orderpb.CreateOrderItem{
		{ProductId: 1, Quantity: 1},
		{ProductId: 2, Quantity: 3},
	}})

	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))
}

// 补偿本身失败不遮蔽原始错误：RestoreStock 报错时，调用方看到的仍是
// 库存不足的 FailedPrecondition（补偿失败只留 ERROR 日志供对账）。
func TestCreateOrder_CompensationFailure_OriginalErrorSurfaces(t *testing.T) {
	svc, _, _, products := newTestService(t)

	products.EXPECT().DeductStock(mock.Anything, uint64(1), int32(1)).
		Return(&productpb.DeductStockResponse{RemainingStock: 9, PriceCents: 100, Name: "A"}, nil)
	products.EXPECT().DeductStock(mock.Anything, uint64(2), int32(1)).
		Return(nil, apperrors.ToGRPCStatus(apperrors.FailedPrecondition("insufficient stock", nil)))
	products.EXPECT().RestoreStock(mock.Anything, uint64(1), int32(1), "").
		Return(apperrors.ToGRPCStatus(apperrors.Internal("product-service down", nil)))

	_, err := svc.CreateOrder(ctxWithUserID(42), &orderpb.CreateOrderRequest{Items: []*orderpb.CreateOrderItem{
		{ProductId: 1, Quantity: 1},
		{ProductId: 2, Quantity: 1},
	}})

	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "补偿失败不能遮蔽原始的扣减失败")
}

func TestCreateOrder_Unauthenticated(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	_, err := svc.CreateOrder(context.Background(), &orderpb.CreateOrderRequest{Items: []*orderpb.CreateOrderItem{
		{ProductId: 1, Quantity: 1},
	}})

	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestCreateOrder_Validation(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	// 空 items
	_, err := svc.CreateOrder(ctxWithUserID(42), &orderpb.CreateOrderRequest{})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	// 非法数量
	_, err = svc.CreateOrder(ctxWithUserID(42), &orderpb.CreateOrderRequest{Items: []*orderpb.CreateOrderItem{
		{ProductId: 1, Quantity: 0},
	}})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// 决策 4：查到不属于自己的订单返回 404 而不是 403——不暴露订单号的存在性。
func TestGetOrder_OthersOrder_Returns404(t *testing.T) {
	svc, repo, _, _ := newTestService(t)
	repo.EXPECT().GetByID(mock.Anything, uint64(1001)).
		Return(&model.Order{ID: 1001, UserID: 42, Status: model.StatusPending}, nil)

	_, err := svc.GetOrder(ctxWithUserID(7), &orderpb.GetOrderRequest{Id: 1001})

	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err), "越权查订单必须是 404，不能泄露存在性")
}

func TestGetOrder_Success(t *testing.T) {
	svc, repo, _, _ := newTestService(t)
	repo.EXPECT().GetByID(mock.Anything, uint64(1001)).Return(&model.Order{
		ID: 1001, UserID: 42, Status: model.StatusPending, TotalCents: 39800,
		Items:     []model.OrderItem{{ProductID: 1, ProductName: "机械键盘", Quantity: 2, UnitPriceCents: 19900}},
		CreatedAt: time.Now(),
	}, nil)

	resp, err := svc.GetOrder(ctxWithUserID(42), &orderpb.GetOrderRequest{Id: 1001})

	require.NoError(t, err)
	assert.Equal(t, int64(39800), resp.GetOrder().GetTotalCents())
	require.Len(t, resp.GetOrder().GetItems(), 1)
	assert.Equal(t, "机械键盘", resp.GetOrder().GetItems()[0].GetProductName())
}

// ListMyOrders 的 user_id 过滤从 repo 就带上：mock 断言 repo 收到的就是
// 调用者自己的 id——不存在"查全部再过滤"的越权窗口。
func TestListMyOrders_ScopedByCaller(t *testing.T) {
	svc, repo, _, _ := newTestService(t)
	repo.EXPECT().ListByUser(mock.Anything, uint64(42), 1, 10).
		Return([]*model.Order{{ID: 1001, UserID: 42, Status: model.StatusPending, CreatedAt: time.Now()}}, int64(1), nil)

	resp, err := svc.ListMyOrders(ctxWithUserID(42), &orderpb.ListMyOrdersRequest{Page: 1, PageSize: 10})

	require.NoError(t, err)
	assert.Equal(t, int64(1), resp.GetTotal())
	require.Len(t, resp.GetOrders(), 1)
}

// —— CancelOrder（阶段 4：本地消息表全异步回补）——

// 成功取消：一个事务完成状态迁移 + 每个订单项一条 outbox 消息。
// 不再同步调 RestoreStock——回补交给 relay，这里验证的是消息组装正确。
func TestCancelOrder_Success_QueuesOutboxMessages(t *testing.T) {
	svc, repo, outbox, _ := newTestService(t)
	repo.EXPECT().GetByID(mock.Anything, uint64(1001)).Return(&model.Order{
		ID: 1001, UserID: 42, Status: model.StatusPending, TotalCents: 239700,
		Items: []model.OrderItem{
			{ProductID: 1, ProductName: "机械键盘", Quantity: 2, UnitPriceCents: 19900},
			{ProductID: 2, ProductName: "显示器", Quantity: 1, UnitPriceCents: 199900},
		},
	}, nil)

	outbox.EXPECT().CancelWithOutbox(mock.Anything, uint64(1001), model.StatusPending, model.StatusCancelled, mock.Anything).
		Run(func(ctx context.Context, orderID uint64, from, to string, messages []*model.OutboxMessage) {
			require.Len(t, messages, 2, "每个订单项一条消息")

			seen := map[string]bool{}
			for _, m := range messages {
				assert.Equal(t, model.OutboxTypeStockRestore, m.Type)
				assert.Equal(t, model.OutboxStatusPending, m.Status)
				assert.NotEmpty(t, m.MessageID, "幂等键不能为空")
				assert.False(t, seen[m.MessageID], "message_id 必须唯一")
				seen[m.MessageID] = true
			}

			// 消息体逐项对应订单明细。
			var p1 model.StockRestorePayload
			require.NoError(t, json.Unmarshal([]byte(messages[0].Payload), &p1))
			assert.Equal(t, model.StockRestorePayload{ProductID: 1, Quantity: 2, OrderID: 1001}, p1)
			var p2 model.StockRestorePayload
			require.NoError(t, json.Unmarshal([]byte(messages[1].Payload), &p2))
			assert.Equal(t, model.StockRestorePayload{ProductID: 2, Quantity: 1, OrderID: 1001}, p2)
		}).Return(nil)

	resp, err := svc.CancelOrder(ctxWithUserID(42), &orderpb.CancelOrderRequest{Id: 1001})

	require.NoError(t, err)
	assert.Equal(t, orderpb.OrderStatus_ORDER_STATUS_CANCELLED, resp.GetOrder().GetStatus())
	// 全异步后 service 不再直接调 product——products mock 零期望即是证明。
}

// CancelWithOutbox 返回 FailedPrecondition（并发取消，条件更新未命中）：
// 原样透传 409，消息批次整体作废。
func TestCancelOrder_ConcurrentCancel_409(t *testing.T) {
	svc, repo, outbox, _ := newTestService(t)
	repo.EXPECT().GetByID(mock.Anything, uint64(1001)).Return(&model.Order{
		ID: 1001, UserID: 42, Status: model.StatusPending,
		Items: []model.OrderItem{{ProductID: 1, Quantity: 1}},
	}, nil)
	outbox.EXPECT().CancelWithOutbox(mock.Anything, uint64(1001), model.StatusPending, model.StatusCancelled, mock.Anything).
		Return(apperrors.FailedPrecondition("order status changed concurrently", nil))

	_, err := svc.CancelOrder(ctxWithUserID(42), &orderpb.CancelOrderRequest{Id: 1001})

	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// 已取消的订单再取消：FailedPrecondition（409），且绝不能重复回补库存。
func TestCancelOrder_AlreadyCancelled_409(t *testing.T) {
	svc, repo, _, _ := newTestService(t)
	repo.EXPECT().GetByID(mock.Anything, uint64(1001)).
		Return(&model.Order{ID: 1001, UserID: 42, Status: model.StatusCancelled}, nil)

	_, err := svc.CancelOrder(ctxWithUserID(42), &orderpb.CancelOrderRequest{Id: 1001})

	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// 越权取消别人的订单：404，不暴露存在性（与 GetOrder 同一语义）。
func TestCancelOrder_OthersOrder_Returns404(t *testing.T) {
	svc, repo, _, _ := newTestService(t)
	repo.EXPECT().GetByID(mock.Anything, uint64(1001)).
		Return(&model.Order{ID: 1001, UserID: 42, Status: model.StatusPending}, nil)

	_, err := svc.CancelOrder(ctxWithUserID(7), &orderpb.CancelOrderRequest{Id: 1001})

	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestCancelOrder_Unauthenticated(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	_, err := svc.CancelOrder(context.Background(), &orderpb.CancelOrderRequest{Id: 1001})

	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}
