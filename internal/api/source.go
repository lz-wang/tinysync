package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
	"tinysync/internal/credential"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// registerSourceRoutes 注册 Source 管理端点。svc 为 nil 时跳过注册
// （依赖缺失时由未知路径 404 兜底，避免生产静默降级之外的 panic）。
// jobs 非 nil 时启用引用保护：被 Job 引用的 Source 返回 409，
// 数据库层 FK RESTRICT 作为并发路径的兜底。credentials 非 nil 时启用
// 「提升为凭据」端点（编排凭据服务，见 promote）。
func registerSourceRoutes(group *gin.RouterGroup, svc *source.Service, jobs *syncjob.Service, credentials *credential.Service) {
	if svc == nil {
		return
	}
	h := &sourceHandlers{svc: svc, credentials: credentials}
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
	// 权限矩阵：查询 read；创建 / 更新 / 删除 / test / inspect（会以
	// secret 主动访问远端）admin。
	group.GET("/sources", requireScope(auth.ScopeRead), h.list)
	group.POST("/sources", requireScope(auth.ScopeAdmin), h.create)
	group.GET("/sources/:id", requireScope(auth.ScopeRead), h.get)
	group.PATCH("/sources/:id", requireScope(auth.ScopeAdmin), h.update)
	group.DELETE("/sources/:id", requireScope(auth.ScopeAdmin), h.delete)
	group.POST("/sources/:id/test", requireScope(auth.ScopeAdmin), h.test)
	// 提升为凭据：把已存内联私钥转存为命名凭据并改写引用（私钥不
	// 经手前端），编排凭据服务与源更新。
	group.POST("/sources/:id/promote-credential", requireScope(auth.ScopeAdmin), h.promote)
	// inspect 必须先于 :id 路由注册意图上无冲突（gin 的静态段优先），
	// 显式声明不持久化的创建前预览端点。
	group.POST("/sources/inspect", requireScope(auth.ScopeAdmin), h.inspect)
}

// sourceHandlers 是 Source 端点的 handler 集合。
type sourceHandlers struct {
	svc *source.Service
	// refGuard 校验 Source 是否被 Job 引用；nil 表示不启用保护。
	// 删除始终校验；remote identity 变更时校验（防止 Mirror Job 下轮
	// 连接到另一个合法远端后把全部 managed 文件误判为远端消失）。
	refGuard func(ctx context.Context, sourceID string) error
	// credentials 是凭据应用服务；非 nil 时启用「提升为凭据」端点。
	credentials *credential.Service
}

// sourceDTO 是 Source 的 API 表示：config 为非敏感协议配置单选组，
// credential_state 只回显各 secret 是否设置。任何 secret 都不出现在
// 响应中。
type sourceDTO struct {
	ID              string                 `json:"id"`
	Name            string                 `json:"name"`
	Type            string                 `json:"type"`
	Config          json.RawMessage        `json:"config"`
	CredentialState source.CredentialState `json:"credential_state"`
	Enabled         bool                   `json:"enabled"`
	CreatedAt       string                 `json:"created_at"`
	UpdatedAt       string                 `json:"updated_at"`
}

// toSourceDTO 转换领域对象，时间输出 RFC3339，config 按协议序列化。
func toSourceDTO(s source.Source) (sourceDTO, error) {
	configJSON, err := marshalConfigForResponse(s.Type, s.Config)
	if err != nil {
		return sourceDTO{}, err
	}
	return sourceDTO{
		ID:              s.ID,
		Name:            s.Name,
		Type:            string(s.Type),
		Config:          configJSON,
		CredentialState: s.CredentialState,
		Enabled:         s.Enabled,
		CreatedAt:       s.CreatedAt.Format(time.RFC3339),
		UpdatedAt:       s.UpdatedAt.Format(time.RFC3339),
	}, nil
}

// marshalConfigForResponse 把 typed config 序列化为响应 JSON。
func marshalConfigForResponse(t source.Type, c source.Config) (json.RawMessage, error) {
	var data any
	switch t {
	case source.TypeWebDAV:
		data = c.WebDAV
	case source.TypeS3:
		data = c.S3
	case source.TypeSFTP:
		data = c.SFTP
	case source.TypeGitHubRelease:
		data = c.GitHubRelease
	default:
		return nil, fmt.Errorf("unsupported source type %q", t)
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal source config: %w", err)
	}
	return b, nil
}

// createSourceRequest 是创建请求体；config / credentials 按 type
// 严格解码（拒绝未知字段与类型不符字段）。
type createSourceRequest struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Enabled     *bool           `json:"enabled"`
	Config      json.RawMessage `json:"config"`
	Credentials json.RawMessage `json:"credentials"`
}

// updateSourceRequest 是更新请求体：nil 字段保留现有值。Type 不支持
// 修改（携带 type 且与现有值不同返回 400）。config 出现即整个协议
// config 替换；credentials 组内 secret 三态（缺省保留、空串清除、
// 非空替换）。
type updateSourceRequest struct {
	Name        *string          `json:"name"`
	Type        *string          `json:"type"`
	Enabled     *bool            `json:"enabled"`
	Config      json.RawMessage  `json:"config"`
	Credentials *json.RawMessage `json:"credentials"`
}

// testResultDTO 是连接测试结果。连接失败也是成功完成的测试操作，
// 以 ok=false 表达；只有请求本身异常才走 REST 错误。
type testResultDTO struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// strictDecode 用 DisallowUnknownFields 严格解码一个 JSON 子对象，
// 拒绝未知字段（前端与后端契约拼写错误在 400 处直接暴露）；再次
// Decode 确认 payload 是单一 JSON 值，尾随数据一律拒绝。
func strictDecode(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return errors.New("payload is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("payload must contain exactly one JSON value")
	}
	return nil
}

// decodeConfigPayload 按 type 严格解码 config 子对象。
func decodeConfigPayload(t source.Type, raw json.RawMessage) (source.Config, error) {
	switch t {
	case source.TypeWebDAV:
		var c source.WebDAVConfig
		if err := strictDecode(raw, &c); err != nil {
			return source.Config{}, fmt.Errorf("invalid webdav config: %v", err)
		}
		return source.Config{WebDAV: &c}, nil
	case source.TypeS3:
		var c source.S3Config
		if err := strictDecode(raw, &c); err != nil {
			return source.Config{}, fmt.Errorf("invalid s3 config: %v", err)
		}
		return source.Config{S3: &c}, nil
	case source.TypeSFTP:
		var c source.SFTPConfig
		if err := strictDecode(raw, &c); err != nil {
			return source.Config{}, fmt.Errorf("invalid sftp config: %v", err)
		}
		return source.Config{SFTP: &c}, nil
	case source.TypeGitHubRelease:
		var c source.GitHubReleaseConfig
		if err := strictDecode(raw, &c); err != nil {
			return source.Config{}, fmt.Errorf("invalid github_release config: %v", err)
		}
		return source.Config{GitHubRelease: &c}, nil
	default:
		return source.Config{}, fmt.Errorf("unsupported source type %q", t)
	}
}

// decodeCredentialsPayload 按 type 严格解码 credentials 子对象为
// 完整凭据集合（创建用：字段为值语义）。
func decodeCredentialsPayload(t source.Type, raw json.RawMessage) (source.Credentials, error) {
	switch t {
	case source.TypeWebDAV:
		var p struct {
			Password *string `json:"password"`
		}
		if err := strictDecode(raw, &p); err != nil {
			return source.Credentials{}, fmt.Errorf("invalid webdav credentials: %v", err)
		}
		creds := source.Credentials{WebDAV: &source.WebDAVCredentials{}}
		if p.Password != nil {
			creds.WebDAV.Password = *p.Password
		}
		return creds, nil
	case source.TypeS3:
		var p struct {
			SecretKey *string `json:"secret_key"`
		}
		if err := strictDecode(raw, &p); err != nil {
			return source.Credentials{}, fmt.Errorf("invalid s3 credentials: %v", err)
		}
		creds := source.Credentials{S3: &source.S3Credentials{}}
		if p.SecretKey != nil {
			creds.S3.SecretKey = *p.SecretKey
		}
		return creds, nil
	case source.TypeSFTP:
		var p struct {
			Password             *string `json:"password"`
			PrivateKey           *string `json:"private_key"`
			PrivateKeyPassphrase *string `json:"private_key_passphrase"`
		}
		if err := strictDecode(raw, &p); err != nil {
			return source.Credentials{}, fmt.Errorf("invalid sftp credentials: %v", err)
		}
		creds := source.Credentials{SFTP: &source.SFTPCredentials{}}
		if p.Password != nil {
			creds.SFTP.Password = *p.Password
		}
		if p.PrivateKey != nil {
			creds.SFTP.PrivateKey = *p.PrivateKey
		}
		if p.PrivateKeyPassphrase != nil {
			creds.SFTP.PrivateKeyPassphrase = *p.PrivateKeyPassphrase
		}
		return creds, nil
	case source.TypeGitHubRelease:
		var p struct {
			Token *string `json:"token"`
		}
		if err := strictDecode(raw, &p); err != nil {
			return source.Credentials{}, fmt.Errorf("invalid github_release credentials: %v", err)
		}
		creds := source.Credentials{GitHubRelease: &source.GitHubReleaseCredentials{}}
		if p.Token != nil {
			creds.GitHubRelease.Token = *p.Token
		}
		return creds, nil
	default:
		return source.Credentials{}, fmt.Errorf("unsupported source type %q", t)
	}
}

// decodeCredentialsUpdatePayload 按 type 严格解码 credentials 子对象
// 为三态更新：nil 保留、空串清除、非空替换。
func decodeCredentialsUpdatePayload(t source.Type, raw json.RawMessage) (*source.CredentialsUpdate, error) {
	switch t {
	case source.TypeWebDAV:
		var p struct {
			Password *string `json:"password"`
		}
		if err := strictDecode(raw, &p); err != nil {
			return nil, fmt.Errorf("invalid webdav credentials: %v", err)
		}
		return &source.CredentialsUpdate{WebDAV: &source.WebDAVCredentialsUpdate{Password: p.Password}}, nil
	case source.TypeS3:
		var p struct {
			SecretKey *string `json:"secret_key"`
		}
		if err := strictDecode(raw, &p); err != nil {
			return nil, fmt.Errorf("invalid s3 credentials: %v", err)
		}
		return &source.CredentialsUpdate{S3: &source.S3CredentialsUpdate{SecretKey: p.SecretKey}}, nil
	case source.TypeSFTP:
		var p struct {
			Password             *string `json:"password"`
			PrivateKey           *string `json:"private_key"`
			PrivateKeyPassphrase *string `json:"private_key_passphrase"`
		}
		if err := strictDecode(raw, &p); err != nil {
			return nil, fmt.Errorf("invalid sftp credentials: %v", err)
		}
		return &source.CredentialsUpdate{SFTP: &source.SFTPCredentialsUpdate{
			Password:             p.Password,
			PrivateKey:           p.PrivateKey,
			PrivateKeyPassphrase: p.PrivateKeyPassphrase,
		}}, nil
	case source.TypeGitHubRelease:
		var p struct {
			Token *string `json:"token"`
		}
		if err := strictDecode(raw, &p); err != nil {
			return nil, fmt.Errorf("invalid github_release credentials: %v", err)
		}
		return &source.CredentialsUpdate{GitHubRelease: &source.GitHubReleaseCredentialsUpdate{
			Token: p.Token,
		}}, nil
	default:
		return nil, fmt.Errorf("unsupported source type %q", t)
	}
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
		dto, err := toSourceDTO(s)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		dtos = append(dtos, dto)
	}
	c.JSON(http.StatusOK, gin.H{"sources": dtos})
}

// strictBind 严格解码请求体：顶层同样拒绝未知字段，并确认请求体是
// 单一 JSON 值（尾随数据一律拒绝）；前端与后端契约拼写错误在 400
// 处直接暴露。
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

// create POST /api/v1/sources。
func (h *sourceHandlers) create(c *gin.Context) {
	var req createSourceRequest
	if !strictBind(c, &req) {
		return
	}
	t := source.Type(req.Type)
	config, err := decodeConfigPayload(t, req.Config)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var credentials source.Credentials
	if req.Credentials != nil {
		credentials, err = decodeCredentialsPayload(t, req.Credentials)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	created, err := h.svc.Create(c.Request.Context(), source.CreateInput{
		Name:        req.Name,
		Type:        t,
		Config:      config,
		Credentials: credentials,
		Enabled:     enabled,
	})
	if err != nil {
		handleSourceError(c, err)
		return
	}
	dto, err := toSourceDTO(created)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusCreated, dto)
}

// get GET /api/v1/sources/:id。
func (h *sourceHandlers) get(c *gin.Context) {
	s, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		handleSourceError(c, err)
		return
	}
	dto, err := toSourceDTO(s)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, dto)
}

// update PATCH /api/v1/sources/:id。Type 创建后不可变：请求携带 type
// 且与现有值不同返回 400。config 缺省保留，出现即整个协议 config
// 替换；credentials 组内 secret 三态（缺省保留、空串清除、非空替换），
// secret rotation 始终允许。config 中 remote identity 字段的变更与
// endpoint 一致受引用保护（防止 Mirror Job 下轮把既有 managed 文件
// 误判为远端消失）；更换远端的正确路径是新建 Source → Job 切换
// SourceID（触发原子 metadata 重置）。
func (h *sourceHandlers) update(c *gin.Context) {
	var req updateSourceRequest
	if !strictBind(c, &req) {
		return
	}
	id := c.Param("id")
	current, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		handleSourceError(c, err)
		return
	}
	if req.Type != nil && source.Type(*req.Type) != current.Type {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source type cannot be changed"})
		return
	}

	input := source.UpdateInput{
		Name:    req.Name,
		Enabled: req.Enabled,
	}
	if req.Config != nil {
		config, err := decodeConfigPayload(current.Type, req.Config)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// Remote identity 保护：身份字段按协议判定
		// （source.RemoteIdentityEqual），被 Job 引用时拒绝。
		if h.refGuard != nil {
			next := current
			next.Config = config
			if !source.RemoteIdentityEqual(current, next) {
				if err := h.refGuard(c.Request.Context(), id); err != nil {
					c.JSON(http.StatusConflict, gin.H{
						"error": "source remote identity cannot be changed while referenced by sync jobs",
					})
					return
				}
			}
		}
		input.Config = &config
	}
	if req.Credentials != nil {
		credsUpdate, err := decodeCredentialsUpdatePayload(current.Type, *req.Credentials)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		input.Credentials = credsUpdate
	}
	updated, err := h.svc.Update(c.Request.Context(), id, input)
	if err != nil {
		handleSourceError(c, err)
		return
	}
	dto, err := toSourceDTO(updated)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, dto)
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

// promoteCredentialRequest 是「提升为凭据」的请求体：只为新凭据起名。
type promoteCredentialRequest struct {
	Name string `json:"name"`
}

// promoteCredentialDTO 是提升结果的凭据摘要（secret 不回显）。
type promoteCredentialDTO struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Fingerprint   string `json:"fingerprint"`
	HasPassphrase bool   `json:"has_passphrase"`
}

// promote POST /api/v1/sources/:id/promote-credential。把源已存的
// 内联私钥转存为命名凭据并把源改写为引用：私钥明文在后端内部流转，
// 不经手前端。编排顺序——读内联凭据（校验资格）→ 解析建凭据 →
// 源改引用（service 强制清除内联 secret）。若建凭据成功而源更新
// 失败，遗留一条无引用凭据，可由用户安全删除。
func (h *sourceHandlers) promote(c *gin.Context) {
	if h.credentials == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "credential service is not configured"})
		return
	}
	var req promoteCredentialRequest
	if !strictBind(c, &req) {
		return
	}
	id := c.Param("id")

	// 资格校验：SFTP + private_key + 未引用（不存在 404，不合格 400）。
	eligible, err := h.svc.PromoteEligible(c.Request.Context(), id)
	if err != nil {
		handleSourceError(c, err)
		return
	}
	if !eligible {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source is not eligible for credential promotion"})
		return
	}
	inline, err := h.svc.InlineCredentialsForPromote(c.Request.Context(), id)
	if err != nil {
		handleSourceError(c, err)
		return
	}
	if inline.SFTP == nil || inline.SFTP.PrivateKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source has no inline private key"})
		return
	}

	// 解析（入口 fail-closed）并落库；指纹由凭据服务随创建派生。
	secret := credential.Secret{
		PrivateKey:           inline.SFTP.PrivateKey,
		PrivateKeyPassphrase: inline.SFTP.PrivateKeyPassphrase,
	}
	if _, err := credential.ParseSecret(secret); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	created, err := h.credentials.Create(c.Request.Context(), credential.CreateInput{
		Name:   req.Name,
		Type:   credential.TypeSSHKey,
		Secret: secret,
	})
	if err != nil {
		handleCredentialError(c, err)
		return
	}

	// 源改写为引用；service 层强制清除内联 secret（互斥不变量）。
	src, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		handleSourceError(c, err)
		return
	}
	cfg := *src.Config.SFTP
	cfg.CredentialID = created.ID
	updated, err := h.svc.Update(c.Request.Context(), id, source.UpdateInput{
		Config: &source.Config{SFTP: &cfg},
	})
	if err != nil {
		handleSourceError(c, err)
		return
	}
	updatedDTO, err := toSourceDTO(updated)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"source":     updatedDTO,
		"credential": promoteCredentialDTO{
			ID:            created.ID,
			Name:          created.Name,
			Fingerprint:   created.Fingerprint,
			HasPassphrase: created.HasPassphrase,
		},
	})
}

// inspectRequest 是创建前预览的请求体，语义按字段组合解释（均不持
// 久化）：
//   - source_id only → 已存配置 + 已存凭据；
//   - source_id + config → 提案配置 + 已存凭据（编辑表单微调后预览，
//     服务端沿用已存 Token，前端无需重新索取）；
//   - source_id + config + credentials.token → 提案配置 + 提案 Token；
//   - config only（type=github_release）→ 提案配置 + 匿名。
type inspectRequest struct {
	SourceID    string          `json:"source_id,omitempty"`
	Type        string          `json:"type,omitempty"`
	Config      json.RawMessage `json:"config,omitempty"`
	Credentials json.RawMessage `json:"credentials,omitempty"`
}

// inspectedAssetDTO 是预览结果的单个 Asset 概览。
type inspectedAssetDTO struct {
	Name            string `json:"name"`
	Size            int64  `json:"size"`
	DigestAvailable bool   `json:"digest_available"`
}

// inspectedReleaseDTO 是预览结果的版本概览。
type inspectedReleaseDTO struct {
	Tag         string              `json:"tag"`
	Name        string              `json:"name,omitempty"`
	Prerelease  bool                `json:"prerelease"`
	PublishedAt string              `json:"published_at"`
	Assets      []inspectedAssetDTO `json:"assets,omitempty"`
}

// inspectResultDTO 是预览的响应：发现失败也是成功完成的预览操作，
// 以 ok=false 表达。Token 明文与任何 secret 绝不出现在响应中。
type inspectResultDTO struct {
	OK         bool                  `json:"ok"`
	LatencyMS  int64                 `json:"latency_ms"`
	Error      string                `json:"error,omitempty"`
	Repository string                `json:"repository,omitempty"`
	Releases   []inspectedReleaseDTO `json:"releases,omitempty"`
}

// inspect POST /api/v1/sources/inspect。创建前/编辑态测试并预览：不
// 持久化任何值；仅 github_release 类型支持。config / credentials 与
// source_id 可组合：与 source_id 同传时作为提案覆盖项，服务端沿用
// 已存凭据（或以 credentials.token 临时覆盖）。
func (h *sourceHandlers) inspect(c *gin.Context) {
	var req inspectRequest
	if !strictBind(c, &req) {
		return
	}
	input := source.InspectInput{SourceID: req.SourceID}
	if req.SourceID == "" {
		if req.Type != "github_release" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "inspect requires source_id or type github_release"})
			return
		}
		if req.Config == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "inspect config is required"})
			return
		}
	}
	if req.Config != nil {
		config, err := decodeConfigPayload(source.TypeGitHubRelease, req.Config)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if config.GitHubRelease == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid github_release config"})
			return
		}
		input.GitHubConfig = config.GitHubRelease
	}
	if req.Credentials != nil {
		creds, err := decodeCredentialsPayload(source.TypeGitHubRelease, req.Credentials)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if creds.GitHubRelease != nil {
			input.Token = creds.GitHubRelease.Token
		}
	}
	result, err := h.svc.Inspect(c.Request.Context(), input)
	if err != nil {
		handleSourceError(c, err)
		return
	}
	resp := inspectResultDTO{
		OK:        result.OK,
		LatencyMS: result.LatencyMS,
		Error:     result.Error,
	}
	if result.Inspection != nil {
		resp.Repository = result.Inspection.Repository
		resp.Releases = make([]inspectedReleaseDTO, 0, len(result.Inspection.Releases))
		for _, rel := range result.Inspection.Releases {
			dto := inspectedReleaseDTO{
				Tag:         rel.Tag,
				Name:        rel.Name,
				Prerelease:  rel.Prerelease,
				PublishedAt: rel.PublishedAt.UTC().Format(time.RFC3339),
			}
			for _, a := range rel.Assets {
				dto.Assets = append(dto.Assets, inspectedAssetDTO{
					Name:            a.Name,
					Size:            a.Size,
					DigestAvailable: a.DigestAvailable,
				})
			}
			resp.Releases = append(resp.Releases, dto)
		}
	}
	c.JSON(http.StatusOK, resp)
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
