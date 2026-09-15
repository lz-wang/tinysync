package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// registerJobRoutes 注册 Sync Job 管理与手动运行端点。svc 为 nil 时跳过
// 注册（依赖缺失时由未知路径 404 兜底）；runner 为 nil 时只注册 CRUD。
func registerJobRoutes(group *gin.RouterGroup, svc *syncjob.Service, runner *syncjob.Runner) {
	if svc == nil {
		return
	}
	h := &jobHandlers{svc: svc, runner: runner}
	group.GET("/jobs", h.list)
	group.POST("/jobs", h.create)
	group.GET("/jobs/:id", h.get)
	group.PATCH("/jobs/:id", h.update)
	group.DELETE("/jobs/:id", h.delete)
	if runner != nil {
		group.POST("/jobs/:id/run", h.run)
		group.GET("/jobs/:id/status", h.status)
	}
}

// jobHandlers 是 Sync Job 端点的 handler 集合。
type jobHandlers struct {
	svc    *syncjob.Service
	runner *syncjob.Runner
}

// jobDTO 是 Sync Job 的 API 表示。Include / Exclude 恒为数组（nil 归一）。
type jobDTO struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	SourceID   string   `json:"source_id"`
	RemoteRoot string   `json:"remote_root"`
	LocalRoot  string   `json:"local_root"`
	Mode       string   `json:"mode"`
	Include    []string `json:"include"`
	Exclude    []string `json:"exclude"`
	Enabled    bool     `json:"enabled"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
}

// toJobDTO 转换领域对象，时间输出 RFC3339。
func toJobDTO(j syncjob.Job) jobDTO {
	include := j.Include
	if include == nil {
		include = []string{}
	}
	exclude := j.Exclude
	if exclude == nil {
		exclude = []string{}
	}
	return jobDTO{
		ID:         j.ID,
		Name:       j.Name,
		SourceID:   j.SourceID,
		RemoteRoot: j.RemoteRoot,
		LocalRoot:  j.LocalRoot,
		Mode:       string(j.Mode),
		Include:    include,
		Exclude:    exclude,
		Enabled:    j.Enabled,
		CreatedAt:  j.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  j.UpdatedAt.Format(time.RFC3339),
	}
}

// createJobRequest 是创建请求体；Enabled 缺省为 true。
type createJobRequest struct {
	Name       string   `json:"name"`
	SourceID   string   `json:"source_id"`
	RemoteRoot string   `json:"remote_root"`
	LocalRoot  string   `json:"local_root"`
	Mode       string   `json:"mode"`
	Include    []string `json:"include"`
	Exclude    []string `json:"exclude"`
	Enabled    *bool    `json:"enabled"`
}

// updateJobRequest 是更新请求体：nil 字段保留现有值；Include / Exclude
// 提供 null 时同样保留，提供数组时整体替换。
type updateJobRequest struct {
	Name       *string   `json:"name"`
	SourceID   *string   `json:"source_id"`
	RemoteRoot *string   `json:"remote_root"`
	LocalRoot  *string   `json:"local_root"`
	Mode       *string   `json:"mode"`
	Include    *[]string `json:"include"`
	Exclude    *[]string `json:"exclude"`
	Enabled    *bool     `json:"enabled"`
}

// runStatsDTO 是一轮同步的统计摘要。
type runStatsDTO struct {
	FilesTotal       int   `json:"files_total"`
	FilesCreated     int   `json:"files_created"`
	FilesUpdated     int   `json:"files_updated"`
	FilesDeleted     int   `json:"files_deleted"`
	FilesSkipped     int   `json:"files_skipped"`
	BytesTransferred int64 `json:"bytes_transferred"`
}

// runStatusDTO 是运行状态快照。idle 时 run_id 与时间戳为空；
// stats 恒输出，便于前端按稳定结构渲染。
type runStatusDTO struct {
	RunID      string      `json:"run_id,omitempty"`
	State      string      `json:"state"`
	StartedAt  string      `json:"started_at,omitempty"`
	FinishedAt string      `json:"finished_at,omitempty"`
	Stats      runStatsDTO `json:"stats"`
	Error      string      `json:"error,omitempty"`
}

// toRunStatusDTO 转换运行状态，时间输出 RFC3339。
func toRunStatusDTO(s syncjob.RunStatus) runStatusDTO {
	dto := runStatusDTO{
		RunID: s.RunID,
		State: string(s.State),
		Error: s.Error,
		Stats: runStatsDTO{
			FilesTotal:       s.Stats.FilesTotal,
			FilesCreated:     s.Stats.FilesCreated,
			FilesUpdated:     s.Stats.FilesUpdated,
			FilesDeleted:     s.Stats.FilesDeleted,
			FilesSkipped:     s.Stats.FilesSkipped,
			BytesTransferred: s.Stats.BytesTransferred,
		},
	}
	if !s.StartedAt.IsZero() {
		dto.StartedAt = s.StartedAt.Format(time.RFC3339)
	}
	if !s.FinishedAt.IsZero() {
		dto.FinishedAt = s.FinishedAt.Format(time.RFC3339)
	}
	return dto
}

// list GET /api/v1/jobs。
func (h *jobHandlers) list(c *gin.Context) {
	jobs, err := h.svc.List(c.Request.Context())
	if err != nil {
		handleJobError(c, err)
		return
	}
	dtos := make([]jobDTO, 0, len(jobs))
	for _, j := range jobs {
		dtos = append(dtos, toJobDTO(j))
	}
	c.JSON(http.StatusOK, gin.H{"jobs": dtos})
}

// create POST /api/v1/jobs。
func (h *jobHandlers) create(c *gin.Context) {
	var req createJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	created, err := h.svc.Create(c.Request.Context(), syncjob.CreateInput{
		Name:       req.Name,
		SourceID:   req.SourceID,
		RemoteRoot: req.RemoteRoot,
		LocalRoot:  req.LocalRoot,
		Mode:       syncjob.Mode(req.Mode),
		Include:    req.Include,
		Exclude:    req.Exclude,
		Enabled:    enabled,
	})
	if err != nil {
		handleJobError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toJobDTO(created))
}

// get GET /api/v1/jobs/:id。
func (h *jobHandlers) get(c *gin.Context) {
	job, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		handleJobError(c, err)
		return
	}
	c.JSON(http.StatusOK, toJobDTO(job))
}

// update PATCH /api/v1/jobs/:id。
func (h *jobHandlers) update(c *gin.Context) {
	var req updateJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	input := syncjob.UpdateInput{
		Name:       req.Name,
		SourceID:   req.SourceID,
		RemoteRoot: req.RemoteRoot,
		LocalRoot:  req.LocalRoot,
		Include:    req.Include,
		Exclude:    req.Exclude,
		Enabled:    req.Enabled,
	}
	if req.Mode != nil {
		mode := syncjob.Mode(*req.Mode)
		input.Mode = &mode
	}
	updated, err := h.svc.Update(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		handleJobError(c, err)
		return
	}
	c.JSON(http.StatusOK, toJobDTO(updated))
}

// delete DELETE /api/v1/jobs/:id。只删除 Job 配置与 managed metadata，
// 真实本地文件永远保留。
func (h *jobHandlers) delete(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		handleJobError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// run POST /api/v1/jobs/:id/run。异步启动，立即返回 202 与 run ID；
// 运行 context 独立于本请求。
func (h *jobHandlers) run(c *gin.Context) {
	if h.runner == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	runID, err := h.runner.Start(c.Request.Context(), c.Param("id"))
	if err != nil {
		handleRunError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"run_id": runID, "state": string(syncjob.RunRunning)})
}

// status GET /api/v1/jobs/:id/status。运行记录只存内存，
// 进程重启后回到 idle。
func (h *jobHandlers) status(c *gin.Context) {
	if h.runner == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	id := c.Param("id")
	if _, err := h.svc.Get(c.Request.Context(), id); err != nil {
		handleJobError(c, err)
		return
	}
	status, err := h.runner.GetStatus(c.Request.Context(), id)
	if err != nil {
		handleRunError(c, err)
		return
	}
	c.JSON(http.StatusOK, toRunStatusDTO(status))
}

// handleJobError 把 Sync Job 领域错误映射为 REST 状态码。引用不存在的
// Source 属于请求体错误（400）；LocalRoot 重叠与 Source 删除保护是资源
// 状态冲突（409）。
func handleJobError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, syncjob.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "sync job not found"})
	case errors.Is(err, syncjob.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "sync job name already exists"})
	case errors.Is(err, syncjob.ErrRootOverlap), errors.Is(err, syncjob.ErrSourceInUse):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, syncjob.ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, source.ErrNotFound):
		c.JSON(http.StatusBadRequest, gin.H{"error": "source does not exist"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}

// handleRunError 把手动运行的领域错误映射为 REST 状态码：
// 202 之外的分支只有 404（Job 不存在）与 409（禁用、引用缺失、占用中）。
func handleRunError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, syncjob.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "sync job not found"})
	case errors.Is(err, syncjob.ErrJobDisabled):
		c.JSON(http.StatusConflict, gin.H{"error": "sync job is disabled"})
	case errors.Is(err, syncjob.ErrSourceDisabled):
		c.JSON(http.StatusConflict, gin.H{"error": "source is disabled"})
	case errors.Is(err, syncjob.ErrRunActive):
		c.JSON(http.StatusConflict, gin.H{"error": "another sync run is active"})
	case errors.Is(err, source.ErrNotFound):
		c.JSON(http.StatusConflict, gin.H{"error": "source does not exist"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}
