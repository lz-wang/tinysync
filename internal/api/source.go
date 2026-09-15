package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// registerSourceRoutes 注册 Source 管理端点。svc 为 nil 时跳过注册
// （依赖缺失时由未知路径 404 兜底，避免生产静默降级之外的 panic）。
// jobs 非 nil 时启用删除保护：被 Job 引用的 Source 返回 409，
// 数据库层 FK RESTRICT 作为并发路径的兜底。
func registerSourceRoutes(group *gin.RouterGroup, svc *source.Service, jobs *syncjob.Service) {
	if svc == nil {
		return
	}
	h := &sourceHandlers{svc: svc}
	if jobs != nil {
		h.refGuard = func(ctx context.Context, sourceID string) error {
			count, err := jobs.CountBySource(ctx, sourceID)
			if err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("%w: %d job(s) reference %s", syncjob.ErrSourceInUse, count, sourceID)
			}
			return nil
		}
	}
	group.GET("/sources", h.list)
	group.POST("/sources", h.create)
	group.GET("/sources/:id", h.get)
	group.PATCH("/sources/:id", h.update)
	group.DELETE("/sources/:id", h.delete)
	group.POST("/sources/:id/test", h.test)
}

// sourceHandlers 是 Source 端点的 handler 集合。
type sourceHandlers struct {
	svc *source.Service
	// refGuard 校验 Source 是否被 Job 引用；nil 表示不启用保护。
	// 删除始终校验；修改 endpoint 时校验（防止 Mirror Job 下轮连接到
	// 另一个合法远端后把全部 managed 文件误判为远端消失）。
	refGuard func(ctx context.Context, sourceID string) error
}

// sourceDTO 是 Source 的 API 表示。刻意不含 password 字段：
// 凭据状态只以 password_set 布尔暴露，杜绝序列化层泄漏。
type sourceDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Endpoint    string `json:"endpoint"`
	Username    string `json:"username"`
	PasswordSet bool   `json:"password_set"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// toSourceDTO 转换领域对象，时间输出 RFC3339。
func toSourceDTO(s source.Source) sourceDTO {
	return sourceDTO{
		ID:          s.ID,
		Name:        s.Name,
		Type:        string(s.Type),
		Endpoint:    s.Endpoint,
		Username:    s.Username,
		PasswordSet: s.PasswordSet,
		Enabled:     s.Enabled,
		CreatedAt:   s.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   s.UpdatedAt.Format(time.RFC3339),
	}
}

// createSourceRequest 是创建请求体；Enabled 缺省为 true。
type createSourceRequest struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Endpoint string `json:"endpoint"`
	Username string `json:"username"`
	Password string `json:"password"`
	Enabled  *bool  `json:"enabled"`
}

// updateSourceRequest 是更新请求体：nil 字段保留现有值；
// password 语义为缺省保留、空串清除、非空替换。
type updateSourceRequest struct {
	Name     *string `json:"name"`
	Endpoint *string `json:"endpoint"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	Enabled  *bool   `json:"enabled"`
}

// testResultDTO 是连接测试结果。连接失败也是成功完成的测试操作，
// 以 ok=false 表达；只有请求本身异常才走 REST 错误。
type testResultDTO struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// list GET /api/v1/sources。
func (h *sourceHandlers) list(c *gin.Context) {
	sources, err := h.svc.List(c.Request.Context())
	if err != nil {
		handleSourceError(c, err)
		return
	}
	dtos := make([]sourceDTO, 0, len(sources))
	for _, s := range sources {
		dtos = append(dtos, toSourceDTO(s))
	}
	c.JSON(http.StatusOK, gin.H{"sources": dtos})
}

// create POST /api/v1/sources。
func (h *sourceHandlers) create(c *gin.Context) {
	var req createSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	created, err := h.svc.Create(c.Request.Context(), source.CreateInput{
		Name:     req.Name,
		Type:     source.Type(req.Type),
		Endpoint: req.Endpoint,
		Username: req.Username,
		Password: req.Password,
		Enabled:  enabled,
	})
	if err != nil {
		handleSourceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toSourceDTO(created))
}

// get GET /api/v1/sources/:id。
func (h *sourceHandlers) get(c *gin.Context) {
	s, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		handleSourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, toSourceDTO(s))
}

// update PATCH /api/v1/sources/:id。被 Job 引用时允许改 name /
// credentials / enabled，但拒绝修改 endpoint：新 endpoint 可能指向
// 另一个合法远端，下轮完整扫描会把既有 managed 文件全部误判为
// 远端消失，Mirror 将据其删除本地。更换 endpoint 的正确路径是
// 新建 Source → Job 切换 SourceID（触发原子 metadata 重置）。
func (h *sourceHandlers) update(c *gin.Context) {
	var req updateSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	id := c.Param("id")
	if req.Endpoint != nil && h.refGuard != nil {
		current, err := h.svc.Get(c.Request.Context(), id)
		if err != nil {
			handleSourceError(c, err)
			return
		}
		if strings.TrimSpace(*req.Endpoint) != current.Endpoint {
			if err := h.refGuard(c.Request.Context(), id); err != nil {
				c.JSON(http.StatusConflict, gin.H{
					"error": "source endpoint cannot be changed while referenced by sync jobs",
				})
				return
			}
		}
	}
	updated, err := h.svc.Update(c.Request.Context(), id, source.UpdateInput{
		Name:     req.Name,
		Endpoint: req.Endpoint,
		Username: req.Username,
		Password: req.Password,
		Enabled:  req.Enabled,
	})
	if err != nil {
		handleSourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, toSourceDTO(updated))
}

// delete DELETE /api/v1/sources/:id。仅删除本地 Source 配置，
// 不触及远端文件；被 Sync Job 引用时以 409 拒绝。
func (h *sourceHandlers) delete(c *gin.Context) {
	if h.refGuard != nil {
		if err := h.refGuard(c.Request.Context(), c.Param("id")); err != nil {
			handleSourceError(c, err)
			return
		}
	}
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		handleSourceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// test POST /api/v1/sources/:id/test。
func (h *sourceHandlers) test(c *gin.Context) {
	result, err := h.svc.TestConnection(c.Request.Context(), c.Param("id"))
	if err != nil {
		handleSourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, testResultDTO{
		OK:        result.OK,
		LatencyMS: result.LatencyMS,
		Error:     result.Error,
	})
}

// handleSourceError 把领域错误映射为 REST 状态码。
func handleSourceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, source.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
	case errors.Is(err, source.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "source name already exists"})
	case errors.Is(err, syncjob.ErrSourceInUse):
		c.JSON(http.StatusConflict, gin.H{"error": "source is referenced by sync jobs"})
	case errors.Is(err, source.ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}
