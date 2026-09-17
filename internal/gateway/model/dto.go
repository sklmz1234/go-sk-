// Package model 定义 api-gateway 对外的 REST DTO（JSON 请求/响应结构体）。
//
// 设计决策：故意不直接把 proto 生成的类型（userpb.User 等）当作 JSON 响应体返回。
// REST 客户端（前端/第三方）看到的字段名、字段形态应该由 gateway 自己控制，
// 不应该随下游 gRPC 服务的 proto 变动而被动改变——这是一层反腐层（anti-corruption layer）。
package model

// UserDTO 是暴露给 REST 客户端的用户视图。
type UserDTO struct {
	ID        uint64 `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	CreatedAt int64  `json:"created_at"`
}

// RegisterRequest 对应 POST /api/v1/auth/register。密码只在这个 DTO 里
// 短暂存在（HTTP body -> service 转发给 user-service），gateway 自己
// 不做任何哈希/存储，那是 user-service 的职责。
type RegisterRequest struct {
	Username string `json:"username" binding:"required"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
}

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponseDTO 比 UserDTO 多一个 token 字段——单独定义而不是给
// UserDTO 加可选 token 字段，避免 GetUser 之类的接口也"顺带"带上一个
// 永远是空字符串的 token 字段，语义不清楚。
type LoginResponseDTO struct {
	Token string   `json:"token"`
	User  *UserDTO `json:"user"`
}

// ProductDTO 是暴露给 REST 客户端的商品视图。
// 注意这里把内部的 PriceCents 换算成 PriceYuan 展示——传输/存储用分，
// 展示给最终用户用元，转换逻辑属于 gateway 的职责，不应该让下游服务操心。
type ProductDTO struct {
	ID        uint64  `json:"id"`
	Name      string  `json:"name"`
	PriceYuan float64 `json:"price_yuan"`
	Stock     int32   `json:"stock"`
	// OwnerID 暴露给前端是为了让它判断"这个商品是不是当前登录用户的"，
	// 从而决定要不要渲染编辑/删除按钮。注意这只是展示层的便利——真正的
	// 权限裁决永远在 product-service 内部，前端藏按钮挡不住构造请求的人。
	OwnerID   uint64  `json:"owner_id"`
	// 阶段 5A：C 端商城展示字段。image_url 是外链，空串时前端兜底占位图。
	Description string  `json:"description"`
	ImageURL    string  `json:"image_url"`
	CreatedAt int64   `json:"created_at"`
}

type CreateProductRequest struct {
	Name      string  `json:"name" binding:"required"`
	PriceYuan float64 `json:"price_yuan" binding:"required,gt=0"`
	Stock     int32   `json:"stock"`
	// 图和描述可选（omitempty）：C 端数据入口除了管理台还有 seed，
	// 不强制每个商品都有图。max 与 DB 列宽（varchar 1024/512）对齐，
	// 在网关就拦掉超长输入，比打到 product-service 再失败便宜。
	Description string `json:"description" binding:"omitempty,max=1024"`
	ImageURL    string `json:"image_url" binding:"omitempty,max=512"`
}

// UpdateProductRequest 和 CreateProductRequest 字段一样，但故意不复用
// 同一个结构体：语义不同（一个是"创建"一个是"整体替换"），以后两者的
// 校验规则很可能会分叉（比如 Update 可能要允许部分字段可选），现在分开
// 定义能省掉以后拆分时的破坏性改动。
type UpdateProductRequest struct {
	Name      string  `json:"name" binding:"required"`
	PriceYuan float64 `json:"price_yuan" binding:"required,gt=0"`
	Stock     int32   `json:"stock"`
	// 同 CreateProductRequest：整体替换语义下必须传全，不传会被空串覆盖。
	Description string `json:"description" binding:"omitempty,max=1024"`
	ImageURL    string `json:"image_url" binding:"omitempty,max=512"`
}

type ListProductsRequest struct {
	Page     int `form:"page,default=1"`
	PageSize int `form:"page_size,default=20"`
	// 阶段 5A：搜索关键词，空串=普通分页列表。product-service 内部分流
	// （关键词走 ES 召回，空走 MySQL 列表），网关只透传不感知。
	Keyword string `form:"keyword"`
}

type ListProductsResponse struct {
	Products []*ProductDTO `json:"products"`
	Total    int64         `json:"total"`
}

// —— 订单（阶段 3）——

// OrderItemDTO 是订单明细的 REST 视图。UnitPriceYuan 是下单时刻的价格快照
// （分 → 元的换算同样是 gateway 的职责），商品以后改价不影响这里。
type OrderItemDTO struct {
	ProductID     uint64  `json:"product_id"`
	ProductName   string  `json:"product_name"`
	Quantity      int32   `json:"quantity"`
	UnitPriceYuan float64 `json:"unit_price_yuan"`
}

// OrderDTO 是暴露给 REST 客户端的订单视图。
type OrderDTO struct {
	ID         uint64         `json:"id"`
	UserID     uint64         `json:"user_id"`
	Status     string         `json:"status"` // PENDING / PAID / CANCELLED
	TotalYuan  float64        `json:"total_yuan"`
	Items      []*OrderItemDTO `json:"items,omitempty"`
	CreatedAt  int64          `json:"created_at"`
}

// CreateOrderRequest 对应 POST /api/v1/orders。注意请求里**没有价格字段**——
// 价格只能由 product-service 在扣减时刻给出快照，客户端报价一分都不能信。
type CreateOrderRequest struct {
	Items []*CreateOrderItem `json:"items" binding:"required,min=1,dive"`
}

type CreateOrderItem struct {
	ProductID uint64 `json:"product_id" binding:"required"`
	Quantity  int32  `json:"quantity" binding:"required,gt=0"`
}

type ListOrdersRequest struct {
	Page     int `form:"page,default=1"`
	PageSize int `form:"page_size,default=20"`
}

type ListOrdersResponse struct {
	Orders []*OrderDTO `json:"orders"`
	Total  int64       `json:"total"`
}

// —— 购物车（阶段 5B）——

// CartItemDTO 是购物车条目的 REST 视图。展示信息（名称/价格/图/库存）是
// product-service JOIN 出来的实时值——购物车不存快照，价格快照的正确位置
// 是订单（下单时刻给出），PriceYuan 仅供展示参考。
type CartItemDTO struct {
	ProductID uint64  `json:"product_id"`
	Name      string  `json:"name"`
	ImageURL  string  `json:"image_url"`
	PriceYuan float64 `json:"price_yuan"`
	Stock     int32   `json:"stock"`
	Quantity  int32   `json:"quantity"`
}

// CartResponse 是四个购物车接口统一的响应形态：任何变更（加/改/删）都返回
// 删改后的最新全量列表 + total_quantity（角标数字），前端拿一次响应即可
// 同时刷新列表和角标。
type CartResponse struct {
	Items         []*CartItemDTO `json:"items"`
	TotalQuantity int32          `json:"total_quantity"`
}

// AddCartItemRequest 对应 POST /api/v1/cart/items。quantity 是增量语义
//（重复加购累加），上限 99 与下游 service 的校验对齐——网关先拦一层，
// 非法请求不用打到 product-service。
type AddCartItemRequest struct {
	ProductID uint64 `json:"product_id" binding:"required"`
	Quantity  int32  `json:"quantity" binding:"required,gt=0,lte=99"`
}

// UpdateCartItemRequest 对应 PUT /api/v1/cart/items/:productId，
// quantity 是绝对值语义（购物车页 InputNumber"改成 N"）。
type UpdateCartItemRequest struct {
	Quantity int32 `json:"quantity" binding:"required,gt=0,lte=99"`
}

// RemoveCartItemsRequest 对应 DELETE /api/v1/cart/items（body 传 id 数组）。
// DELETE 带 body 在 HTTP 规范里合法（语义是"删除这些资源"），fetch 也支持；
// 结算清车传勾选项、单行删除传一个元素的数组，同一接口两种用法。
type RemoveCartItemsRequest struct {
	ProductIDs []uint64 `json:"product_ids" binding:"required,min=1"`
}

// AddressDTO（阶段 5B）是 mock 收货信息的 REST 视图，结算/支付页展示
// "收货人 / 电话 / 地址"用。id 一起返回：以后升级真实地址簿时前端靠它
// 记住"这次下单用的是哪条地址"。
type AddressDTO struct {
	ID           uint64 `json:"id"`
	ReceiverName string `json:"receiver_name"`
	Phone        string `json:"phone"`
	Address      string `json:"address"`
}
