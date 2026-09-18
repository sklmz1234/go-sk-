// ratelimit.go 实现按客户端 IP 的令牌桶限流中间件。

package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

const (
	// cleanupInterval 清理间隔：每分钟扫一次 map。
	cleanupInterval = time.Minute
	// visitorIdleTTL 一个 IP 超过 3 分钟没有任何请求，它的桶就被回收——
	// 3 分钟不活动意味着桶早已补满（速率 r 下 burst 个令牌几秒就满），
	// 删掉条目不影响它下次来时"带着满桶重新开始"的行为。
	visitorIdleTTL = 3 * time.Minute
)

// visitor 记录一个 IP 的令牌桶和最后活跃时间（供清理协程判断回收）。
type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// ipRateLimiter 是"每 IP 一个桶"的注册表。两个已知局限（面试要能讲）：
//  1. 单机内存态：K8s 多副本时每个 Pod 各有自己的 map，实际阈值约等于
//     配置值 × 副本数。生产方案是把计数收敛到 Redis（Lua 脚本实现分布式
//     令牌桶），本项目按学习路线先落地单机版。
//  2. 按 IP 限流对 NAT/公司出口后的多个真实用户是"连坐"的，对换 IP 的
//     攻击者又太宽松——所以它是兜底防线，不替代按用户/按账号的限流。
type ipRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	r        rate.Limit
	burst    int
	now      func() time.Time // 便于测试注入假时钟
}

func newIPRateLimiter(r rate.Limit, burst int) *ipRateLimiter {
	return &ipRateLimiter{
		visitors: make(map[string]*visitor),
		r:        r,
		burst:    burst,
		now:      time.Now,
	}
}

// allow 取出该 IP 的桶并尝试消费一个令牌。
// 锁的粒度是关键设计点：mutex 只保护 map 的查/建（纳秒级），
// Allow() 在锁外调用（rate.Limiter 内部已并发安全）——否则所有 IP 的
// 请求会在锁上串行化
func (l *ipRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	v, ok := l.visitors[ip]
	if !ok {
		v = &visitor{limiter: rate.NewLimiter(l.r, l.burst)}
		l.visitors[ip] = v
	}
	v.lastSeen = l.now()
	limiter := v.limiter
	l.mu.Unlock()

	return limiter.Allow()
}

// cleanupLoop 定期回收长期不活跃的 IP 条目，防止恶意换 IP 刷请求导致
// map 无限膨胀（内存泄漏）。协程随进程生命周期存在，不做 stop——
// 中间件本身就是进程级的，进程退出协程自然结束。
func (l *ipRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		for ip, v := range l.visitors {
			if l.now().Sub(v.lastSeen) > visitorIdleTTL {
				delete(l.visitors, ip)
			}
		}
		l.mu.Unlock()
	}
}

// RateLimit 返回一个按客户端 IP 限流的 Gin 中间件。
// r 是每秒补充的令牌数，burst 是桶容量（允许的最大突发）。超出的请求
// 直接 429，不进入后续 handler——响应体保持和项目其他错误一致的 {"error": ...} 形态。
func RateLimit(r rate.Limit, burst int) gin.HandlerFunc {
	limiter := newIPRateLimiter(r, burst)
	go limiter.cleanupLoop()

	return func(c *gin.Context) {
		if !limiter.allow(c.ClientIP()) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded, please slow down"})
			return
		}
		c.Next()
	}
}
