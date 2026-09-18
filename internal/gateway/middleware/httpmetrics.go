// httpmetrics.go：网关 HTTP 指标中间件——每个请求记录"耗时直方图 + 计数"，
// 是 QPS / P99 延迟 / 错误率三张面板的数据源头。

package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// HTTPMetrics 返回记录 HTTP 指标的 gin 中间件，挂在 otelgin 之后
// （顺序不影响指标本身，但和 AccessLog 保持"先观测后业务"的队形）。
//
// 仪器在构造函数里创建一次、被所有请求复用——每次请求新建仪器是
// 常见的错误用法，会白白消耗 SDK 内部的注册表。telemetry 未启用时
// 全局 provider 是 noop，仪器变成空操作，零开销；且全局 provider
// 是延迟绑定的，main 里 router.New 先于 SetupMetrics 执行也安全。
func HTTPMetrics() gin.HandlerFunc {
	meter := otel.Meter("go-ecom-admin/gateway")
	// 命名遵循 OTel HTTP 语义约定（http.server.request.duration），
	// Prometheus 导出时规范化为 http_server_request_duration_seconds。
	duration, err := meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("HTTP 请求处理耗时"),
	)
	if err != nil {
		panic("create http duration histogram: " + err.Error())
	}
	requests, err := meter.Int64Counter(
		"http.server.request.count",
		metric.WithDescription("HTTP 请求总数"),
	)
	if err != nil {
		panic("create http request counter: " + err.Error())
	}

	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			// 没匹配到任何路由的请求（404）没有路由模式，归入固定桶，
			// 同样不能把原始路径塞进去（扫描器乱打的 URL 也是高基数）。
			route = "unmatched"
		}
		if route == "/metrics" {
			// Prometheus 自己每 5s 抓一次 /metrics，计入指标会让 QPS
			// 面板多出一条恒定底噪曲线——观测系统不该观测自己的观测。
			return
		}
		attrs := metric.WithAttributes(
			attribute.String("http.request.method", c.Request.Method),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", c.Writer.Status()),
		)
		// 用请求的 ctx 记录：ctx 里带着 otelgin 建的 span，exporter
		// 因此能给指标点附上 exemplar（trace_id）——Grafana 里点一个
		// 延迟尖峰就能跳到那条慢请求的完整瀑布图，这就是三支柱的粘合。
		duration.Record(c.Request.Context(), time.Since(start).Seconds(), attrs)
		requests.Add(c.Request.Context(), 1, attrs)
	}
}
