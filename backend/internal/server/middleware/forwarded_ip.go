package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"

	"github.com/gin-gonic/gin"
)

// ForwardedIPContext 固化当前请求的 IP 解析配置，避免管理端更新设置时同一请求前后不一致。
func ForwardedIPContext(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		settings := cfg.ForwardedClientIPSettings()
		ip.SetForwardedIPSettings(c, settings.TrustForwardedIP, settings.Headers)
		c.Next()
	}
}
