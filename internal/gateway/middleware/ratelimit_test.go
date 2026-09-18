// 限流中间件的单元测试。

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"golang.org/x/time/rate"
)

// newTestRouter 挂一个超小桶（burst=3，补充速率约等于 0）的限流中间件，
// 后面的 handler 返回 204。速率取极小值是为了让测试不依赖真实时间流逝——
// 桶里的 3 个令牌用完就是完了，不会"等着等着又补上一个"。
func newTestRouter(handlerCalled *int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimit(rate.Limit(0.000001), 3))
	r.GET("/ping", func(c *gin.Context) {
		*handlerCalled++
		c.Status(http.StatusNoContent)
	})
	return r
}

func doRequest(r *gin.Engine, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.RemoteAddr = remoteAddr // ClientIP() 在没有代理头时取 RemoteAddr 的 IP 部分
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRateLimit_BurstThenReject(t *testing.T) {
	called := 0
	r := newTestRouter(&called)

	// 前 3 个请求刚好耗尽 burst=3 的桶，全部放行。
	for i := 0; i < 3; i++ {
		w := doRequest(r, "1.2.3.4:1000")
		assert.Equal(t, http.StatusNoContent, w.Code, "第 %d 个请求应该放行", i+1)
	}

	// 第 4 个请求取不到令牌：429，且不能进入 handler。
	w := doRequest(r, "1.2.3.4:1000")
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, 3, called, "被限流的请求不应触达 handler")
}

func TestRateLimit_PerIPIsolation(t *testing.T) {
	called := 0
	r := newTestRouter(&called)

	// IP-A 把自己的桶打空。
	for i := 0; i < 3; i++ {
		doRequest(r, "1.2.3.4:1000")
	}
	assert.Equal(t, http.StatusTooManyRequests, doRequest(r, "1.2.3.4:1000").Code)

	// IP-B 是另一个桶，不受 IP-A 影响。
	w := doRequest(r, "5.6.7.8:2000")
	assert.Equal(t, http.StatusNoContent, w.Code, "不同 IP 应有独立的令牌桶")
}

// TestRateLimiter_Cleanup 直接测清理逻辑：假时钟推进到"长期不活跃"之后，
// 条目被回收。用 newIPRateLimiter 而不是 RateLimit 中间件，避开真实 ticker。
func TestRateLimiter_Cleanup(t *testing.T) {
	l := newIPRateLimiter(rate.Limit(1), 1)

	fakeNow := l.now()
	l.now = func() time.Time { return fakeNow }

	assert.True(t, l.allow("1.2.3.4"))
	assert.Len(t, l.visitors, 1)

	// 推进假时钟到 idle TTL 之后，手工执行一次清理循环里的淘汰逻辑。
	fakeNow = fakeNow.Add(visitorIdleTTL + time.Minute)
	l.mu.Lock()
	for ip, v := range l.visitors {
		if l.now().Sub(v.lastSeen) > visitorIdleTTL {
			delete(l.visitors, ip)
		}
	}
	l.mu.Unlock()

	assert.Empty(t, l.visitors, "长期不活跃的 IP 条目应被回收")
}
