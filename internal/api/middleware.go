package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/logging"
)

// requestIDMiddleware 为每个请求生成服务器端 request ID
// （req_<128-bit random hex>），写入响应头 X-Request-ID 与 gin
// context（键 requestIDKey）。不无条件信任客户端提供的 request
// id：header 中的值不参与生成，保证日志可归因到本服务发出的 ID。
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := newRequestID()
		if err != nil {
			// 随机源失败属进程级故障：fail closed（500），不留无 ID
			// 的请求进入日志体系。
			_ = c.AbortWithError(500, fmt.Errorf("generate request id: %w", err))
			return
		}
		c.Set(requestIDKey, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// requestIDKey 是 gin context 中 request ID 的键名。
const requestIDKey = "request_id"

// newRequestID 生成 req_<128-bit random hex> 形式的请求 ID。
func newRequestID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "req_" + hex.EncodeToString(buf), nil
}

// accessLogMiddleware 输出项目的唯一 access log：复用全局 zap
// logger（文本单行），字段固定为 event=request、request_id、method、
// path、status、duration_ms、bytes。不记录 query string、请求体与
// 认证头（Authorization / Cookie 从不进入本中间件的任何字段）。
// 通过 defer 保证 panic（由下游 Recovery 收敛为 500）也留下日志。
func accessLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		defer func() {
			id, _ := c.Get(requestIDKey)
			requestID, _ := id.(string)
			logging.Infof("event=request request_id=%s method=%s path=%s status=%d duration_ms=%d bytes=%d",
				requestID,
				c.Request.Method,
				c.Request.URL.Path,
				c.Writer.Status(),
				time.Since(start).Milliseconds(),
				c.Writer.Size(),
			)
		}()
		c.Next()
	}
}
