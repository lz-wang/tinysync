package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/filesafe"
	"tinysync/internal/publish"
	"tinysync/internal/source"
)

// registerPublishRoutes 注册发布策略 CRUD 端点。svc 为 nil 时跳过
// 注册。
func registerPublishRoutes(group *gin.RouterGroup, svc *publish.Service) {
	if svc == nil {
		return
	}
	h := &publishHandlers{svc: svc}
	group.GET("/published-files", h.list)
	group.POST("/published-files", h.create)
	group.PATCH("/published-files/:id", h.update)
	group.DELETE("/published-files/:id", h.remove)
}

// registerPublicServingRoutes 注册公开文件路由：/published/*path 是
// Gin 显式注册的路由，不落入 SPA NoRoute fallback。public 为 nil 时
// 跳过注册。
func registerPublicServingRoutes(router *gin.Engine, public *publish.Service) {
	if public == nil {
		return
	}
	h := &publishHandlers{svc: public}
	router.GET("/published/*path", h.serve)
	router.HEAD("/published/*path", h.serve)
}

// publishHandlers 是发布端点的 handler 集合。
type publishHandlers struct {
	svc *publish.Service
}

// publishedFileDTO 是发布策略的 API 表示：expires_at 输出 RFC3339
// 或 null。
type publishedFileDTO struct {
	ID         string `json:"id"`
	LocalPath  string `json:"local_path"`
	PublicPath string `json:"public_path"`
	Enabled    bool   `json:"enabled"`
	ExpiresAt  string `json:"expires_at"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func toPublishedDTO(p publish.PublishedFile) publishedFileDTO {
	dto := publishedFileDTO{
		ID:         p.ID,
		LocalPath:  p.LocalPath,
		PublicPath: p.PublicPath,
		Enabled:    p.Enabled,
		CreatedAt:  p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  p.UpdatedAt.Format(time.RFC3339),
	}
	if p.ExpiresAt != nil {
		dto.ExpiresAt = p.ExpiresAt.Format(time.RFC3339)
	}
	return dto
}

// createPublishRequest 是创建请求体：目标以 job_id + LocalRoot 内
// 逻辑路径表达，不接受任意 local_path。
type createPublishRequest struct {
	JobID      string `json:"job_id"`
	Path       string `json:"path"`
	PublicPath string `json:"public_path"`
	Enabled    *bool  `json:"enabled"`
	ExpiresAt  string `json:"expires_at"`
}

// list 返回全部发布策略。
func (h *publishHandlers) list(c *gin.Context) {
	policies, err := h.svc.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if policies == nil {
		policies = []publish.PublishedFile{}
	}
	dtos := make([]publishedFileDTO, 0, len(policies))
	for _, p := range policies {
		dtos = append(dtos, toPublishedDTO(p))
	}
	c.JSON(http.StatusOK, gin.H{"published_files": dtos})
}

// create 创建发布策略；校验失败 400、Job 不存在 404、public_path
// 冲突 409。请求体经严格解码：未知字段与尾随 JSON 一律 400，与
// Source API 的契约风格一致——「API 不接受 local_path」不能靠静默
// 忽略拼写错误的字段维持。
func (h *publishHandlers) create(c *gin.Context) {
	var req createPublishRequest
	if !strictBind(c, &req) {
		return
	}
	input := publish.CreateInput{
		JobID:      req.JobID,
		Path:       req.Path,
		PublicPath: req.PublicPath,
		Enabled:    req.Enabled == nil || *req.Enabled,
	}
	if req.ExpiresAt != "" {
		expiry, err := parseRFC3339(req.ExpiresAt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be RFC3339"})
			return
		}
		input.ExpiresAt = &expiry
	}
	policy, err := h.svc.Create(c.Request.Context(), input)
	if err != nil {
		handlePublishError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toPublishedDTO(policy))
}

// updatePublishRequest 是更新请求体：expires_at 用 RawMessage 区分
// 「缺失（不变）」「null（清除）」与「RFC3339 时刻（设置）」。
type updatePublishRequest struct {
	PublicPath *string         `json:"public_path"`
	Enabled    *bool           `json:"enabled"`
	ExpiresAt  json.RawMessage `json:"expires_at"`
}

// update 部分更新策略：local_path 不可变；请求体经严格解码（同
// create），PATCH 只接受声明过的字段。
func (h *publishHandlers) update(c *gin.Context) {
	var req updatePublishRequest
	if !strictBind(c, &req) {
		return
	}
	input := publish.UpdateInput{PublicPath: req.PublicPath, Enabled: req.Enabled}
	if len(req.ExpiresAt) > 0 {
		raw := strings.TrimSpace(string(req.ExpiresAt))
		if raw == "null" {
			input.ClearExpires = true
		} else {
			expiry, err := parseRFC3339(raw)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be RFC3339 or null"})
				return
			}
			input.ExpiresAt = &expiry
		}
	}
	policy, err := h.svc.Update(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		handlePublishError(c, err)
		return
	}
	c.JSON(http.StatusOK, toPublishedDTO(policy))
}

// remove 删除策略（只移除记录，不触及本地文件）。
func (h *publishHandlers) remove(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		handlePublishError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": c.Param("id")})
}

// serve 公开文件服务（GET / HEAD /published/*path）：disabled /
// 过期 / 文件缺失 / 目录一律 404；Range → 206 / 416；响应固定
// Cache-Control: no-store（第一版语义优先，可配置缓存策略不进入
// v0.6）。
func (h *publishHandlers) serve(c *gin.Context) {
	policy, err := h.svc.ResolveForRequest(c.Request.Context(), c.Param("path"))
	if err != nil {
		handlePublishError(c, err)
		return
	}
	// canonical local_path 在创建后可能被替换：OpenCanonicalRegularFile
	// 复验「最终组件非 symlink + 全链解析仍等于持久化路径 + 普通文件」，
	// 经 symlink 指向 root 外文件的路径在此拒绝。
	f, info, err := filesafe.OpenCanonicalRegularFile(policy.LocalPath)
	if err != nil {
		// 与「策略不存在」同形返回 404，不泄露文件系统当前状态。
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	defer func() { _ = f.Close() }()

	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if !rangeRequested(c.Request) {
		c.Status(http.StatusOK)
	}
	// *os.File 是 ReadSeeker：Range / 206 / 416 / HEAD 与 MIME 由
	// filesafe 的共享 serving 出口统一处理，与 Local 下载同语义。
	filesafe.ServeFileContent(c.Writer, c.Request, f, info, path.Base(policy.LocalPath))
}

// handlePublishError 把 publish 领域错误映射为 HTTP 响应：invalid
// 400、not found 404、冲突 409。
func handlePublishError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, publish.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, publish.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, source.ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}

// parseRFC3339 解析 RFC3339 时刻（JSON 字符串已去引号后的形态）。
func parseRFC3339(raw string) (time.Time, error) {
	return time.Parse(time.RFC3339, strings.Trim(raw, `"`))
}
