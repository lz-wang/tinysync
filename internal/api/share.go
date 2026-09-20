package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
	"tinysync/internal/browser"
	"tinysync/internal/filesafe"
	"tinysync/internal/share"
	"tinysync/internal/source"
)

// registerShareRoutes 注册共享策略 CRUD 端点。svc 为 nil 时跳过注册。
func registerShareRoutes(group *gin.RouterGroup, svc *share.Service) {
	if svc == nil {
		return
	}
	h := &shareHandlers{svc: svc}
	// 权限矩阵：查询 read；创建 / 更新 / 删除 admin。
	group.GET("/shares", requireScope(auth.ScopeRead), h.list)
	group.POST("/shares", requireScope(auth.ScopeAdmin), h.create)
	group.PATCH("/shares/:id", requireScope(auth.ScopeAdmin), h.update)
	group.DELETE("/shares/:id", requireScope(auth.ScopeAdmin), h.remove)
}

// registerSharedServingRoutes 注册公开共享路由：/shared/:slug 是浏览页
// 深链接（返回 SPA index.html，带 noindex，ADR-0001）；
// /shared/:slug/*path 是文件直链。二者均为 Gin 显式注册的路由，
// 不落入 SPA NoRoute fallback。svc 为 nil 时跳过注册。
func registerSharedServingRoutes(router *gin.Engine, webFS fs.FS, svc *share.Service) {
	if svc == nil {
		return
	}
	h := &shareHandlers{svc: svc, webFS: webFS}
	router.GET("/shared/:slug", h.page)
	router.HEAD("/shared/:slug", h.page)
	router.GET("/shared/:slug/*path", h.serve)
	router.HEAD("/shared/:slug/*path", h.serve)
}

// registerPublicShareRoutes 注册无认证的公开共享端点：索引卡片与
// 浏览分页。svc 为 nil 时跳过注册。
func registerPublicShareRoutes(group *gin.RouterGroup, svc *share.Service) {
	if svc == nil {
		return
	}
	h := &shareHandlers{svc: svc}
	group.GET("/public/shares", h.publicList)
	group.GET("/public/shares/:slug/entries", h.publicEntries)
}

// publicShareCardDTO 是公开索引卡片的 API 表示：name 为回落后的
// 展示名（未命名共享显示目标 basename）；expires_at 为 RFC3339 或
// 空串（永不过期）。
type publicShareCardDTO struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

// publicList 返回全部可服务共享的卡片：过期 / 禁用共享完全不出现在
// 结果中（ADR-0001）。
func (h *shareHandlers) publicList(c *gin.Context) {
	shares, err := h.svc.ListPublic(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if shares == nil {
		shares = []share.Share{}
	}
	cards := make([]publicShareCardDTO, 0, len(shares))
	for _, s := range shares {
		card := publicShareCardDTO{
			Slug:      s.Slug,
			Name:      s.DisplayName(),
			IsDir:     s.IsDir,
			CreatedAt: s.CreatedAt.Format(time.RFC3339),
		}
		if s.ExpiresAt != nil {
			card.ExpiresAt = s.ExpiresAt.Format(time.RFC3339)
		}
		cards = append(cards, card)
	}
	c.JSON(http.StatusOK, gin.H{"shares": cards})
}

// publicEntries 返回共享浏览视图的一页条目（目录单层或文件共享的
// 单条目虚拟根）：path 缺省 /，limit / cursor 与本地浏览端点同一
// 语义；共享不可服务或路径不可用一律同形 404。
func (h *shareHandlers) publicEntries(c *gin.Context) {
	p := queryPath(c)
	opts, err := listOptionsFromQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	entries, nextCursor, err := h.svc.Browse(c.Request.Context(), c.Param("slug"), p, opts)
	if err != nil {
		handleShareError(c, err)
		return
	}
	if entries == nil {
		entries = []browser.Entry{}
	}
	c.JSON(http.StatusOK, localListResponse{
		Path:       p,
		Entries:    entries,
		NextCursor: nextCursor,
	})
}

// shareHandlers 是共享端点的 handler 集合。
type shareHandlers struct {
	svc   *share.Service
	webFS fs.FS
}

// shareDTO 是共享策略的 API 表示：expires_at 输出 RFC3339 或空串
// （永不过期）；name 为自定义共享名称（null 表示未命名）。
type shareDTO struct {
	ID        string  `json:"id"`
	LocalPath string  `json:"local_path"`
	Slug      string  `json:"slug"`
	Name      *string `json:"name"`
	IsDir     bool    `json:"is_dir"`
	Enabled   bool    `json:"enabled"`
	ExpiresAt string  `json:"expires_at"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

func toShareDTO(s share.Share) shareDTO {
	dto := shareDTO{
		ID:        s.ID,
		LocalPath: s.LocalPath,
		Slug:      s.Slug,
		Name:      s.Name,
		IsDir:     s.IsDir,
		Enabled:   s.Enabled,
		CreatedAt: s.CreatedAt.Format(time.RFC3339),
		UpdatedAt: s.UpdatedAt.Format(time.RFC3339),
	}
	if s.ExpiresAt != nil {
		dto.ExpiresAt = s.ExpiresAt.Format(time.RFC3339)
	}
	return dto
}

// createShareRequest 是创建请求体：目标以 job_id + LocalRoot 内逻辑
// 路径表达（"/" 即整个本地根），不接受任意 local_path；name 为可选
// 的自定义共享名称（即 slug，留空随机生成）。
type createShareRequest struct {
	JobID     string `json:"job_id"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	Enabled   *bool  `json:"enabled"`
	ExpiresAt string `json:"expires_at"`
}

// updateShareRequest 是更新请求体：name 三态——缺失（不变）、空串
// （清除自定义名称，slug 不变）、非空（同时改写 slug，旧链接失效）；
// expires_at 区分「缺失（不变）」「null（清除）」与「RFC3339 时刻
// （设置）」。
type updateShareRequest struct {
	Name      *string         `json:"name"`
	Enabled   *bool           `json:"enabled"`
	ExpiresAt json.RawMessage `json:"expires_at"`
}

// list 返回全部共享策略。
func (h *shareHandlers) list(c *gin.Context) {
	shares, err := h.svc.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if shares == nil {
		shares = []share.Share{}
	}
	dtos := make([]shareDTO, 0, len(shares))
	for _, s := range shares {
		dtos = append(dtos, toShareDTO(s))
	}
	c.JSON(http.StatusOK, gin.H{"shares": dtos})
}

// create 创建共享策略；校验失败 400、Job 不存在 404、slug 冲突 409。
// 请求体经严格解码：未知字段与尾随 JSON 一律 400，与 Source API 的
// 契约风格一致——「API 不接受 local_path」不能靠静默忽略拼写错误
// 的字段维持。
func (h *shareHandlers) create(c *gin.Context) {
	var req createShareRequest
	if !strictBind(c, &req) {
		return
	}
	input := share.CreateInput{
		JobID:   req.JobID,
		Path:    req.Path,
		Name:    req.Name,
		Enabled: req.Enabled == nil || *req.Enabled,
	}
	if req.ExpiresAt != "" {
		expiry, err := parseRFC3339(req.ExpiresAt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be RFC3339"})
			return
		}
		input.ExpiresAt = &expiry
	}
	created, err := h.svc.Create(c.Request.Context(), input)
	if err != nil {
		handleShareError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toShareDTO(created))
}

// update 部分更新共享策略：local_path 与 is_dir 不可变；请求体经
// 严格解码（同 create），PATCH 只接受声明过的字段。
func (h *shareHandlers) update(c *gin.Context) {
	var req updateShareRequest
	if !strictBind(c, &req) {
		return
	}
	input := share.UpdateInput{Name: req.Name, Enabled: req.Enabled}
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
	updated, err := h.svc.Update(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		handleShareError(c, err)
		return
	}
	c.JSON(http.StatusOK, toShareDTO(updated))
}

// remove 删除共享策略（只移除记录，不触及本地文件）。
func (h *shareHandlers) remove(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		handleShareError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": c.Param("id")})
}

// page 服务 /shared/:slug 浏览页深链接：返回 SPA index.html，由前端
// 公开路由渲染。页面不落缓存（发版即生效），并以 X-Robots-Tag
// 拒绝收录（ADR-0001）。
func (h *shareHandlers) page(c *gin.Context) {
	data, err := fs.ReadFile(h.webFS, "index.html")
	if err != nil {
		_ = c.Error(err)
		c.String(http.StatusInternalServerError, "embedded web assets missing")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Robots-Tag", "noindex")
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}

// serve 文件直链（GET / HEAD /shared/:slug/*path）：禁用 / 过期 /
// 共享或文件缺失 / 指向目录 / 逃逸 / 文件共享的非常规路径一律同形
// 404；Range → 206 / 416；响应固定 Cache-Control: no-store + nosniff
// + noindex。
func (h *shareHandlers) serve(c *gin.Context) {
	rest := c.Param("path")
	if rest == "/" {
		// 显式尾随斜杠回到浏览页（/shared/<slug>/ → /shared/<slug>）。
		c.Redirect(http.StatusPermanentRedirect, "/shared/"+c.Param("slug"))
		return
	}
	// OpenFile 在服务侧完成可服务判定与 canonical 身份复验，任何
	// 失败都与「共享不存在」同形返回 404，不泄露当前状态。
	name, f, info, err := h.svc.OpenFile(c.Request.Context(), c.Param("slug"), rest)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	defer func() { _ = f.Close() }()

	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Robots-Tag", "noindex")
	if !rangeRequested(c.Request) {
		c.Status(http.StatusOK)
	}
	// *os.File 是 ReadSeeker：Range / 206 / 416 / HEAD 与 MIME 由
	// filesafe 的共享 serving 出口统一处理，与 Local 下载同语义。
	filesafe.ServeFileContent(c.Writer, c.Request, f, info, name)
}

// handleShareError 把 share 领域错误映射为 HTTP 响应：invalid 400、
// not found 404、冲突 409。
func handleShareError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, share.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, share.ErrConflict):
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
