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

func (s *CartService) AddItem(ctx context.Context, req *productpb.AddCartItemRequest) (*productpb.CartResponse, error) {
	userID, err := identity.FromIncoming(ctx)
	if err != nil {
		return nil, apperrors.ToGRPCStatus(err)
	}

	if req.GetProductId() == 0 || req.GetQuantity() < 1 || req.GetQuantity() > cartQuantityLimit {
		return nil, apperrors.ToGRPCStatus(apperrors.InvalidArgument(
			"product_id is required and quantity must be between 1 and 99", nil))
	}

	if _, err := s.repo.GetByID(ctx, req.GetProductId()); err != nil {
		return nil, apperrors.ToGRPCStatus(err)
	}

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
