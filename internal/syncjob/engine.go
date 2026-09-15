package syncjob

import (
	"context"
	"fmt"
	"os"
	"time"

	"tinysync/internal/source"
)

// RunOptions 是一轮同步的输入。
type RunOptions struct {
	// Remote 是本轮使用的远端客户端（由 Source 配置构造）。
	Remote source.Remote
	// Job 是目标 Sync Job（LocalRoot / RemoteRoot / Mode / patterns）。
	Job Job
	// Managed 是 managed_files 持久化。
	Managed ManagedRepository
}

// RunStats 是一轮同步的统计摘要，仅存内存（持久化历史属 v0.4）。
type RunStats struct {
	FilesTotal       int
	FilesCreated     int
	FilesUpdated     int
	FilesDeleted     int
	FilesSkipped     int
	BytesTransferred int64
}

// Run 按契约顺序执行一轮同步：完整扫描 → Selector → 读取 managed →
// 计划 → 本地 preflight → create/update → relinquish → Copy/Mirror
// remote-delete。强制安全规则：
//
//   - 远端扫描未完整成功：整体失败，零本地变更（含删除）；
//   - 任一传输失败：整体失败，跳过后续 relinquish 与删除，
//     失败前已完成传输的文件保持 synced，下一轮继续收敛。
func Run(ctx context.Context, opts RunOptions) (RunStats, error) {
	var stats RunStats
	job := opts.Job

	// 1. 完整远端扫描：失败即中止，不做任何本地变更。
	remoteFiles, err := ScanRemote(ctx, opts.Remote, job.RemoteRoot)
	if err != nil {
		return stats, fmt.Errorf("remote scan failed; no local changes were made: %w", err)
	}
	stats.FilesTotal = len(remoteFiles)

	// 2. Selector：产出相对路径命中集合。
	sel, err := NewSelector(job.Include, job.Exclude)
	if err != nil {
		return stats, err
	}
	selected := make(map[string]bool, len(remoteFiles))
	for _, f := range remoteFiles {
		rel, err := remoteRelPath(job.RemoteRoot, f.Path)
		if err != nil {
			continue
		}
		if sel.Match(rel) {
			selected[rel] = true
		}
	}

	// 3-4. 读取 managed 并构造完整计划。
	managed, err := opts.Managed.ListByJob(ctx, job.ID)
	if err != nil {
		return stats, err
	}
	plan := BuildPlan(job.Mode, job.RemoteRoot, remoteFiles, selected, managed)

	// 5. 本地 preflight：download 目标冲突记为跳过（永不覆盖未知本地文件）。
	conflicts, err := PreflightLocal(job.LocalRoot, plan)
	if err != nil {
		return stats, err
	}

	downloader := NewDownloader(opts.Remote)
	now := time.Now().UTC()

	// transferFile 执行单个文件：pending 登记 → 原子下载 → synced 推进。
	// pending 先行登记保证传输中断的文件留在 managed 中（Mirror 授权
	// 语义完整）；下载成功后以本地实际 size/mtime 推进为 synced。
	transfer := func(e planEntry) error {
		if err := opts.Managed.Upsert(ctx, []ManagedFile{{
			JobID:        job.ID,
			RemotePath:   e.remote.Path,
			LocalRelPath: e.relPath,
			State:        StatePending,
			Remote:       e.remote.Fingerprint,
			UpdatedAt:    now,
		}}); err != nil {
			return err
		}
		if err := downloader.Download(ctx, e.remote.Path, job.LocalRoot, e.relPath, e.remote.Fingerprint); err != nil {
			return err
		}
		target, err := resolveLocalTarget(job.LocalRoot, e.relPath)
		if err != nil {
			return err
		}
		info, err := os.Lstat(target)
		if err != nil {
			return err
		}
		mtimeNs := info.ModTime().UnixNano()
		size := info.Size()
		return opts.Managed.Upsert(ctx, []ManagedFile{{
			JobID:        job.ID,
			RemotePath:   e.remote.Path,
			LocalRelPath: e.relPath,
			State:        StateSynced,
			Remote:       e.remote.Fingerprint,
			LocalSize:    &size,
			LocalMtimeNs: &mtimeNs,
			UpdatedAt:    now,
		}})
	}

	// 6a. skip 条目校验本地文件在位：managed synced 但本地缺失时转为修复下载。
	for _, e := range plan.Skips {
		target, err := resolveLocalTarget(job.LocalRoot, e.relPath)
		if err == nil {
			if _, statErr := os.Lstat(target); os.IsNotExist(statErr) {
				if err := transfer(e); err != nil {
					return stats, transferFailure(err)
				}
				stats.FilesCreated++
				stats.BytesTransferred += e.remote.Fingerprint.Size
				continue
			}
		}
		stats.FilesSkipped++
	}

	// 6b. downloads。
	for _, e := range plan.Downloads {
		if _, conflicted := conflicts[e.relPath]; conflicted {
			stats.FilesSkipped++
			continue
		}
		if err := transfer(e); err != nil {
			return stats, transferFailure(err)
		}
		stats.FilesCreated++
		stats.BytesTransferred += e.remote.Fingerprint.Size
	}

	// 6c. updates。
	for _, e := range plan.Updates {
		if err := transfer(e); err != nil {
			return stats, transferFailure(err)
		}
		stats.FilesUpdated++
		stats.BytesTransferred += e.remote.Fingerprint.Size
	}

	// 7. relinquish：仅清理 metadata，本地文件保留。
	if len(plan.Relinquish) > 0 {
		if err := opts.Managed.Delete(ctx, job.ID, plan.Relinquish); err != nil {
			return stats, err
		}
	}

	// 8. Mirror remote-delete：先删本地 managed 文件（缺失视为已完成），
	//    全部成功后清理 metadata；任一失败保留 metadata 供下轮重试。
	if len(plan.Deletes) > 0 {
		for _, remotePath := range plan.Deletes {
			rel, err := remoteRelPath(job.RemoteRoot, remotePath)
			if err != nil {
				return stats, err
			}
			target, err := resolveLocalTarget(job.LocalRoot, rel)
			if err != nil {
				return stats, err
			}
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return stats, fmt.Errorf("mirror delete %s: %w", target, err)
			}
			stats.FilesDeleted++
		}
		if err := opts.Managed.Delete(ctx, job.ID, plan.Deletes); err != nil {
			return stats, err
		}
	}

	return stats, nil
}

// transferFailure 包装传输失败错误，说明后续 metadata 变更与删除被跳过。
func transferFailure(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("transfer failed; metadata updates and mirror deletions were skipped: %w", err)
}
