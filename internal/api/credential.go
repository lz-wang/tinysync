package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
	"tinysync/internal/credential"
)

// registerCredentialRoutes 注册凭据管理端点。凭据是管理面资源：读取
// 同样要求 admin——公钥指纹与引用关系也属于敏感管理信息，run-only
// 与 read scope token 一律不可见。svc 为 nil 时跳过注册（依赖缺失时
// 由未知路径 404 兜底）。
func registerCredentialRoutes(group *gin.RouterGroup, svc *credential.Service, refs credential.ReferenceIndex) {
	if svc == nil {
		return
	}
	h := &credentialHandlers{svc: svc, refs: refs}
	group.GET("/credentials", requireScope(auth.ScopeAdmin), h.list)
	group.POST("/credentials", requireScope(auth.ScopeAdmin), h.create)
	group.GET("/credentials/:id", requireScope(auth.ScopeAdmin), h.get)
	group.PATCH("/credentials/:id", requireScope(auth.ScopeAdmin), h.update)
	group.DELETE("/credentials/:id", requireScope(auth.ScopeAdmin), h.delete)
}

// credentialHandlers 是凭据端点的 handler 集合。
type credentialHandlers struct {
	svc *credential.Service
	// refs 提供引用计数回显；nil 时引用计数恒为 0（测试装配形态）。
	refs credential.ReferenceIndex
}

// credentialDTO 是凭据的 API 表示：指纹与口令状态回显，任何 secret
// 都不出现在响应中。
type credentialDTO struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Fingerprint   string `json:"fingerprint"`
	HasPassphrase bool   `json:"has_passphrase"`
	// ReferencedBy 是当前引用本凭据的同步源数量。
	ReferencedBy int    `json:"referenced_by"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// toCredentialDTO 转换领域对象，时间输出 RFC3339；refCount 由调用方
// 按引用索引提供。
func toCredentialDTO(c credential.Credential, refCount int) credentialDTO {
	return credentialDTO{
		ID:            c.ID,
		Name:          c.Name,
		Type:          string(c.Type),
		Fingerprint:   c.Fingerprint,
		HasPassphrase: c.HasPassphrase,
		ReferencedBy:  refCount,
		CreatedAt:     c.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     c.UpdatedAt.Format(time.RFC3339),
	}
}

// credentialSecretPayload 是 secret 请求体。创建：值语义；更新：nil
// 保留、非 nil 整体替换（无三态——私钥与口令一体）。
type credentialSecretPayload struct {
	PrivateKey           string `json:"private_key"`
	PrivateKeyPassphrase string `json:"private_key_passphrase"`
}

// createCredentialRequest 是创建请求体。
type createCredentialRequest struct {
	Name   string                   `json:"name"`
	Type   string                   `json:"type"`
	Secret *credentialSecretPayload `json:"secret"`
}

// updateCredentialRequest 是更新请求体：nil 字段保留现有值。
type updateCredentialRequest struct {
	Name   *string                  `json:"name"`
	Secret *credentialSecretPayload `json:"secret"`
}

// list GET /api/v1/credentials。引用计数一次查询批量回显；索引查询
// 失败与 refCount 同语义——计数只影响展示，按 0 降级，不阻断列表。
func (h *credentialHandlers) list(c *gin.Context) {
	creds, err := h.svc.List(c.Request.Context())
	if err != nil {
		handleCredentialError(c, err)
		return
	}
	counts := make(map[string]int, len(creds))
	if h.refs != nil {
		if all, err := h.refs.AllCredentialReferences(c.Request.Context()); err == nil {
			for id, refs := range all {
				counts[id] = len(refs)
			}
		}
	}
	dtos := make([]credentialDTO, 0, len(creds))
	for _, cr := range creds {
		dtos = append(dtos, toCredentialDTO(cr, counts[cr.ID]))
	}
	c.JSON(http.StatusOK, gin.H{"credentials": dtos})
}

// create POST /api/v1/credentials。secret 必填：ssh_key 无匿名形态。
func (h *credentialHandlers) create(c *gin.Context) {
	var req createCredentialRequest
	if !strictBind(c, &req) {
		return
	}
	if req.Secret == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "secret is required"})
		return
	}
	created, err := h.svc.Create(c.Request.Context(), credential.CreateInput{
		Name: req.Name,
		Type: credential.Type(req.Type),
		Secret: credential.Secret{
			PrivateKey:           req.Secret.PrivateKey,
			PrivateKeyPassphrase: req.Secret.PrivateKeyPassphrase,
		},
	})
	if err != nil {
		handleCredentialError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toCredentialDTO(created, 0))
}

// get GET /api/v1/credentials/:id。
func (h *credentialHandlers) get(c *gin.Context) {
	cr, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		handleCredentialError(c, err)
		return
	}
	c.JSON(http.StatusOK, toCredentialDTO(cr, h.refCount(c, cr.ID)))
}

// update PATCH /api/v1/credentials/:id。只改名不携带 secret 时原
// secret 保留；携带 secret 即整体替换并重新派生指纹。
func (h *credentialHandlers) update(c *gin.Context) {
	var req updateCredentialRequest
	if !strictBind(c, &req) {
		return
	}
	input := credential.UpdateInput{Name: req.Name}
	if req.Secret != nil {
		input.Secret = &credential.Secret{
			PrivateKey:           req.Secret.PrivateKey,
			PrivateKeyPassphrase: req.Secret.PrivateKeyPassphrase,
		}
	}
	updated, err := h.svc.Update(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		handleCredentialError(c, err)
		return
	}
	c.JSON(http.StatusOK, toCredentialDTO(updated, h.refCount(c, updated.ID)))
}

// delete DELETE /api/v1/credentials/:id。被同步源引用时以 409 拒绝
// 并回显引用源清单，先解绑再删（fail-closed）。
func (h *credentialHandlers) delete(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		handleCredentialError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// refCount 返回凭据的引用计数；refs 未装配或查询失败均按 0 处理
// （计数只影响展示，删除守卫在 Service 层独立执行）。
func (h *credentialHandlers) refCount(c *gin.Context, credentialID string) int {
	if h.refs == nil {
		return 0
	}
	refs, err := h.refs.SourcesReferencingCredential(c.Request.Context(), credentialID)
	if err != nil {
		return 0
	}
	return len(refs)
}

// handleCredentialError 把凭据领域错误映射为 HTTP 响应：invalid 400、
// not found 404、冲突（重名 / 被引用）409、未知 500。
func handleCredentialError(c *gin.Context, err error) {
	var inUse *credential.ErrInUse
	switch {
	case errors.As(err, &inUse):
		c.JSON(http.StatusConflict, gin.H{
			"error":   "credential is referenced by sources",
			"sources": inUse.Sources,
		})
	case errors.Is(err, credential.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
	case errors.Is(err, credential.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "credential name already exists"})
	case errors.Is(err, credential.ErrInvalid), errors.Is(err, credential.ErrUnsupportedType):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}
