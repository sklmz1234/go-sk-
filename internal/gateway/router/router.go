// Package router 负责把 URL 路径映射到 handler 方法，是唯一知道具体路由表的地方。
// main.go 只需要调用 router.New(...) 拿到一个 *gin.Engine，不需要关心路由细节。
package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"go-ecom-admin/internal/gateway/handler"
	"go-ecom-admin/internal/gateway/middleware"
)

// 限流参数：每 IP 每秒补 10 个令牌，桶容量 20（允许 20 个请求的瞬时突发）。
// 取值思路：正常浏览商品的频率远低于 10 QPS，这个额度对真实用户无感，
// 但能挡住脚本对 /auth/login 这类重接口（bcrypt 校验约 100ms/次）的爆破。
// 学习项目先用常量；生产上应该进 config，配合多副本还要换 Redis 分布式限流
// （理由见 ratelimit.go 里 ipRateLimiter 的注释）。
const (
	rateLimitPerSecond rate.Limit = 10
	rateLimitBurst                = 20
)

// New 需要 jwtSecret 才能组装出鉴权中间件——路由表是唯一决定"哪些接口
// 需要登录"的地方，所以中间件的接入点也放在这里，而不是让每个 handler
// 自己判断要不要校验 token。
//
// log 用于访问日志中间件（带 trace_id 的那行 access log，见 accesslog.go）。
//
// metricsHandler 是 /metrics 端点的处理器（阶段 2D 指标）：telemetry 开启时
// 由 main 传入 telemetry.SetupMetrics 的返回值；为 nil 表示指标未启用，
// 不注册端点——请求 /metrics 会得到和业务 404 一致的行为，不暴露任何信息。
func New(h *handler.Handler, jwtSecret string, log *zap.Logger, metricsHandler http.Handler) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	// otelgin 放在限流之前：被限流拒绝的请求也会留下"被拒绝"的 trace，
	// 否则攻击/突发流量在追踪系统里就是黑洞，排查"为什么全 429"没有数据。
	// telemetry 未启用时全局 provider 是 noop，span 自动变成空操作，零开销。
	r.Use(otelgin.Middleware("api-gateway"))
	r.Use(middleware.AccessLog(log))
	// HTTP 指标（QPS/P99/错误率的数据源）：标签用路由模式防基数爆炸，
	// 细节见 httpmetrics.go 的包注释。
	r.Use(middleware.HTTPMetrics())
	// 限流是所有中间件里最外层的一道：比 JWT 验签更早执行才省钱，
	// 且未登录接口（/auth/login）也受它保护。
	r.Use(middleware.RateLimit(rateLimitPerSecond, rateLimitBurst))

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})
	if metricsHandler != nil {
		// 复用网关的 HTTP 端口暴露指标（两个 gRPC 服务则走独立 :9464，
		// 见 cmd/*/main.go）。本地开发无所谓；生产环境应在 Ingress /
		// LB 层拦掉外网对 /metrics 的访问。
		r.GET("/metrics", gin.WrapH(metricsHandler))
	}

	auth := middleware.Auth(jwtSecret)

	api := r.Group("/api/v1")
	{
		// /auth 下的接口本身就是"证明你是谁"的入口，不能反过来要求已经登录。
		authGroup := api.Group("/auth")
		authGroup.POST("/register", h.Register)
		authGroup.POST("/login", h.Login)

		users := api.Group("/users")
		users.GET("/:id", h.GetUser)

		// 商品的"读"保持公开（游客可浏览），"写"（新增/改/删）需要登录——
		// 这是本阶段唯一的权限模型，还没有细到"哪个用户能改哪个商品"。
		products := api.Group("/products")
		products.GET("/:id", h.GetProduct)
		products.GET("", h.ListProducts)
		products.POST("", auth, h.CreateProduct)
		products.PUT("/:id", auth, h.UpdateProduct)
		products.DELETE("/:id", auth, h.DeleteProduct)

		// 订单（阶段 3）全部要求登录，没有公开读——订单是私密资源，
		// 连"浏览"都必须是自己的订单。
		orders := api.Group("/orders")
		orders.POST("", auth, h.CreateOrder)
		orders.GET("", auth, h.ListMyOrders)
		orders.GET("/:id", auth, h.GetOrder)
		orders.POST("/:id/cancel", auth, h.CancelOrder)
		// 模拟支付（阶段 5B）：PENDING → PAID 的状态迁移，同 cancel 的子资源
		// 动作路径。真实支付接入后这个端点就是"支付回调"的落点。
		orders.POST("/:id/pay", auth, h.PayOrder)

		// 购物车（阶段 5B）同订单一样全部要求登录：购物车是私密资源，
		// 且下游 CartService 每个方法都要从 metadata 验 user_id（零信任）。
		// 四个路由覆盖：加购 / 列表 / 改数量 / 批量删（结算清车共用）。
		cart := api.Group("/cart")
		cart.POST("/items", auth, h.AddCartItem)
		cart.GET("", auth, h.ListCart)
		cart.PUT("/items/:productId", auth, h.UpdateCartItem)
		cart.DELETE("/items", auth, h.RemoveCartItems)

		// 收货信息（阶段 5B）：随机 mock 地址，结算/支付页展示用。
		// 只有这一个读端点（mock 池没有 CRUD），独立成组而不是塞进
		// /users——它不属于任何用户的资源。
		addresses := api.Group("/addresses", auth)
		addresses.GET("/random", h.GetRandomAddress)
	}

	return r
}
