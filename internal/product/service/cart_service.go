// 阶段 5B：购物车的业务逻辑，实现 gRPC 生成的 CartServiceServer 接口。
// 与 ProductService 同进程注册（cmd/product-service/main.go），但独立成
// struct——两者共享商品 Repository（存在性检查走缓存装饰器），各自的
// 职责边界互不污染。
package service

import (
	"context"
	"errors"

	"go.uber.org/zap"

	apperrors "go-ecom-admin/pkg/errors"
	"go-ecom-admin/pkg/identity"

	"go-ecom-admin/internal/product/model"
	"go-ecom-admin/internal/product/repository"
	productpb "go-ecom-admin/proto/product"
)

// cartQuantityLimit 是单条购物车记录的数量上限（与 DB 注释、网关 binding
// 校验三处对齐）。上限存在的意义：把"加购数量"约束在人类正常购物行为
// 范围内，也顺手保证累加不会把 int32 推向溢出语义。
const cartQuantityLimit = 99

type CartService struct {
	productpb.UnimplementedCartServiceServer

	// repo 是商品 Repository（可能被缓存/搜索装饰器包裹）——加购前的
	// "商品是否存在"检查走它，热点商品命中缓存不碰 MySQL。
	repo repository.Repository
	// cartRepo 是购物车专用 Repository，无装饰器（购物车不需要缓存/搜索）。
	cartRepo repository.CartRepository
	log      *zap.Logger
}

func NewCartService(repo repository.Repository, cartRepo repository.CartRepository, log *zap.Logger) *CartService {
	return &CartService{repo: repo, cartRepo: cartRepo, log: log}
}

// AddItem：验身份 -> 验参数 -> 验商品存在 -> 验累加不超限 -> 原子累加。
// 不校验库存：库存的最终裁决权在下单（DeductStock 条件更新 + 409），
// 加购时校验一遍只会制造"双重校验语义打架"——加购通过不代表结算时有货，
// 车里的 stock 字段只是展示参考。
func (s *CartService) AddItem(ctx context.Context, req *productpb.AddCartItemRequest) (*productpb.CartResponse, error) {
	userID, err := identity.FromIncoming(ctx)
	if err != nil {
		return nil, apperrors.ToGRPCStatus(err)
	}

	if req.GetProductId() == 0 || req.GetQuantity() < 1 || req.GetQuantity() > cartQuantityLimit {
		return nil, apperrors.ToGRPCStatus(apperrors.InvalidArgument(
			"product_id is required and quantity must be between 1 and 99", nil))
	}

	// 商品存在性：不存在的商品加购是 NotFound（加错链接）而不是 Internal。
	// 这里复用商品读路径（带缓存），顺带拿到归属模型的同一套语义。
	if _, err := s.repo.GetByID(ctx, req.GetProductId()); err != nil {
		return nil, apperrors.ToGRPCStatus(err)
	}

	// 累加超限的前置校验：先读当前行，current + n > 99 直接拒。
	// 与 repo.AddItem 的原子累加之间存在竞态窗口（并发加购可能瞬时冲破
	// 99）——不为此加行锁：超限一两件是展示层无害误差，幂等重试/手动
	// 改数量都能纠正，锁的复杂度不值得（取舍写在方案文档的风险清单里）。
	if current, err := s.cartRepo.GetItem(ctx, userID, req.GetProductId()); err == nil {
		if current.Quantity+req.GetQuantity() > cartQuantityLimit {
			return nil, apperrors.ToGRPCStatus(apperrors.InvalidArgument(
				"total quantity in cart would exceed 99", nil))
		}
	} else {
		// NotFound 是"还没有这行"的正常分支（放行走累加）；
		// 其他错误（DB 故障）必须中断而不是被当成"没加购过"。
		var appErr *apperrors.AppError
		if !errors.As(err, &appErr) || appErr.Code != apperrors.CodeNotFound {
			return nil, apperrors.ToGRPCStatus(err)
		}
	}

	if err := s.cartRepo.AddItem(ctx, userID, req.GetProductId(), req.GetQuantity()); err != nil {
		s.log.Warn("add cart item failed",
			zap.Uint64("user_id", userID), zap.Uint64("product_id", req.GetProductId()), zap.Error(err))
		return nil, apperrors.ToGRPCStatus(err)
	}

	return s.cartResponse(ctx, userID)
}

func (s *CartService) ListCart(ctx context.Context, _ *productpb.ListCartRequest) (*productpb.CartResponse, error) {
	userID, err := identity.FromIncoming(ctx)
	if err != nil {
		return nil, apperrors.ToGRPCStatus(err)
	}
	return s.cartResponse(ctx, userID)
}

// UpdateItemQuantity 绝对值语义（购物车页 InputNumber 的"改成 N"）。
// 条目不存在由 repo 的 RowsAffected 判定翻译成 NotFound。
func (s *CartService) UpdateItemQuantity(ctx context.Context, req *productpb.UpdateCartItemRequest) (*productpb.CartResponse, error) {
	userID, err := identity.FromIncoming(ctx)
	if err != nil {
		return nil, apperrors.ToGRPCStatus(err)
	}

	if req.GetProductId() == 0 || req.GetQuantity() < 1 || req.GetQuantity() > cartQuantityLimit {
		return nil, apperrors.ToGRPCStatus(apperrors.InvalidArgument(
			"product_id is required and quantity must be between 1 and 99", nil))
	}

	if err := s.cartRepo.UpdateQuantity(ctx, userID, req.GetProductId(), req.GetQuantity()); err != nil {
		s.log.Warn("update cart item failed",
			zap.Uint64("user_id", userID), zap.Uint64("product_id", req.GetProductId()), zap.Error(err))
		return nil, apperrors.ToGRPCStatus(err)
	}

	return s.cartResponse(ctx, userID)
}

// RemoveItems 结算清车/单行删除共用：批量、幂等。product_ids 为空是
// 调用方组装错误（不是"清空购物车"），按 InvalidArgument 拒绝——
// 空数组如果放行，一次手滑的空请求会制造"什么都没删却返回成功"的
// 无效往返，不如在契约上就掐死。
func (s *CartService) RemoveItems(ctx context.Context, req *productpb.RemoveCartItemsRequest) (*productpb.CartResponse, error) {
	userID, err := identity.FromIncoming(ctx)
	if err != nil {
		return nil, apperrors.ToGRPCStatus(err)
	}

	if len(req.GetProductIds()) == 0 {
		return nil, apperrors.ToGRPCStatus(apperrors.InvalidArgument("product_ids must not be empty", nil))
	}

	if err := s.cartRepo.RemoveItems(ctx, userID, req.GetProductIds()); err != nil {
		s.log.Warn("remove cart items failed",
			zap.Uint64("user_id", userID), zap.Uint64s("product_ids", req.GetProductIds()), zap.Error(err))
		return nil, apperrors.ToGRPCStatus(err)
	}

	return s.cartResponse(ctx, userID)
}

// cartResponse 是四个方法的统一出口：任何变更后都返回最新全量购物车。
// 前端拿一次响应就能刷新列表和角标，不需要变更后再发一次 GET——
// 变更+读之间的一致性由同进程内的顺序调用保证（学习项目单副本，
// 没有跨副本的读己之写问题）。
func (s *CartService) cartResponse(ctx context.Context, userID uint64) (*productpb.CartResponse, error) {
	entries, err := s.cartRepo.ListByUser(ctx, userID)
	if err != nil {
		return nil, apperrors.ToGRPCStatus(err)
	}

	items := make([]*productpb.CartItem, 0, len(entries))
	var total int32
	for _, e := range entries {
		items = append(items, cartEntryToProto(e))
		total += e.Quantity
	}
	return &productpb.CartResponse{Items: items, TotalQuantity: total}, nil
}

func cartEntryToProto(e *model.CartEntry) *productpb.CartItem {
	return &productpb.CartItem{
		ProductId:  e.ProductID,
		Quantity:   e.Quantity,
		Name:       e.Name,
		PriceCents: e.PriceCents,
		ImageUrl:   e.ImageURL,
		Stock:      e.Stock,
	}
}
