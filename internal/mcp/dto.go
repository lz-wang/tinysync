package mcp

import (
	"fmt"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// MCP DTO 层：冻结 MCP wire contract，不直接输出 domain object。
// secret / raw token 永不出现在这里列出的任何结构中；时间输出与
// REST 一致（RFC3339）。

// sourceSummary 是 list_sources 的条目：非敏感 config 与凭据状态
// 布尔集合，与 REST sourceDTO 同一披露边界。Config 用 any 承载
// 按协议变化的 JSON 对象（schema 侧保持宽松）。
type sourceSummary struct {
	ID              string                 `json:"id"`
	Name            string                 `json:"name"`
	Type            string                 `json:"type"`
	Config          any                    `json:"config"`
	CredentialState source.CredentialState `json:"credential_state"`
	Enabled         bool                   `json:"enabled"`
}

// listSourcesResult 是 list_sources 的输出。
type listSourcesResult struct {
	Sources []sourceSummary `json:"sources"`
	Total   int             `json:"total"`
	Offset  int             `json:"offset"`
}

// toSourceSummary 转换领域对象；config 按协议序列化（非敏感字段）。
func toSourceSummary(s source.Source) (sourceSummary, error) {
	var config any
	switch s.Type {
	case source.TypeWebDAV:
		config = s.Config.WebDAV
	case source.TypeS3:
		config = s.Config.S3
	case source.TypeSFTP:
		config = s.Config.SFTP
	case source.TypeGitHubRelease:
		config = s.Config.GitHubRelease
	default:
		return sourceSummary{}, fmt.Errorf("unsupported source type %q", s.Type)
	}
	return sourceSummary{
		ID:              s.ID,
		Name:            s.Name,
		Type:            string(s.Type),
		Config:          config,
		CredentialState: s.CredentialState,
		Enabled:         s.Enabled,
	}, nil
}

// jobSummary 是 list_jobs 的条目。
type jobSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	SourceID string `json:"source_id"`
	Mode     string `json:"mode"`
	Enabled  bool   `json:"enabled"`
}

// listJobsResult 是 list_jobs 的输出。
type listJobsResult struct {
	Jobs   []jobSummary `json:"jobs"`
	Total  int          `json:"total"`
	Offset int          `json:"offset"`
}

// jobDetail 是 get_job 的输出：完整只读配置，字段与 REST jobDTO 一致。
type jobDetail struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	SourceID   string          `json:"source_id"`
	RemoteRoot string          `json:"remote_root"`
	LocalRoot  string          `json:"local_root"`
	Mode       string          `json:"mode"`
	Include    []string        `json:"include"`
	Exclude    []string        `json:"exclude"`
	Enabled    bool            `json:"enabled"`
	Schedule   *scheduleDetail `json:"schedule"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

// scheduleDetail 是调度配置的只读展示（discriminated union）。
type scheduleDetail struct {
	Type       string `json:"type"`
	At         string `json:"at,omitempty"`
	Every      string `json:"every,omitempty"`
	Expression string `json:"expression,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
}

// toJobSummary 转换列表条目。
func toJobSummary(j syncjob.Job) jobSummary {
	return jobSummary{
		ID:       j.ID,
		Name:     j.Name,
		SourceID: j.SourceID,
		Mode:     string(j.Mode),
		Enabled:  j.Enabled,
	}
}

// toJobDetail 转换完整只读配置；Include / Exclude 恒为数组（nil 归一）。
func toJobDetail(j syncjob.Job) jobDetail {
	include := j.Include
	if include == nil {
		include = []string{}
	}
	exclude := j.Exclude
	if exclude == nil {
		exclude = []string{}
	}
	return jobDetail{
		ID:         j.ID,
		Name:       j.Name,
		SourceID:   j.SourceID,
		RemoteRoot: j.RemoteRoot,
		LocalRoot:  j.LocalRoot,
		Mode:       string(j.Mode),
		Include:    include,
		Exclude:    exclude,
		Enabled:    j.Enabled,
		Schedule:   toScheduleDetail(j.Schedule),
		CreatedAt:  j.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  j.UpdatedAt.Format(time.RFC3339),
	}
}

// toScheduleDetail 转换领域调度配置；anchor 等内部字段不输出。
func toScheduleDetail(s syncjob.Schedule) *scheduleDetail {
	out := &scheduleDetail{Type: string(s.Type)}
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
