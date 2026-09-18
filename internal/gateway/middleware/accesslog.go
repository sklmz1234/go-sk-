// accesslog.go：带 trace_id 的访问日志——阶段 2D 的"三支柱串联"落点。
//
// 日志、指标、追踪是可观测的三大支柱，而 trace_id 是把它们粘起来的钥匙：

package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"go-ecom-admin/pkg/telemetry"
)

// AccessLog 在请求结束时打一行结构化访问日志。必须挂在 otelgin 中间件
// 之后——先有 span 进 context，这里才能取到 trace_id。
func AccessLog(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		log.Info("access",
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
			zap.String("client_ip", c.ClientIP()),
			// 未采样/未开启追踪时为空串——日志依然有效，只是少一个跳转键。
			zap.String("trace_id", telemetry.TraceIDFromContext(c.Request.Context())),
		)
	}
}
