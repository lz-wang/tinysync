package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// registerJobRoutes 注册 Sync Job 管理、手动运行与同步历史端点。svc 为
// nil 时跳过注册（依赖缺失时由未知路径 404 兜底）；runner 为 nil 时只
// 注册 CRUD。
func registerJobRoutes(group *gin.RouterGroup, svc *syncjob.Service, runner *syncjob.Runner) {
	if svc == nil {
		return
	}
	h := &jobHandlers{svc: svc, runner: runner}
	// 权限矩阵：查询 read；创建 / 更新 / 删除 admin；手动触发 run
	//（run 不含 read）。
	group.GET("/jobs", requireScope(auth.ScopeRead), h.list)
	group.GET("/jobs/local-directories", requireScope(auth.ScopeAdmin), h.listLocalDirectories)
	group.POST("/jobs/local-directories", requireScope(auth.ScopeAdmin), h.createLocalDirectory)
	group.POST("/jobs", requireScope(auth.ScopeAdmin), h.create)
	group.GET("/jobs/:id", requireScope(auth.ScopeRead), h.get)
	group.PATCH("/jobs/:id", requireScope(auth.ScopeAdmin), h.update)
	group.DELETE("/jobs/:id", requireScope(auth.ScopeAdmin), h.delete)
	if runner != nil {
		group.POST("/jobs/:id/run", requireScope(auth.ScopeRun), h.run)
		group.GET("/jobs/:id/status", requireScope(auth.ScopeRead), h.status)
		group.GET("/runs", requireScope(auth.ScopeRead), h.listRuns)
		group.GET("/runs/:id", requireScope(auth.ScopeRead), h.getRun)
		group.GET("/runs/:id/items", requireScope(auth.ScopeRead), h.listRunItems)
		group.POST("/runs/:id/cancel", requireScope(auth.ScopeRun), h.cancelRun)
	}
}

// jobHandlers 是 Sync Job 端点的 handler 集合。
type jobHandlers struct {
	svc    *syncjob.Service
	runner *syncjob.Runner
}

type localDirectoryDTO struct {
	Path string `json:"path"`
}

type localDirectoriesDTO struct {
	Path        string              `json:"path"`
	Directories []localDirectoryDTO `json:"directories"`
}

type createLocalDirectoryRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// listLocalDirectories 列出 TinySync 所在主机上的直接子目录，供管理员在
// 创建 Job 时选择 LocalRoot。空 path 默认从运行用户 Home 目录开始；只返回
// 目录，且不跟随子项 symlink。hidden=true 时包含点开头的目录。
func (h *jobHandlers) listLocalDirectories(c *gin.Context) {
	requested := c.Query("path")
	if requested == "" {
		var err error
		requested, err = os.UserHomeDir()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "resolve user home directory"})
			return
		}
	}
	abs, err := filepath.Abs(requested)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("resolve local directory: %v", err)})
		return
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("resolve local directory: %v", err)})
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "local directory does not exist or is not a directory"})
		return
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("read local directory: %v", err)})
		return
	}
	showHidden, err := strconv.ParseBool(c.DefaultQuery("hidden", "false"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hidden must be a boolean"})
		return
	}
	directories := make([]localDirectoryDTO, 0)
	for _, entry := range entries {
		if entry.IsDir() && (showHidden || !strings.HasPrefix(entry.Name(), ".")) {
			directories = append(directories, localDirectoryDTO{Path: filepath.Join(resolved, entry.Name())})
		}
	}
	c.JSON(http.StatusOK, localDirectoriesDTO{Path: resolved, Directories: directories})
}

// createLocalDirectory 在管理员当前浏览的目录中建立直接子目录。名称只允许
// 单个路径段，防止请求绕过目录选择器在其他位置创建目录。
func (h *jobHandlers) createLocalDirectory(c *gin.Context) {
	var req createLocalDirectoryRequest
	if !strictBind(c, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder name must be a single non-empty path segment"})
		return
	}
	parent, err := filepath.Abs(req.Path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("resolve local directory: %v", err)})
		return
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("resolve local directory: %v", err)})
		return
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "local directory does not exist or is not a directory"})
		return
	}
	created := filepath.Join(parent, name)
	if err := os.Mkdir(created, 0o755); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("create local directory: %v", err)})
		return
	}
	c.JSON(http.StatusCreated, localDirectoryDTO{Path: created})
}

// scheduleDTO 是调度配置的 discriminated object：按 type 消费互斥字段，
// 不暴露 nullable 平铺字段。once 用 at（RFC3339）、interval 用 every
// （Go duration）、cron 用 expression + timezone（IANA，缺省机器本地时区）。
type scheduleDTO struct {
	Type       string `json:"type"`
	At         string `json:"at,omitempty"`
	Every      string `json:"every,omitempty"`
	Expression string `json:"expression,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
}

// toScheduleDTO 转换领域调度配置；anchor 等内部字段不输出。
func toScheduleDTO(s syncjob.Schedule) *scheduleDTO {
	out := &scheduleDTO{Type: string(s.Type)}
	switch s.Type {
	case syncjob.ScheduleOnce:
		out.At = s.Value
	case syncjob.ScheduleInterval:
		out.Every = s.Value
	case syncjob.ScheduleCron:
		out.Expression = s.Value
		out.Timezone = s.Timezone
	}
	return out
}

// scheduleFromDTO 把请求体中的调度配置转为领域对象；nil 透传表示
// 「未提供」。discriminated union 严格校验：不属于该类型的互斥字段
// 一律报错——拼错类型或多传字段必须显式暴露，而不是被静默丢弃。
// 字段取值合法性仍由 Service 校验。
func scheduleFromDTO(dto *scheduleDTO) (*syncjob.Schedule, error) {
	if dto == nil {
		return nil, nil
	}
	hasAt := dto.At != ""
	hasEvery := dto.Every != ""
	hasExpression := dto.Expression != ""
	hasTimezone := dto.Timezone != ""
	reject := func(reason string) (*syncjob.Schedule, error) {
		return nil, fmt.Errorf("schedule %q: %s", dto.Type, reason)
	}
	s := syncjob.Schedule{Type: syncjob.ScheduleType(dto.Type)}
	switch s.Type {
	case syncjob.ScheduleManual:
		if hasAt || hasEvery || hasExpression || hasTimezone {
			return reject("manual schedule takes no scheduling fields")
		}
	case syncjob.ScheduleOnce:
		if !hasAt {
			return reject("once schedule requires \"at\" (RFC3339)")
		}
		if hasEvery || hasExpression || hasTimezone {
			return reject("once schedule takes no every / expression / timezone")
		}
		s.Value = dto.At
	case syncjob.ScheduleInterval:
		if !hasEvery {
			return reject("interval schedule requires \"every\" (Go duration)")
		}
		if hasAt || hasExpression || hasTimezone {
			return reject("interval schedule takes no at / expression / timezone")
		}
		s.Value = dto.Every
	case syncjob.ScheduleCron:
		if !hasExpression {
			return reject("cron schedule requires \"expression\" (5-field)")
		}
		if hasAt || hasEvery {
			return reject("cron schedule takes no at / every")
		}
		s.Value = dto.Expression
		s.Timezone = dto.Timezone
	default:
		return reject("unknown schedule type")
	}
	return &s, nil
}

// jobDTO 是 Sync Job 的 API 表示。Include / Exclude 恒为数组（nil 归一），
// schedule 恒输出（manual 表示仅手动触发）。
type jobDTO struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	SourceID   string       `json:"source_id"`
	RemoteRoot string       `json:"remote_root"`
	LocalRoot  string       `json:"local_root"`
	Mode       string       `json:"mode"`
	Include    []string     `json:"include"`
	Exclude    []string     `json:"exclude"`
	Enabled    bool         `json:"enabled"`
	Schedule   *scheduleDTO `json:"schedule"`
	CreatedAt  string       `json:"created_at"`
	UpdatedAt  string       `json:"updated_at"`
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
		Schedule:   toScheduleDTO(j.Schedule),
		CreatedAt:  j.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  j.UpdatedAt.Format(time.RFC3339),
	}
}

// createJobRequest 是创建请求体；Enabled 缺省为 true，schedule 缺省为
// manual。
type createJobRequest struct {
	Name       string       `json:"name"`
	SourceID   string       `json:"source_id"`
	RemoteRoot string       `json:"remote_root"`
	LocalRoot  string       `json:"local_root"`
	Mode       string       `json:"mode"`
	Include    []string     `json:"include"`
	Exclude    []string     `json:"exclude"`
	Enabled    *bool        `json:"enabled"`
	Schedule   *scheduleDTO `json:"schedule"`
}

// updateJobRequest 是更新请求体：nil 字段保留现有值；Include / Exclude
// 提供 null 时同样保留，提供数组时整体替换；schedule 提供时原子替换。
type updateJobRequest struct {
	Name       *string      `json:"name"`
	SourceID   *string      `json:"source_id"`
	RemoteRoot *string      `json:"remote_root"`
	LocalRoot  *string      `json:"local_root"`
	Mode       *string      `json:"mode"`
	Include    *[]string    `json:"include"`
	Exclude    *[]string    `json:"exclude"`
	Enabled    *bool        `json:"enabled"`
	Schedule   *scheduleDTO `json:"schedule"`
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

// runStatusDTO 是运行状态快照：state 取最近一条持久化 run（含 skipped），
// 重启后不再回到 idle。next_run_at 为下一次计划触发时间（RFC3339），
// manual 或 once 已消费时省略。idle 时 run_id 与时间戳为空；
// stats 恒输出，便于前端按稳定结构渲染。
type runStatusDTO struct {
	RunID      string      `json:"run_id,omitempty"`
	State      string      `json:"state"`
	StartedAt  string      `json:"started_at,omitempty"`
	FinishedAt string      `json:"finished_at,omitempty"`
	NextRunAt  string      `json:"next_run_at,omitempty"`
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
	schedule, err := scheduleFromDTO(req.Schedule)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
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
		Schedule:   schedule,
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

// update PATCH /api/v1/jobs/:id。通过 Runner 协调位与执行链原子互斥
// （运行中或正在启动的 Job 拒绝修改）：旧 mapping 的传输可能仍在推进
// metadata，与配置变更交叉会产生状态竞争。
func (h *jobHandlers) update(c *gin.Context) {
	id := c.Param("id")
	if h.runner != nil {
		if err := h.runner.BeginMutation(id); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "sync job is running"})
			return
		}
		defer h.runner.EndMutation(id)
	}
	var req updateJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	schedule, err := scheduleFromDTO(req.Schedule)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		Schedule:   schedule,
	}
	if req.Mode != nil {
		mode := syncjob.Mode(*req.Mode)
		input.Mode = &mode
	}
	updated, err := h.svc.Update(c.Request.Context(), id, input)
	if err != nil {
		handleJobError(c, err)
		return
	}
	c.JSON(http.StatusOK, toJobDTO(updated))
}

// delete DELETE /api/v1/jobs/:id。只删除 Job 配置与 managed metadata，
// 真实本地文件永远保留；通过 Runner 协调位与执行链原子互斥，运行中
// 或正在启动的 Job 拒绝删除，避免进行中的传输向已删除的 Job 登记
// metadata。
func (h *jobHandlers) delete(c *gin.Context) {
	id := c.Param("id")
	if h.runner != nil {
		if err := h.runner.BeginMutation(id); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "sync job is running"})
			return
		}
		defer h.runner.EndMutation(id)
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
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

// status GET /api/v1/jobs/:id/status。数据源为持久化运行历史
// （进行中的运行优先），重启后最近一次运行仍可查询；附带 next_run_at。
func (h *jobHandlers) status(c *gin.Context) {
	if h.runner == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	id := c.Param("id")
	job, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		handleJobError(c, err)
		return
	}
	status, err := h.runner.GetStatus(c.Request.Context(), id)
	if err != nil {
		handleRunError(c, err)
		return
	}
	dto := toRunStatusDTO(status)
	if next, ok, err := h.runner.NextRunAt(c.Request.Context(), job); err != nil {
		handleRunError(c, err)
		return
	} else if ok {
		dto.NextRunAt = next.Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, dto)
}

// runsDefaultLimit / runsMaxLimit 是历史列表分页参数。
const (
	runsDefaultLimit = 50
	runsMaxLimit     = 200
)

// runProgressFileDTO 是一个在途文件的实时进度（字节计数由前端换算
// 百分比与显示文本，后端只输出原始计数）。
type runProgressFileDTO struct {
	Path       string `json:"path"`
	Action     string `json:"action"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
}

// runProgressDTO 是运行中 run 的实时进度：phase 为生命周期阶段
// （connecting / scanning / planning / transferring / finalizing），
// work_done / work_total 为计划工作项计数（transferring 起可用），
// active_files 为在途传输。瞬态内存快照：终态 run 与进程重启后省略。
type runProgressDTO struct {
	Phase       string               `json:"phase"`
	WorkDone    int64                `json:"work_done"`
	WorkTotal   int64                `json:"work_total"`
	ActiveFiles []runProgressFileDTO `json:"active_files"`
}

func toRunProgressDTO(snap syncjob.RunProgressSnapshot) *runProgressDTO {
	dto := &runProgressDTO{
		Phase:     string(snap.Phase),
		WorkDone:  snap.WorkDone,
		WorkTotal: snap.WorkTotal,
	}
	if len(snap.ActiveFiles) > 0 {
		dto.ActiveFiles = make([]runProgressFileDTO, 0, len(snap.ActiveFiles))
		for _, f := range snap.ActiveFiles {
			dto.ActiveFiles = append(dto.ActiveFiles, runProgressFileDTO{
				Path:       f.Path,
				Action:     f.Action,
				BytesDone:  f.BytesDone,
				BytesTotal: f.BytesTotal,
			})
		}
	}
	return dto
}

// runDTO 是一轮运行的 API 表示；job_name 冗余输出便于全局历史渲染。
type runDTO struct {
	ID           string      `json:"id"`
	JobID        string      `json:"job_id"`
	JobName      string      `json:"job_name"`
	Trigger      string      `json:"trigger"`
	ScheduledFor string      `json:"scheduled_for,omitempty"`
	Status       string      `json:"status"`
	StartedAt    string      `json:"started_at"`
	FinishedAt   string      `json:"finished_at,omitempty"`
	Stats        runStatsDTO `json:"stats"`
	Error        string      `json:"error,omitempty"`
	// Progress 仅运行中的 run 输出：内存瞬态快照，终态省略——
	// SQLite 历史始终是运行事实的唯一来源。
	Progress *runProgressDTO `json:"progress,omitempty"`
}

// toRunDTO 转换运行记录，时间输出 RFC3339。
func toRunDTO(run syncjob.RunRecord, jobName string) runDTO {
	dto := runDTO{
		ID:        run.ID,
		JobID:     run.JobID,
		JobName:   jobName,
		Trigger:   string(run.Trigger),
		Status:    string(run.State),
		StartedAt: run.StartedAt.Format(time.RFC3339),
		Error:     run.Error,
		Stats: runStatsDTO{
			FilesTotal:       run.Stats.FilesTotal,
			FilesCreated:     run.Stats.FilesCreated,
			FilesUpdated:     run.Stats.FilesUpdated,
			FilesDeleted:     run.Stats.FilesDeleted,
			FilesSkipped:     run.Stats.FilesSkipped,
			BytesTransferred: run.Stats.BytesTransferred,
		},
	}
	if run.ScheduledFor != nil {
		dto.ScheduledFor = run.ScheduledFor.Format(time.RFC3339)
	}
	if run.FinishedAt != nil {
		dto.FinishedAt = run.FinishedAt.Format(time.RFC3339)
	}
	return dto
}

// runItemDTO 是文件级变更明细的 API 表示。
type runItemDTO struct {
	ID     int64  `json:"id"`
	RunID  string `json:"run_id"`
	Path   string `json:"path"`
	Action string `json:"action"`
	Status string `json:"status"`
	Bytes  int64  `json:"bytes"`
	Error  string `json:"error,omitempty"`
}

// toRunItemDTO 转换明细记录。
func toRunItemDTO(item syncjob.RunItem) runItemDTO {
	return runItemDTO{
		ID:     item.ID,
		RunID:  item.RunID,
		Path:   item.Path,
		Action: string(item.Action),
		Status: string(item.Status),
		Bytes:  item.Bytes,
		Error:  item.Error,
	}
}

// jobNames 一次性取回全部 Job 名（HomeLab 规模下成本可忽略），
// 供运行历史冗余 job_name 使用。
func (h *jobHandlers) jobNames(ctx context.Context) map[string]string {
	jobs, err := h.svc.List(ctx)
	if err != nil {
		return nil
	}
	names := make(map[string]string, len(jobs))
	for _, j := range jobs {
		names[j.ID] = j.Name
	}
	return names
}

// listRuns GET /api/v1/runs。全局运行历史：支持 job_id / status 过滤与
// limit / offset 分页，按开始时间倒序，total 为过滤后总数。
func (h *jobHandlers) listRuns(c *gin.Context) {
	if h.runner == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	filter := syncjob.RunFilter{}
	if jobID := c.Query("job_id"); jobID != "" {
		filter.JobID = jobID
	}
	if status := c.Query("status"); status != "" {
		switch syncjob.RunState(status) {
		case syncjob.RunRunning, syncjob.RunSucceeded, syncjob.RunFailed, syncjob.RunSkipped, syncjob.RunCanceled:
			filter.Status = syncjob.RunState(status)
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status filter"})
			return
		}
	}
	limit := runsDefaultLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > runsMaxLimit {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("limit must be an integer in [1, %d]", runsMaxLimit)})
			return
		}
		limit = n
	}
	offset := 0
	if raw := c.Query("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be a non-negative integer"})
			return
		}
		offset = n
	}
	filter.Limit = limit
	filter.Offset = offset

	runs, total, err := h.runner.ListRuns(c.Request.Context(), filter)
	if err != nil {
		handleRunError(c, err)
		return
	}
	names := h.jobNames(c.Request.Context())
	dtos := make([]runDTO, 0, len(runs))
	for _, run := range runs {
		dtos = append(dtos, toRunDTO(run, names[run.JobID]))
	}
	c.JSON(http.StatusOK, gin.H{"runs": dtos, "total": total})
}

// getRun GET /api/v1/runs/:id。运行摘要；不存在返回 404。运行中的
// run 合并内存实时进度（progress 字段）：phase / 工作量计数 / 在途
// 文件字节。终态 run（或进程重启后遗留 running 已被启动恢复收敛）
// 不输出 progress——SQLite 历史是运行事实的唯一来源。前端以
// 500ms～1s 轮询本端点消费进度。
func (h *jobHandlers) getRun(c *gin.Context) {
	if h.runner == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	run, err := h.runner.GetRun(c.Request.Context(), c.Param("id"))
	if err != nil {
		handleRunError(c, err)
		return
	}
	names := h.jobNames(c.Request.Context())
	dto := toRunDTO(run, names[run.JobID])
	if run.State == syncjob.RunRunning {
		if snap, ok := h.runner.Progress(run.ID); ok {
			dto.Progress = toRunProgressDTO(snap)
		}
	}
	c.JSON(http.StatusOK, dto)
}

// cancelRun POST /api/v1/runs/:id/cancel。手动停止进行中的运行：取消
// 异步生效（取消链中断拨号 / 扫描 / 传输），立即返回 202；仍 active 的
// 重复取消幂等 202（cancel 本身幂等，前端竞态无需特殊处理）。run 已
// 终态返回 409 并附当前状态；不存在返回 404。scope 为 run（与触发运行
// 同级），不要求 admin。
func (h *jobHandlers) cancelRun(c *gin.Context) {
	if h.runner == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	runID := c.Param("id")
	err := h.runner.Cancel(runID)
	if err == nil {
		c.JSON(http.StatusAccepted, gin.H{"run_id": runID, "state": string(syncjob.RunRunning)})
		return
	}
	if !errors.Is(err, syncjob.ErrRunNotActive) {
		handleRunError(c, err)
		return
	}
	// 不在进行中：结合持久化历史区分 404（不存在）与 409（已终态）。
	rec, getErr := h.runner.GetRun(c.Request.Context(), runID)
	if getErr != nil {
		handleRunError(c, getErr)
		return
	}
	c.JSON(http.StatusConflict, gin.H{
		"error":  "run already finished",
		"run_id": runID,
		"state":  string(rec.State),
	})
}

// listRunItems GET /api/v1/runs/:id/items。文件级变更明细，分页参数与
// /runs 一致。
func (h *jobHandlers) listRunItems(c *gin.Context) {
	if h.runner == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	runID := c.Param("id")
	limit := runsDefaultLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > runsMaxLimit {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("limit must be an integer in [1, %d]", runsMaxLimit)})
			return
		}
		limit = n
	}
	offset := 0
	if raw := c.Query("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be a non-negative integer"})
			return
		}
		offset = n
	}
	items, total, err := h.runner.ListRunItems(c.Request.Context(), runID, limit, offset)
	if err != nil {
		handleRunError(c, err)
		return
	}
	dtos := make([]runItemDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toRunItemDTO(item))
	}
	c.JSON(http.StatusOK, gin.H{"items": dtos, "total": total})
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
// 202 之外的分支有 404（Job / run 不存在）、409（禁用、引用缺失、
// 占用中、并发已满）与 503（Runner 正在关闭）。
func handleRunError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, syncjob.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "sync job not found"})
	case errors.Is(err, syncjob.ErrRunUnknown):
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
	case errors.Is(err, syncjob.ErrJobDisabled):
		c.JSON(http.StatusConflict, gin.H{"error": "sync job is disabled"})
	case errors.Is(err, syncjob.ErrSourceDisabled):
		c.JSON(http.StatusConflict, gin.H{"error": "source is disabled"})
	case errors.Is(err, syncjob.ErrRunActive):
		c.JSON(http.StatusConflict, gin.H{"error": "another sync run is active"})
	case errors.Is(err, syncjob.ErrJobMutating):
		c.JSON(http.StatusConflict, gin.H{"error": "sync job is being modified"})
	case errors.Is(err, syncjob.ErrConcurrencyLimit):
		c.JSON(http.StatusConflict, gin.H{"error": "concurrency limit reached"})
	case errors.Is(err, syncjob.ErrShuttingDown):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sync runner is shutting down"})
	case errors.Is(err, source.ErrNotFound):
		c.JSON(http.StatusConflict, gin.H{"error": "source does not exist"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}
