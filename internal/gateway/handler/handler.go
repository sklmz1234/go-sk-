// Package handler 是 Gin 的 HTTP 处理函数层：只做参数绑定、调用 service、
// 把结果/错误翻译成 HTTP 响应，不包含任何业务逻辑。
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	apperrors "go-ecom-admin/pkg/errors"

	"go-ecom-admin/internal/gateway/middleware"
	"go-ecom-admin/internal/gateway/model"
	"go-ecom-admin/internal/gateway/service"
)

type Handler struct {
	svc *service.Service
	log *zap.Logger
}

func New(svc *service.Service, log *zap.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) GetUser(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	user, err := h.svc.GetUser(c.Request.Context(), id)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, user)
}

func (h *Handler) Register(c *gin.Context) {
	var req model.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user, err := h.svc.Register(c.Request.Context(), req)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusCreated, user)
}

func (h *Handler) Login(c *gin.Context) {
	var req model.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, err := h.svc.Login(c.Request.Context(), req)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GetRandomAddress 对应 GET /api/v1/addresses/random（阶段 5B）：结算/支付
// 页展示用的随机 mock 收货信息。挂在 auth 组下——接口本身读的是公开池，
// 但它只服务于"已登录的结算流程"，没有必要对游客开放。
func (h *Handler) GetRandomAddress(c *gin.Context) {
	addr, err := h.svc.GetRandomAddress(c.Request.Context())
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, addr)
}

func (h *Handler) GetProduct(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	product, err := h.svc.GetProduct(c.Request.Context(), id)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, product)
}

func (h *Handler) CreateProduct(c *gin.Context) {
	var req model.CreateProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		// 路由上挂了 Auth 中间件时这里永远走不到——这是一次防御性检查：
		// 万一以后有人把写路由挂到 auth 组外面，至少不会带着"零身份"调到下游。
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	product, err := h.svc.CreateProduct(c.Request.Context(), userID, req)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusCreated, product)
}

func (h *Handler) ListProducts(c *gin.Context) {
	var req model.ListProductsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, err := h.svc.ListProducts(c.Request.Context(), req)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) UpdateProduct(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var req model.UpdateProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	product, err := h.svc.UpdateProduct(c.Request.Context(), userID, id, req)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, product)
}

func (h *Handler) DeleteProduct(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	if err := h.svc.DeleteProduct(c.Request.Context(), userID, id); err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// —— 订单（阶段 3）：全部要求登录，userID 取自 JWT 中间件 ——

func (h *Handler) CreateOrder(c *gin.Context) {
	var req model.CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	order, err := h.svc.CreateOrder(c.Request.Context(), userID, req)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusCreated, order)
}

func (h *Handler) GetOrder(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	order, err := h.svc.GetOrder(c.Request.Context(), userID, id)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, order)
}

func (h *Handler) ListMyOrders(c *gin.Context) {
	var req model.ListOrdersRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	resp, err := h.svc.ListMyOrders(c.Request.Context(), userID, req)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// CancelOrder 对应 POST /api/v1/orders/:id/cancel。动作用子资源路径而不是
// DELETE /orders/:id：取消不是删除——订单记录必须保留（对账、历史），
// 只是状态机走 PENDING → CANCELLED。
func (h *Handler) CancelOrder(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	order, err := h.svc.CancelOrder(c.Request.Context(), userID, id)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, order)
}

// PayOrder 对应 POST /api/v1/orders/:id/pay（阶段 5B 模拟支付）。
// 与 cancel 同构的子资源动作路径：pay 也不是 CRUD，是状态机迁移。
func (h *Handler) PayOrder(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	order, err := h.svc.PayOrder(c.Request.Context(), userID, id)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, order)
}

// —— 购物车（阶段 5B）：全部要求登录，四个方法共用同一套
// "取身份 -> 调 service -> 翻译错误"的骨架，与订单 handler 一致 ——

// AddCartItem 对应 POST /api/v1/cart/items，body：{product_id, quantity}。
// 成功返回 200（带最新购物车全量）——不用 201：购物车行不是独立寻址的
// 资源（没有 GET /cart/items/:id），REST 语义上"加购"是对 /cart 集合状态
// 的变更而非创建子资源。
func (h *Handler) AddCartItem(c *gin.Context) {
	var req model.AddCartItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	resp, err := h.svc.AddCartItem(c.Request.Context(), userID, req)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) ListCart(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	resp, err := h.svc.ListCart(c.Request.Context(), userID)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// UpdateCartItem 对应 PUT /api/v1/cart/items/:productId，body：{quantity}。
// :productId 放路径而不是 body——它是被操作资源的标识，和 PUT /products/:id
// 是同一个建模习惯。
func (h *Handler) UpdateCartItem(c *gin.Context) {
	productID, err := strconv.ParseUint(c.Param("productId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}

	var req model.UpdateCartItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	resp, err := h.svc.UpdateCartItem(c.Request.Context(), userID, productID, req)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// RemoveCartItems 对应 DELETE /api/v1/cart/items，body：{product_ids: []}。
// 单行删除和结算清车共用同一接口（传一个元素的数组 vs 勾选项数组），
// 批量幂等语义在下游 repo 保证，可安全重试。
func (h *Handler) RemoveCartItems(c *gin.Context) {
	var req model.RemoveCartItemsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing caller identity"})
		return
	}

	resp, err := h.svc.RemoveCartItems(c.Request.Context(), userID, req)
	if err != nil {
		h.respondGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// respondGRPCError 把下游 gRPC 服务返回的 status error 翻译成 HTTP 状态码。
// 这是 pkg/errors.ToHTTPStatus 唯一的调用点——gateway 里所有 handler 共用同一套翻译规则。
func (h *Handler) respondGRPCError(c *gin.Context, err error) {
	status, msg := apperrors.ToHTTPStatus(err)
	if status == http.StatusInternalServerError {
		h.log.Error("downstream call failed", zap.Error(err), zap.String("path", c.FullPath()))
	}
	c.JSON(status, gin.H{"error": msg})
}
