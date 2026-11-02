package oci

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/oci/image"
	"tinysync/internal/oci/registry"
)

// Handlers 是 OCI 领域端点的 gin handler 集合（Registry 配置 CRUD、镜像
// 管理查询与远端 discovery）。路由注册与 scope 中间件由 internal/api
// 完成（registerOciRoutes），本类型只承载请求 / 响应映射。
type Handlers struct {
	svc *Service
}

// NewHandlers 构造 handler 集合；svc 为 nil 时路由不注册。
func NewHandlers(svc *Service) *Handlers {
	return &Handlers{svc: svc}
}

// ---------------------------------------------------------------------------
// Registry CRUD
// ---------------------------------------------------------------------------

// registryDTO 是 Registry 配置的 API 表示：password 永不回显（只回
// password_set），namespace 为 host 的派生只读字段。
type registryDTO struct {
	ID                 string                        `json:"id"`
	Name               string                        `json:"name"`
	Kind               string                        `json:"kind"`
	Host               string                        `json:"host"`
	Namespace          string                        `json:"namespace"`
	Username           string                        `json:"username"`
	PasswordSet        bool                          `json:"password_set"`
	InsecureSkipVerify bool                          `json:"insecure_skip_verify"`
	PlainHTTP          bool                          `json:"plain_http"`
	StripRegistries    []string                      `json:"strip_registries"`
	PathTransform      *registry.PathTransformConfig `json:"path_transform"`
	Mirrors            []string                      `json:"mirrors"`
	CreatedAt          string                        `json:"created_at"`
	UpdatedAt          string                        `json:"updated_at"`
}

func toRegistryDTO(row Registry) registryDTO {
	return registryDTO{
		ID:                 row.ID,
		Name:               row.Name,
		Kind:               string(row.Kind),
		Host:               row.Host,
		Namespace:          row.Namespace(),
		Username:           row.Username,
		PasswordSet:        row.Password != "",
		InsecureSkipVerify: row.InsecureSkipVerify,
		PlainHTTP:          row.PlainHTTP,
		StripRegistries:    row.StripRegistries,
		PathTransform:      row.PathTransform,
		Mirrors:            row.Mirrors,
		CreatedAt:          row.CreatedAt.Format(time.RFC3339),
		UpdatedAt:          row.UpdatedAt.Format(time.RFC3339),
	}
}

// createRegistryRequest 是创建请求体；path_transform 为 null / 缺省均视为
// 未配置。kind 缺省 primary。
type createRegistryRequest struct {
	Name               string          `json:"name"`
	Kind               string          `json:"kind"`
	Host               string          `json:"host"`
	Username           string          `json:"username"`
	Password           string          `json:"password"`
	InsecureSkipVerify bool            `json:"insecure_skip_verify"`
	PlainHTTP          bool            `json:"plain_http"`
	StripRegistries    []string        `json:"strip_registries"`
	PathTransform      json.RawMessage `json:"path_transform"`
	Mirrors            []string        `json:"mirrors"`
}

// updateRegistryRequest 是更新请求体：nil 字段保留现有值。password 三态
// （缺省保留、空串清除、非空替换）；path_transform 携带（含 null）即替换。
type updateRegistryRequest struct {
	Name               *string         `json:"name"`
	Host               *string         `json:"host"`
	Username           *string         `json:"username"`
	Password           *string         `json:"password"`
	InsecureSkipVerify *bool           `json:"insecure_skip_verify"`
	PlainHTTP          *bool           `json:"plain_http"`
	StripRegistries    *[]string       `json:"strip_registries"`
	PathTransform      json.RawMessage `json:"path_transform"`
	Mirrors            *[]string       `json:"mirrors"`
}

// decodePathTransform 严格解码 path_transform 子对象；null / 空视为未配置。
func decodePathTransform(raw json.RawMessage) (*registry.PathTransformConfig, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var pt registry.PathTransformConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pt); err != nil {
		return nil, fmt.Errorf("invalid path_transform: %v", err)
	}
	return &pt, nil
}

// strictBind 严格解码请求体：拒绝未知字段并确认单一 JSON 值（拼写错误
// 在 400 暴露）；与 internal/api 的同名helper 同一契约。
func strictBind(c *gin.Context, target any) bool {
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return false
	}
	return true
}

// ListRegistries GET /registries。
func (h *Handlers) ListRegistries(c *gin.Context) {
	rows, err := h.svc.List(c.Request.Context())
	if err != nil {
		respondOciError(c, err)
		return
	}
	dtos := make([]registryDTO, 0, len(rows))
	for _, row := range rows {
		dtos = append(dtos, toRegistryDTO(row))
	}
	c.JSON(http.StatusOK, gin.H{"registries": dtos})
}

// CreateRegistry POST /registries。
func (h *Handlers) CreateRegistry(c *gin.Context) {
	var req createRegistryRequest
	if !strictBind(c, &req) {
		return
	}
	pathTransform, err := decodePathTransform(req.PathTransform)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	created, err := h.svc.Create(c.Request.Context(), CreateInput{
		Name:               req.Name,
		Kind:               RegistryKind(req.Kind),
		Host:               req.Host,
		Username:           req.Username,
		Password:           req.Password,
		InsecureSkipVerify: req.InsecureSkipVerify,
		PlainHTTP:          req.PlainHTTP,
		StripRegistries:    req.StripRegistries,
		PathTransform:      pathTransform,
		Mirrors:            req.Mirrors,
	})
	if err != nil {
		respondOciError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toRegistryDTO(created))
}

// UpdateRegistry PATCH /registries/:id。
func (h *Handlers) UpdateRegistry(c *gin.Context) {
	var req updateRegistryRequest
	if !strictBind(c, &req) {
		return
	}
	input := UpdateInput{
		Name:               req.Name,
		Host:               req.Host,
		Username:           req.Username,
		Password:           req.Password,
		InsecureSkipVerify: req.InsecureSkipVerify,
		PlainHTTP:          req.PlainHTTP,
		StripRegistries:    req.StripRegistries,
		Mirrors:            req.Mirrors,
	}
	if len(strings.TrimSpace(string(req.PathTransform))) > 0 {
		input.PathTransformSet = true
		pathTransform, err := decodePathTransform(req.PathTransform)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		input.PathTransform = pathTransform
	}
	updated, err := h.svc.Update(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		respondOciError(c, err)
		return
	}
	c.JSON(http.StatusOK, toRegistryDTO(updated))
}

// DeleteRegistry DELETE /registries/:id。
func (h *Handlers) DeleteRegistry(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		respondOciError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 镜像管理五端点（R9：DELETE /images 与 move 不移植）
// ---------------------------------------------------------------------------

// ListImages GET /images：oci 任务源镜像列表 + 最近执行状态。
func (h *Handlers) ListImages(c *gin.Context) {
	items, err := h.svc.ListImages(c.Request.Context())
	if err != nil {
		respondOciError(c, err)
		return
	}
	if items == nil {
		items = []ImageListItem{}
	}
	c.JSON(http.StatusOK, gin.H{"images": items})
}

// ListImageTags GET /images/tags?repo=。
func (h *Handlers) ListImageTags(c *gin.Context) {
	repo := strings.TrimSpace(c.Query("repo"))
	if repo == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query parameter 'repo' is required"})
		return
	}
	result, err := h.svc.ListTags(c.Request.Context(), repo)
	if err != nil {
		respondOciError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// imageInspectResponse 是 GET /images/inspect 的响应（oci-sync model 移植）。
type imageInspectResponse struct {
	Image  string                     `json:"image"`
	Result *image.RemoteInspectResult `json:"result"`
}

// imagePlatformInspectResponse 是 GET /images/inspect/platform 的响应。
type imagePlatformInspectResponse struct {
	Image    string                     `json:"image"`
	Platform image.PlatformInfo         `json:"platform"`
	Result   *image.RemoteInspectResult `json:"result"`
}

// parseInspectPlatformQuery 解析平台查询参数（oci-sync 移植）。
func parseInspectPlatformQuery(c *gin.Context) (*image.PlatformInfo, error) {
	osName := strings.TrimSpace(strings.ToLower(c.Query("os")))
	architecture := strings.TrimSpace(strings.ToLower(c.Query("architecture")))
	variant := strings.TrimSpace(strings.ToLower(c.Query("variant")))

	if osName == "" {
		return nil, fmt.Errorf("query parameter 'os' is required")
	}
	if architecture == "" {
		return nil, fmt.Errorf("query parameter 'architecture' is required")
	}

	return &image.PlatformInfo{
		OS:           osName,
		Architecture: architecture,
		Variant:      variant,
	}, nil
}

// InspectImage GET /images/inspect?image=&registry=。
func (h *Handlers) InspectImage(c *gin.Context) {
	input := strings.TrimSpace(c.Query("image"))
	if input == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query parameter 'image' is required"})
		return
	}
	result, err := h.svc.InspectImage(c.Request.Context(), input, nil, c.Query("registry"))
	if err != nil {
		respondOciError(c, err)
		return
	}
	c.JSON(http.StatusOK, imageInspectResponse{Image: result.Ref, Result: result})
}

// InspectImagePlatform GET /images/inspect/platform?image=&os=&architecture=&variant=。
func (h *Handlers) InspectImagePlatform(c *gin.Context) {
	input := strings.TrimSpace(c.Query("image"))
	if input == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query parameter 'image' is required"})
		return
	}
	platform, err := parseInspectPlatformQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	result, err := h.svc.InspectImage(c.Request.Context(), input, platform, c.Query("registry"))
	if err != nil {
		respondOciError(c, err)
		return
	}
	c.JSON(http.StatusOK, imagePlatformInspectResponse{Image: result.Ref, Platform: *platform, Result: result})
}

// ---------------------------------------------------------------------------
// 远端 discovery（/remote/*）
// ---------------------------------------------------------------------------

// remoteOptions 解析 limit / scan_limit / q / cursor 查询参数
// （oci-sync remote_handlers 移植）。
func remoteOptions(c *gin.Context) (registry.SearchInput, bool) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 || limit > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_input", "error": "limit must be an integer in [1, 100]"})
		return registry.SearchInput{}, false
	}
	scan, err := strconv.Atoi(c.DefaultQuery("scan_limit", "1000"))
	if err != nil || scan < 1 || scan > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_input", "error": "scan_limit must be an integer in [1, 5000]"})
		return registry.SearchInput{}, false
	}
	return registry.SearchInput{Query: c.Query("q"), PageSize: limit, ScanLimit: scan, Cursor: c.Query("cursor")}, true
}

// SearchRemoteRepositories GET /remote/repositories?registry=。
func (h *Handlers) SearchRemoteRepositories(c *gin.Context) {
	options, ok := remoteOptions(c)
	if !ok {
		return
	}
	page, err := h.svc.SearchRemoteRepositories(c.Request.Context(), c.Query("registry"), options)
	if err != nil {
		respondOciError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

// ListRemoteTags GET /remote/tags?repository=。
func (h *Handlers) ListRemoteTags(c *gin.Context) {
	options, ok := remoteOptions(c)
	if !ok {
		return
	}
	page, err := h.svc.ListRemoteTags(c.Request.Context(), c.Query("repository"), options, c.Query("registry"))
	if err != nil {
		respondOciError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

// ---------------------------------------------------------------------------
// 错误映射
// ---------------------------------------------------------------------------

// respondOciError 把领域错误映射为 REST 状态码：Registry CRUD 哨兵与
// 查询分类错误（oci-sync respondControlError 的映射移植——
// DiscoveryError 码 → unauthorized / forbidden / not_found /
// catalog_unsupported / rate_limited / unavailable）。
func respondOciError(c *gin.Context, err error) {
	var queryErr *Error
	if errors.As(err, &queryErr) {
		status := http.StatusInternalServerError
		switch queryErr.Code {
		case "invalid_input", "invalid_cursor", "registry_not_configured", "registry_ambiguous":
			status = http.StatusBadRequest
		case "registry_not_found", "platform_not_found":
			status = http.StatusNotFound
		case "registry_catalog_unsupported", "registry_operation_unsupported":
			status = http.StatusNotImplemented
		case "registry_unauthorized":
			status = http.StatusUnauthorized
		case "registry_forbidden":
			status = http.StatusForbidden
		case "registry_rate_limited":
			status = http.StatusTooManyRequests
		case "registry_unavailable":
			status = http.StatusBadGateway
		}
		c.JSON(status, gin.H{"error": queryErr.Message, "code": queryErr.Code, "retryable": queryErr.Retryable})
		return
	}
	switch {
	case errors.Is(err, ErrRegistryNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "registry not found"})
	case errors.Is(err, ErrRegistryConflict), errors.Is(err, ErrMirrorConflict):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, ErrRegistryInUse):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, ErrRegistryInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}
