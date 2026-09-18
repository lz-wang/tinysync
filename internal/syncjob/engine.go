package syncjob

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"tinysync/internal/logging"
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
	// Items 接收文件级变更明细；nil 表示不记录（只累计 summary）。
	Items ItemRecorder
	// RunID 是本轮运行的持久化 ID，写入每条明细。
	RunID string
	// Transfers 是本轮下载前必须 Acquire 的并发上限。生产路径由 Runner
	// 注入全进程共享的 limiter，使上限约束所有 Job 的下载总和；nil
	//（独立调用）退化为本地单传输串行。远端下载 I/O 并行，SQLite
	// 状态推进始终串行。
	Transfers *TransferLimiter
	// TransferTimeout 是单文件单次 attempt 的传输超时；0 表示不启用
	//（由运行配置注入，默认保持既有行为）。
	TransferTimeout time.Duration
}

// ItemRecorder 接收文件级变更明细。返回错误视为本轮失败：历史明细缺失
// 与 metadata 缺失同等对待，不做静默降级。
type ItemRecorder interface {
	RecordItem(ctx context.Context, item RunItem) error
}

// RunStats 是一轮同步的统计摘要。
type RunStats struct {
	FilesTotal       int
	FilesCreated     int
	FilesUpdated     int
	FilesDeleted     int
	FilesSkipped     int
	BytesTransferred int64
}

// transferJob 是待传输的单文件任务。
type transferJob struct {
	entry  planEntry
	action RunItemAction // ItemCreate | ItemUpdate
}

// transferOutcome 是一次并发下载的结果。
type transferOutcome struct {
	job transferJob
	err error
}

// Run 按契约顺序执行一轮同步：完整扫描 → Selector → 读取 managed →
// 计划 → 本地 preflight → create/update → relinquish → Copy/Mirror
// remote-delete。传输阶段为「有界并发下载 + 单协调者串行推进」：
// pending 登记与 synced 推进全部由协调者串行写（SQLite 单连接），
// 仅远端下载 I/O 并行。强制安全规则：
//
//   - 远端扫描未完整成功：整体失败，零本地变更（含删除）；
//   - 任一传输失败：取消剩余工作并等待在途 worker 收敛，整体失败，
//     跳过后续 relinquish 与删除；失败前已完成传输的文件保持 synced，
//     已派发未完成的文件保持 pending（下一轮强制重传）继续收敛。
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

	// 5.5 filesystem compatibility preflight：跨平台本地映射校验
	//（Windows 非法文件名、case-insensitive 冲突）。位于任何本地
	// mutation 之前，不全通过则整轮失败、零本地变更。
	if err := PreflightFilesystemCompat(job.LocalRoot, plan, DefaultFilenamePolicy()); err != nil {
		return stats, fmt.Errorf("filesystem compatibility preflight failed; no local changes were made: %w", err)
	}

	downloader := NewDownloader(opts.Remote)
	downloader.timeout = opts.TransferTimeout
	now := time.Now().UTC()

	// recordItem 记录文件级明细；记录失败使本轮失败（不静默丢历史）。
	recordItem := func(item RunItem) error {
		if opts.Items == nil {
			return nil
		}
		item.RunID = opts.RunID
		if err := opts.Items.RecordItem(ctx, item); err != nil {
			return fmt.Errorf("record run item %s: %w", item.Path, err)
		}
		return nil
	}

	// markPending 在派发前登记 pending（协调者串行写）：pending 先行
	// 登记保证传输中断的文件留在 managed 中（Mirror 授权语义完整）。
	markPending := func(j transferJob) error {
		return opts.Managed.Upsert(ctx, []ManagedFile{{
			JobID:        job.ID,
			RemotePath:   j.entry.remote.Path,
			LocalRelPath: j.entry.relPath,
			State:        StatePending,
			Remote:       j.entry.remote.Fingerprint,
			UpdatedAt:    now,
		}})
	}

	// applySuccess 在下载成功后以本地实际 size/mtime 推进 synced
	//（协调者串行写）并记录明细。
	applySuccess := func(j transferJob) error {
		target, err := resolveLocalTarget(job.LocalRoot, j.entry.relPath)
		if err != nil {
			return err
		}
		info, err := os.Lstat(target)
		if err != nil {
			return err
		}
		mtimeNs := info.ModTime().UnixNano()
		size := info.Size()
		if err := opts.Managed.Upsert(ctx, []ManagedFile{{
			JobID:        job.ID,
			RemotePath:   j.entry.remote.Path,
			LocalRelPath: j.entry.relPath,
			State:        StateSynced,
			Remote:       j.entry.remote.Fingerprint,
			LocalSize:    &size,
			LocalMtimeNs: &mtimeNs,
			UpdatedAt:    now,
		}}); err != nil {
			return err
		}
		if err := recordItem(RunItem{
			Path:   j.entry.relPath,
			Action: j.action,
			Status: ItemSucceeded,
			Bytes:  size,
		}); err != nil {
			return err
		}
		switch j.action {
		case ItemCreate:
			stats.FilesCreated++
		case ItemUpdate:
			stats.FilesUpdated++
		}
		stats.BytesTransferred += size
		return nil
	}

	// 组装传输任务（确定性顺序：修复 → 下载 → 更新）。
	var jobs []transferJob

	// 6a. skip 条目校验本地文件在位：managed synced 但本地缺失时转为
	// 修复下载；其余 unchanged 只累计 skipped（不写明细）。
	for _, e := range plan.Skips {
		target, err := resolveLocalTarget(job.LocalRoot, e.relPath)
		if err == nil {
			if _, statErr := os.Lstat(target); os.IsNotExist(statErr) {
				jobs = append(jobs, transferJob{entry: e, action: ItemCreate})
				continue
			}
		}
		stats.FilesSkipped++
	}

	// 6b. downloads：冲突条目记 skipped 明细（永不覆盖），其余入队。
	for _, e := range plan.Downloads {
		if reason, conflicted := conflicts[e.relPath]; conflicted {
			stats.FilesSkipped++
			if err := recordItem(RunItem{
				Path:   e.relPath,
				Action: ItemCreate,
				Status: ItemSkipped,
				Error:  reason,
			}); err != nil {
				return stats, err
			}
			continue
		}
		jobs = append(jobs, transferJob{entry: e, action: ItemCreate})
	}

	// 6c. updates。
	for _, e := range plan.Updates {
		jobs = append(jobs, transferJob{entry: e, action: ItemUpdate})
	}

	// 传输阶段：pending 登记串行、下载并发、结果单点收敛。并发名额由
	// TransferLimiter 统一控制（Runner 注入进程级共享 limiter，独立
	// 调用退化为本地串行）。首次失败后停止派发、取消在途工作并排空
	// 结果——失败后完成的下载不推进 synced（保留 pending 供下一轮
	// 重传），relinquish 与 Mirror delete 一律不执行。
	limiter := opts.Transfers
	if limiter == nil {
		limiter = NewTransferLimiter(1)
	}
	// transferCtx 是传输阶段的可取消子 context：首次失败即取消在途
	// 下载（远端 reader 绑定 request context，取消立即中断读取），
	// 不让失败后的大文件继续消耗远端流量或推迟失败收敛。
	transferCtx, cancelTransfers := context.WithCancel(ctx)
	defer cancelTransfers()

	var (
		wg sync.WaitGroup
		// results 带全量缓冲：worker 发送结果后立即执行 defer 释放
		// 传输名额，不依赖协调者当时是否在收取——否则多个 Job 的协调者
		// 同时阻塞在全局 limiter 的 Acquire 上会互相等死。
		results  = make(chan transferOutcome, len(jobs))
		firstErr error
		inflight int
	)
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
			cancelTransfers()
		}
	}
	next := 0
	for next < len(jobs) || inflight > 0 {
		for next < len(jobs) && inflight < limiter.Capacity() && firstErr == nil && transferCtx.Err() == nil {
			j := jobs[next]
			// 先占全局传输名额再登记 pending：拿不到名额的文件保持
			// 计划态，不提前把 pending 写入 managed。
			if err := limiter.Acquire(transferCtx); err != nil {
				break
			}
			if err := markPending(j); err != nil {
				limiter.Release()
				fail(transferFailure(fmt.Errorf("register pending for %s: %w", j.entry.relPath, err)))
				break
			}
			next++
			inflight++
			wg.Add(1)
			go func(j transferJob) {
				defer wg.Done()
				defer limiter.Release()
				err := downloader.Download(transferCtx, j.entry.remote.Path, job.LocalRoot, j.entry.relPath, j.entry.remote.Fingerprint)
				results <- transferOutcome{job: j, err: err}
			}(j)
		}
		if inflight == 0 {
			break
		}
		out := <-results
		inflight--
		if out.err != nil {
			fail(transferFailure(fmt.Errorf("transfer %s: %w", out.job.entry.relPath, out.err)))
			// 文件级失败事件（v0.9 可观测性契约：带 path；成功的单
			// 文件不逐条输出，避免大目录产生海量日志）。
			logging.Infof("event=sync_file_failed job_id=%s run_id=%s path=%s error=%q",
				job.ID, opts.RunID, out.job.entry.relPath, out.err.Error())
			// 失败明细尽力记录：主错误（传输失败）优先，不被覆盖。
			// 因取消被中断的在途下载同样如实记 failed。
			_ = recordItem(RunItem{
				Path:   out.job.entry.relPath,
				Action: out.job.action,
				Status: ItemFailed,
				Error:  out.err.Error(),
			})
			continue
		}
		if firstErr != nil || transferCtx.Err() != nil {
			continue
		}
		if err := applySuccess(out.job); err != nil {
			fail(transferFailure(fmt.Errorf("finalize transfer %s: %w", out.job.entry.relPath, err)))
		}
	}
	wg.Wait()
	if firstErr != nil {
		return stats, firstErr
	}
	if err := ctx.Err(); err != nil {
		return stats, transferFailure(err)
	}

	// 7. relinquish：仅清理 metadata，本地文件保留。
	if len(plan.Relinquish) > 0 {
		if err := opts.Managed.Delete(ctx, job.ID, plan.Relinquish); err != nil {
			return stats, err
		}
		for _, remotePath := range plan.Relinquish {
			rel, err := remoteRelPath(job.RemoteRoot, remotePath)
			if err != nil {
				return stats, err
			}
			if err := recordItem(RunItem{Path: rel, Action: ItemRelinquish, Status: ItemSucceeded}); err != nil {
				return stats, err
			}
		}
	}

	// 8. Mirror remote-delete：先删本地 managed 文件（缺失视为已完成），
	//    全部成功后清理 metadata；任一失败保留 metadata 供下轮重试。
	//    删除与下载同等对待：执行前校验既有路径组件，父目录出现
	//    symlink 一律拒绝——lexical 路径落在 LocalRoot 之内不代表
	//    解析后的真实目标也在之内。
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
			if err := rejectSymlinkComponents(job.LocalRoot, target); err != nil {
				return stats, fmt.Errorf("mirror delete %s: %w", target, err)
			}
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				// 文件级失败事件：Mirror 删除失败同样带 path。
				logging.Infof("event=sync_file_failed job_id=%s run_id=%s path=%s error=%q",
					job.ID, opts.RunID, rel, err.Error())
				// 主错误优先；失败明细尽力记录。
				_ = recordItem(RunItem{
					Path:   rel,
					Action: ItemDelete,
					Status: ItemFailed,
					Error:  err.Error(),
				})
				return stats, fmt.Errorf("mirror delete %s: %w", target, err)
			}
			stats.FilesDeleted++
			if err := recordItem(RunItem{Path: rel, Action: ItemDelete, Status: ItemSucceeded}); err != nil {
				return stats, err
			}
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
