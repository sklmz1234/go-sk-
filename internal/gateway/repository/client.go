// Package repository 是 api-gateway 的"数据访问层"——只不过它的数据源不是数据库，
// 而是别的微服务。。
package repository

import (
	"context"
	"time"

	"github.com/sony/gobreaker"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	orderpb "go-ecom-admin/proto/order"
	productpb "go-ecom-admin/proto/product"
	userpb "go-ecom-admin/proto/user"
)

// defaultCallTimeout 给每次下游调用设置超时，避免某个后端服务卡死时
// 把 api-gateway 的请求协程也一起拖死。
const defaultCallTimeout = 3 * time.Second

// UserClient 封装对 user-service 的 gRPC 调用。
type UserClient struct {
	client  userpb.UserServiceClient
	breaker *gobreaker.CircuitBreaker
}

var otelStatsHandler = otelgrpc.NewClientHandler()

// NewUserClient 用 target（形如 "127.0.0.1:9001"）拨号 user-service。
// grpc.NewClient 是非阻塞的——它只做地址解析和惰性连接管理，不会在这里
// 真的发起网络连接，所以即使 user-service 还没启动，api-gateway 也能正常起来
// （请求到来时才会真正建连，失败了也只是那一次调用报错）。
// log 用于熔断器状态迁移的 WARN 日志（breaker.go）。
func NewUserClient(target string, log *zap.Logger) (*UserClient, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelStatsHandler),
	)
	if err != nil {
		return nil, err
	}
	return &UserClient{
		client:  userpb.NewUserServiceClient(conn),
		breaker: newBreaker("user-service", log),
	}, nil
}

func (c *UserClient) GetUser(ctx context.Context, id uint64) (*userpb.User, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	resp, err := callWithBreaker(c.breaker, func() (*userpb.GetUserResponse, error) {
		return c.client.GetUser(ctx, &userpb.GetUserRequest{Id: id})
	})
	if err != nil {
		return nil, err
	}
	return resp.GetUser(), nil
}

func (c *UserClient) Register(ctx context.Context, username, email, password string) (*userpb.User, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	resp, err := callWithBreaker(c.breaker, func() (*userpb.RegisterResponse, error) {
		return c.client.Register(ctx, &userpb.RegisterRequest{Username: username, Email: email, Password: password})
	})
	if err != nil {
		return nil, err
	}
	return resp.GetUser(), nil
}

// loginResult 是 Login 多返回值过泛型 helper 的打包类型。
type loginResult struct {
	token string
	user  *userpb.User
}

// Login 返回 (token, user)，token 的生成在 user-service 完成——gateway
// 只负责透传，不知道也不需要知道签名算法/密钥。
func (c *UserClient) Login(ctx context.Context, username, password string) (string, *userpb.User, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	res, err := callWithBreaker(c.breaker, func() (loginResult, error) {
		resp, err := c.client.Login(ctx, &userpb.LoginRequest{Username: username, Password: password})
		if err != nil {
			return loginResult{}, err
		}
		return loginResult{token: resp.GetToken(), user: resp.GetUser()}, nil
	})
	if err != nil {
		return "", nil, err
	}
	return res.token, res.user, nil
}

// GetRandomAddress（阶段 5B）：随机取一条 mock 收货信息。读公开数据池，
// 不需要调用方身份。
func (c *UserClient) GetRandomAddress(ctx context.Context) (*userpb.Address, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	resp, err := callWithBreaker(c.breaker, func() (*userpb.GetRandomAddressResponse, error) {
		return c.client.GetRandomAddress(ctx, &userpb.GetRandomAddressRequest{})
	})
	if err != nil {
		return nil, err
	}
	return resp.GetAddress(), nil
}

// ProductClient 封装对 product-service 的 gRPC 调用。
//
// 阶段 5B 起 product-service 进程暴露两个 gRPC service（ProductService +
// CartService），本 client 同时持有两个 service 的桩——同一个 conn、同一个
// 熔断器：熔断器保护的是"下游进程的可用性"，同进程的两个 service 共享
// 熔断状态才是真实语义（拆开会让两个 breaker 各自计数，故障时各熔各的，
// 半开状态的探测流量也翻倍）。
type ProductClient struct {
	client  productpb.ProductServiceClient
	cart    productpb.CartServiceClient
	breaker *gobreaker.CircuitBreaker
}

func NewProductClient(target string, log *zap.Logger) (*ProductClient, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelStatsHandler),
	)
	if err != nil {
		return nil, err
	}
	return &ProductClient{
		client:  productpb.NewProductServiceClient(conn),
		cart:    productpb.NewCartServiceClient(conn),
		breaker: newBreaker("product-service", log),
	}, nil
}

func (c *ProductClient) GetProduct(ctx context.Context, id uint64) (*productpb.Product, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	resp, err := callWithBreaker(c.breaker, func() (*productpb.GetProductResponse, error) {
		return c.client.GetProduct(ctx, &productpb.GetProductRequest{Id: id})
	})
	if err != nil {
		return nil, err
	}
	return resp.GetProduct(), nil
}

func (c *ProductClient) CreateProduct(ctx context.Context, name string, priceCents int64, stock int32, description, imageURL string) (*productpb.Product, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	resp, err := callWithBreaker(c.breaker, func() (*productpb.CreateProductResponse, error) {
		return c.client.CreateProduct(ctx, &productpb.CreateProductRequest{
			Name: name, PriceCents: priceCents, Stock: stock,
			Description: description, ImageUrl: imageURL,
		})
	})
	if err != nil {
		return nil, err
	}
	return resp.GetProduct(), nil
}

func (c *ProductClient) UpdateProduct(ctx context.Context, id uint64, name string, priceCents int64, stock int32, description, imageURL string) (*productpb.Product, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	resp, err := callWithBreaker(c.breaker, func() (*productpb.UpdateProductResponse, error) {
		return c.client.UpdateProduct(ctx, &productpb.UpdateProductRequest{
			Id: id, Name: name, PriceCents: priceCents, Stock: stock,
			Description: description, ImageUrl: imageURL,
		})
	})
	if err != nil {
		return nil, err
	}
	return resp.GetProduct(), nil
}

func (c *ProductClient) DeleteProduct(ctx context.Context, id uint64) error {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	_, err := callWithBreaker(c.breaker, func() (*productpb.DeleteProductResponse, error) {
		return c.client.DeleteProduct(ctx, &productpb.DeleteProductRequest{Id: id})
	})
	return err
}

// listProductsResult 是 ListProducts 多返回值过泛型 helper 的打包类型。
type listProductsResult struct {
	products []*productpb.Product
	total    int64
}

func (c *ProductClient) ListProducts(ctx context.Context, page, pageSize int32, keyword string) ([]*productpb.Product, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	res, err := callWithBreaker(c.breaker, func() (listProductsResult, error) {
		resp, err := c.client.ListProducts(ctx, &productpb.ListProductsRequest{Page: page, PageSize: pageSize, Keyword: keyword})
		if err != nil {
			return listProductsResult{}, err
		}
		return listProductsResult{products: resp.GetProducts(), total: resp.GetTotal()}, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return res.products, res.total, nil
}

// OrderClient 封装对 order-service 的 gRPC 调用（阶段 3）。
type OrderClient struct {
	client  orderpb.OrderServiceClient
	breaker *gobreaker.CircuitBreaker
}

func NewOrderClient(target string, log *zap.Logger) (*OrderClient, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelStatsHandler),
	)
	if err != nil {
		return nil, err
	}
	return &OrderClient{
		client:  orderpb.NewOrderServiceClient(conn),
		breaker: newBreaker("order-service", log),
	}, nil
}

// CreateOrder 的 ctx 必须已由 service 层注入调用方身份（pkg/identity）——
// order-service 的零信任校验要求所有方法都带 user_id。
func (c *OrderClient) CreateOrder(ctx context.Context, items []*orderpb.CreateOrderItem) (*orderpb.Order, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	resp, err := callWithBreaker(c.breaker, func() (*orderpb.CreateOrderResponse, error) {
		return c.client.CreateOrder(ctx, &orderpb.CreateOrderRequest{Items: items})
	})
	if err != nil {
		return nil, err
	}
	return resp.GetOrder(), nil
}

func (c *OrderClient) GetOrder(ctx context.Context, id uint64) (*orderpb.Order, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	resp, err := callWithBreaker(c.breaker, func() (*orderpb.GetOrderResponse, error) {
		return c.client.GetOrder(ctx, &orderpb.GetOrderRequest{Id: id})
	})
	if err != nil {
		return nil, err
	}
	return resp.GetOrder(), nil
}

// listOrdersResult 是 ListMyOrders 多返回值过泛型 helper 的打包类型。
type listOrdersResult struct {
	orders []*orderpb.Order
	total  int64
}

func (c *OrderClient) ListMyOrders(ctx context.Context, page, pageSize int32) ([]*orderpb.Order, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	res, err := callWithBreaker(c.breaker, func() (listOrdersResult, error) {
		resp, err := c.client.ListMyOrders(ctx, &orderpb.ListMyOrdersRequest{Page: page, PageSize: pageSize})
		if err != nil {
			return listOrdersResult{}, err
		}
		return listOrdersResult{orders: resp.GetOrders(), total: resp.GetTotal()}, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return res.orders, res.total, nil
}

func (c *OrderClient) CancelOrder(ctx context.Context, id uint64) (*orderpb.Order, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	resp, err := callWithBreaker(c.breaker, func() (*orderpb.CancelOrderResponse, error) {
		return c.client.CancelOrder(ctx, &orderpb.CancelOrderRequest{Id: id})
	})
	if err != nil {
		return nil, err
	}
	return resp.GetOrder(), nil
}

// PayOrder（阶段 5B 模拟支付）：PENDING → PAID，ctx 需已注入调用方身份。
func (c *OrderClient) PayOrder(ctx context.Context, id uint64) (*orderpb.Order, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	resp, err := callWithBreaker(c.breaker, func() (*orderpb.PayOrderResponse, error) {
		return c.client.PayOrder(ctx, &orderpb.PayOrderRequest{Id: id})
	})
	if err != nil {
		return nil, err
	}
	return resp.GetOrder(), nil
}

// —— 购物车（阶段 5B）：四个方法的 ctx 都必须已由 service 层注入调用方
// 身份（pkg/identity）——购物车是私密资源，下游全部方法都验 user_id。 ——

func (c *ProductClient) AddCartItem(ctx context.Context, productID uint64, quantity int32) (*productpb.CartResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	return callWithBreaker(c.breaker, func() (*productpb.CartResponse, error) {
		return c.cart.AddItem(ctx, &productpb.AddCartItemRequest{ProductId: productID, Quantity: quantity})
	})
}

func (c *ProductClient) ListCart(ctx context.Context) (*productpb.CartResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	return callWithBreaker(c.breaker, func() (*productpb.CartResponse, error) {
		return c.cart.ListCart(ctx, &productpb.ListCartRequest{})
	})
}

func (c *ProductClient) UpdateCartItem(ctx context.Context, productID uint64, quantity int32) (*productpb.CartResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	return callWithBreaker(c.breaker, func() (*productpb.CartResponse, error) {
		return c.cart.UpdateItemQuantity(ctx, &productpb.UpdateCartItemRequest{ProductId: productID, Quantity: quantity})
	})
}

func (c *ProductClient) RemoveCartItems(ctx context.Context, productIDs []uint64) (*productpb.CartResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	return callWithBreaker(c.breaker, func() (*productpb.CartResponse, error) {
		return c.cart.RemoveItems(ctx, &productpb.RemoveCartItemsRequest{ProductIds: productIDs})
	})
}
