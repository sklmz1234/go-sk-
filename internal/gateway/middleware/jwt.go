// Package middleware 存放 中间件。
package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"go-ecom-admin/pkg/jwt"
)

const (
	userIDKey   = "user_id"
	usernameKey = "username"
)

// Auth 返回一个只做一件事的中间件：校验 Authorization header 里的 JWT，
// 通过则把 userID/username 写进 gin.Context 供后续 handler 读取，不通过
// 直接 401 并 Abort（不再执行后面的 handler）。
//
// secret 在 router 组装路由时以参数形式传入（来自 cfg.JWT.Secret），
// 这里不读全局配置——和 pkg/jwt 的设计保持一致，谁用哪个 secret 一目了然。
func Auth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		const prefix = "Bearer "
		if header == "" || !strings.HasPrefix(header, prefix) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or malformed authorization header"})
			return
		}

		tokenString := strings.TrimPrefix(header, prefix)
		claims, err := jwt.Parse(tokenString, secret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		c.Set(userIDKey, claims.UserID)
		c.Set(usernameKey, claims.Username)
		c.Next()
	}
}

func UserIDFromContext(c *gin.Context) (uint64, bool) {
	v, ok := c.Get(userIDKey)
	if !ok {
		return 0, false
	}
	id, ok := v.(uint64)
	return id, ok
}

func UsernameFromContext(c *gin.Context) (string, bool) {
	v, ok := c.Get(usernameKey)
	if !ok {
		return "", false
	}
	name, ok := v.(string)
	return name, ok
}
