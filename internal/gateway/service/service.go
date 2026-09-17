// Package service 是 api-gateway 的编排层：把 REST 请求翻译成对下游 gRPC 服务的调用，
// 并把 gRPC 响应翻译回 REST DTO。以后如果一个 REST 接口需要聚合 user + product
// 两个服务的数据，也是在这一层做，而不是让 handler 直接调多个 repository。
package service

import (
	"context"

	"go-ecom-admin/internal/gateway/model"
	"go-ecom-admin/internal/gateway/repository"
	"go-ecom-admin/pkg/identity"
	orderpb "go-ecom-admin/proto/order"
	productpb "go-ecom-admin/proto/product"
	userpb "go-ecom-admin/proto/user"
)

type Service struct {
	userClient    *repository.UserClient
	productClient *repository.ProductClient
	orderClient   *repository.OrderClient
}

func New(userClient *repository.UserClient, productClient *repository.ProductClient, orderClient *repository.OrderClient) *Service {
	return &Service{userClient: userClient, productClient: productClient, orderClient: orderClient}
}

func (s *Service) GetUser(ctx context.Context, id uint64) (*model.UserDTO, error) {
	u, err := s.userClient.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return userToDTO(u), nil
}

func (s *Service) Register(ctx context.Context, req model.RegisterRequest) (*model.UserDTO, error) {
	u, err := s.userClient.Register(ctx, req.Username, req.Email, req.Password)
	if err != nil {
		return nil, err
	}
	return userToDTO(u), nil
}

func (s *Service) Login(ctx context.Context, req model.LoginRequest) (*model.LoginResponseDTO, error) {
	token, u, err := s.userClient.Login(ctx, req.Username, req.Password)
	if err != nil {
		return nil, err
	}
	return &model.LoginResponseDTO{Token: token, User: userToDTO(u)}, nil
}

// GetRandomAddress（阶段 5B）：随机 mock 收货信息，读公开数据池不需要身份。
func (s *Service) GetRandomAddress(ctx context.Context) (*model.AddressDTO, error) {
	a, err := s.userClient.GetRandomAddress(ctx)
	if err != nil {
		return nil, err
	}
	return &model.AddressDTO{
		ID:           a.GetId(),
		ReceiverName: a.GetReceiverName(),
		Phone:        a.GetPhone(),
		Address:      a.GetAddress(),
	}, nil
}

func (s *Service) GetProduct(ctx context.Context, id uint64) (*model.ProductDTO, error) {
	p, err := s.productClient.GetProduct(ctx, id)
	if err != nil {
		return nil, err
	}
	return productToDTO(p), nil
}

// CreateProduct / UpdateProduct / DeleteProduct 是写路径，必须带调用方身份——
// userID 由 handler 从 gin.Context 取（JWT 中间件已验过签），这里注入 metadata。
// 身份走 metadata 而不是 proto 字段：身份是"横切关注点"，和 JWT 放在 HTTP
// header 而不是塞 body 是同一个道理——每个 proto message 都加一个 user_id
// 字段既重复又容易漏，metadata 由调用链统一注入，业务消息保持干净。
// 读路径（Get/List）是公开的，不需要身份。
func (s *Service) CreateProduct(ctx context.Context, userID uint64, req model.CreateProductRequest) (*model.ProductDTO, error) {
	// 元 -> 分：四舍五入到分，避免浮点数直接乘出现的精度误差被带进下游服务。
	priceCents := int64(req.PriceYuan*100 + 0.5)

	p, err := s.productClient.CreateProduct(identity.InjectOutgoing(ctx, userID), req.Name, priceCents, req.Stock, req.Description, req.ImageURL)
	if err != nil {
		return nil, err
	}
	return productToDTO(p), nil
}

func (s *Service) UpdateProduct(ctx context.Context, userID, id uint64, req model.UpdateProductRequest) (*model.ProductDTO, error) {
	priceCents := int64(req.PriceYuan*100 + 0.5)

	p, err := s.productClient.UpdateProduct(identity.InjectOutgoing(ctx, userID), id, req.Name, priceCents, req.Stock, req.Description, req.ImageURL)
	if err != nil {
		return nil, err
	}
	return productToDTO(p), nil
}

func (s *Service) DeleteProduct(ctx context.Context, userID, id uint64) error {
	return s.productClient.DeleteProduct(identity.InjectOutgoing(ctx, userID), id)
}

func (s *Service) ListProducts(ctx context.Context, req model.ListProductsRequest) (*model.ListProductsResponse, error) {
	products, total, err := s.productClient.ListProducts(ctx, int32(req.Page), int32(req.PageSize), req.Keyword)
	if err != nil {
		return nil, err
	}

	dtos := make([]*model.ProductDTO, 0, len(products))
	for _, p := range products {
		dtos = append(dtos, productToDTO(p))
	}

	return &model.ListProductsResponse{Products: dtos, Total: total}, nil
}

// —— 订单（阶段 3）：三个方法全部要求登录，身份注入走 pkg/identity ——

func (s *Service) CreateOrder(ctx context.Context, userID uint64, req model.CreateOrderRequest) (*model.OrderDTO, error) {
	items := make([]*orderpb.CreateOrderItem, 0, len(req.Items))
	for _, it := range req.Items {
		items = append(items, &orderpb.CreateOrderItem{ProductId: it.ProductID, Quantity: it.Quantity})
	}

	o, err := s.orderClient.CreateOrder(identity.InjectOutgoing(ctx, userID), items)
	if err != nil {
		return nil, err
	}
	return orderToDTO(o), nil
}

func (s *Service) GetOrder(ctx context.Context, userID, id uint64) (*model.OrderDTO, error) {
	o, err := s.orderClient.GetOrder(identity.InjectOutgoing(ctx, userID), id)
	if err != nil {
		return nil, err
	}
	return orderToDTO(o), nil
}

func (s *Service) ListMyOrders(ctx context.Context, userID uint64, req model.ListOrdersRequest) (*model.ListOrdersResponse, error) {
	orders, total, err := s.orderClient.ListMyOrders(identity.InjectOutgoing(ctx, userID), int32(req.Page), int32(req.PageSize))
	if err != nil {
		return nil, err
	}

	dtos := make([]*model.OrderDTO, 0, len(orders))
	for _, o := range orders {
		dtos = append(dtos, orderToDTO(o))
	}
	return &model.ListOrdersResponse{Orders: dtos, Total: total}, nil
}

func (s *Service) CancelOrder(ctx context.Context, userID, id uint64) (*model.OrderDTO, error) {
	o, err := s.orderClient.CancelOrder(identity.InjectOutgoing(ctx, userID), id)
	if err != nil {
		return nil, err
	}
	return orderToDTO(o), nil
}

// PayOrder（阶段 5B 模拟支付）：PENDING → PAID。
func (s *Service) PayOrder(ctx context.Context, userID, id uint64) (*model.OrderDTO, error) {
	o, err := s.orderClient.PayOrder(identity.InjectOutgoing(ctx, userID), id)
	if err != nil {
		return nil, err
	}
	return orderToDTO(o), nil
}

// —— 购物车（阶段 5B）：全部要求登录，身份注入与订单同一模式 ——
// 购物车的方法签名里没有 userID 参数、下游请求里也没有 user_id 字段，
// 身份只走 metadata——和"密码不进 proto"是同一个分层洁癖。

func (s *Service) AddCartItem(ctx context.Context, userID uint64, req model.AddCartItemRequest) (*model.CartResponse, error) {
	resp, err := s.productClient.AddCartItem(identity.InjectOutgoing(ctx, userID), req.ProductID, req.Quantity)
	if err != nil {
		return nil, err
	}
	return cartToDTO(resp), nil
}

func (s *Service) ListCart(ctx context.Context, userID uint64) (*model.CartResponse, error) {
	resp, err := s.productClient.ListCart(identity.InjectOutgoing(ctx, userID))
	if err != nil {
		return nil, err
	}
	return cartToDTO(resp), nil
}

func (s *Service) UpdateCartItem(ctx context.Context, userID, productID uint64, req model.UpdateCartItemRequest) (*model.CartResponse, error) {
	resp, err := s.productClient.UpdateCartItem(identity.InjectOutgoing(ctx, userID), productID, req.Quantity)
	if err != nil {
		return nil, err
	}
	return cartToDTO(resp), nil
}

func (s *Service) RemoveCartItems(ctx context.Context, userID uint64, req model.RemoveCartItemsRequest) (*model.CartResponse, error) {
	resp, err := s.productClient.RemoveCartItems(identity.InjectOutgoing(ctx, userID), req.ProductIDs)
	if err != nil {
		return nil, err
	}
	return cartToDTO(resp), nil
}

func cartToDTO(resp *productpb.CartResponse) *model.CartResponse {
	items := make([]*model.CartItemDTO, 0, len(resp.GetItems()))
	for _, it := range resp.GetItems() {
		items = append(items, &model.CartItemDTO{
			ProductID: it.GetProductId(),
			Name:      it.GetName(),
			ImageURL:  it.GetImageUrl(),
			PriceYuan: float64(it.GetPriceCents()) / 100,
			Stock:     it.GetStock(),
			Quantity:  it.GetQuantity(),
		})
	}
	return &model.CartResponse{Items: items, TotalQuantity: resp.GetTotalQuantity()}
}

func userToDTO(u *userpb.User) *model.UserDTO {
	return &model.UserDTO{
		ID:        u.GetId(),
		Username:  u.GetUsername(),
		Email:     u.GetEmail(),
		CreatedAt: u.GetCreatedAt(),
	}
}

func productToDTO(p *productpb.Product) *model.ProductDTO {
	return &model.ProductDTO{
		ID:          p.GetId(),
		Name:        p.GetName(),
		PriceYuan:   float64(p.GetPriceCents()) / 100,
		Stock:       p.GetStock(),
		OwnerID:     p.GetOwnerId(),
		Description: p.GetDescription(),
		ImageURL:    p.GetImageUrl(),
		CreatedAt:   p.GetCreatedAt(),
	}
}

// orderToDTO 把 proto 枚举状态翻译成 REST 字符串，并完成分 → 元换算。
// 列表接口的 items 可能为空（order-service 的 ListMyOrders 不带明细），
// omitempty 让两种响应共用一个 DTO。
func orderToDTO(o *orderpb.Order) *model.OrderDTO {
	items := make([]*model.OrderItemDTO, 0, len(o.GetItems()))
	for _, it := range o.GetItems() {
		items = append(items, &model.OrderItemDTO{
			ProductID:     it.GetProductId(),
			ProductName:   it.GetProductName(),
			Quantity:      it.GetQuantity(),
			UnitPriceYuan: float64(it.GetUnitPriceCents()) / 100,
		})
	}
	return &model.OrderDTO{
		ID:        o.GetId(),
		UserID:    o.GetUserId(),
		Status:    orderStatusToString(o.GetStatus()),
		TotalYuan: float64(o.GetTotalCents()) / 100,
		Items:     items,
		CreatedAt: o.GetCreatedAt(),
	}
}

func orderStatusToString(s orderpb.OrderStatus) string {
	switch s {
	case orderpb.OrderStatus_ORDER_STATUS_PENDING:
		return "PENDING"
	case orderpb.OrderStatus_ORDER_STATUS_PAID:
		return "PAID"
	case orderpb.OrderStatus_ORDER_STATUS_CANCELLED:
		return "CANCELLED"
	default:
		return "UNSPECIFIED"
	}
}
