package syncjob

import (
	"fmt"
	"os"
	"sort"

	"tinysync/internal/source"
)

// Action 是计划中单个文件的动作类型。
type Action string

// 计划动作：download 创建本地新文件，update 覆盖已管理文件，
// skip 表示远端指纹未变无需传输。
const (
	ActionDownload Action = "download"
	ActionUpdate   Action = "update"
	ActionSkip     Action = "skip"
)

// planEntry 是单个远端文件的写动作条目。
type planEntry struct {
	relPath string
	remote  source.FileInfo
	action  Action
}

// Plan 是一轮同步的完整计划。Relinquish 与 Deletes 存 remote_path
// （/ 开头的 logical path）：前者释放 metadata 但保留本地（selector
// 排除、Copy 模式的远端消失），后者按 Mirror 授权删除本地文件。
// 所有列表按路径确定性排序。
type Plan struct {
	Downloads  []planEntry
	Updates    []planEntry
	Skips      []planEntry
	Relinquish []string
	Deletes    []string
}

// HasWork 判断计划是否包含任何动作。
func (p Plan) HasWork() bool {
	return len(p.Downloads) > 0 || len(p.Updates) > 0 || len(p.Skips) > 0 ||
		len(p.Relinquish) > 0 || len(p.Deletes) > 0
}

// BuildPlan 对比完整远端快照与 managed 记录，产出确定性排序的计划。
// remoteFiles 必须是 ScanRemote 的完整结果；selected 是 selector 命中的
// 相对路径集合。mode 决定远端消失的处理：Copy 释放授权保留本地，
// Mirror 删除 managed 本地文件；selector 排除永远走释放——文件仍在
// 远端，不构成删除理由。
func BuildPlan(mode Mode, remoteRoot string, remoteFiles []source.FileInfo, selected map[string]bool, managed []ManagedFile) Plan {
	managedByPath := make(map[string]ManagedFile, len(managed))
	for _, m := range managed {
		managedByPath[m.RemotePath] = m
	}
	remoteByPath := make(map[string]source.FileInfo, len(remoteFiles))

	type remoteEntry struct {
		rel string
		fi  source.FileInfo
	}
	entries := make([]remoteEntry, 0, len(remoteFiles))
	for _, f := range remoteFiles {
		rel, err := remoteRelPath(remoteRoot, f.Path)
		if err != nil {
			// ScanRemote 已验证路径在 root 内；防御坏数据直接跳过。
			continue
		}
		entries = append(entries, remoteEntry{rel: rel, fi: f})
		remoteByPath[f.Path] = f
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	plan := Plan{}
	seenRel := make(map[string]bool, len(entries))
	for _, e := range entries {
		if seenRel[e.rel] {
			continue
		}
		seenRel[e.rel] = true
		managedRec, wasManaged := managedByPath[e.fi.Path]

		switch {
		case !selected[e.rel]:
			if wasManaged {
				plan.Relinquish = append(plan.Relinquish, e.fi.Path)
			}
		case !wasManaged:
			plan.Downloads = append(plan.Downloads, planEntry{relPath: e.rel, remote: e.fi, action: ActionDownload})
		case fingerprintChanged(e.fi.Fingerprint, managedRec.Remote):
			plan.Updates = append(plan.Updates, planEntry{relPath: e.rel, remote: e.fi, action: ActionUpdate})
		default:
			plan.Skips = append(plan.Skips, planEntry{relPath: e.rel, remote: e.fi, action: ActionSkip})
		}
	}

	var gone []string
	for _, m := range managed {
		if _, stillThere := remoteByPath[m.RemotePath]; !stillThere {
			gone = append(gone, m.RemotePath)
		}
	}
	sort.Strings(gone)
	if mode == ModeMirror {
		plan.Deletes = gone
	} else {
		plan.Relinquish = append(plan.Relinquish, gone...)
	}
	sort.Strings(plan.Relinquish)
	return plan
}

// fingerprintChanged 按优先级判定远端指纹相对已知指纹是否变化：
// Version → Checksum → ETag+Size → Size+ModifiedAt；无可判定信息时
// 视为未变，不盲目重传。ETag 是 opaque token，只比较相等性。
func fingerprintChanged(now, known source.Fingerprint) bool {
	if now.Version != "" || known.Version != "" {
		return now.Version != known.Version
	}
	if now.Checksum != "" || known.Checksum != "" {
		return now.Checksum != known.Checksum
	}
	if now.ETag != "" || known.ETag != "" {
		return now.ETag != known.ETag || now.Size != known.Size
	}
	if now.Size != known.Size {
		return true
	}
	if !now.ModifiedAt.IsZero() && !known.ModifiedAt.IsZero() {
		return !now.ModifiedAt.Equal(known.ModifiedAt)
	}
	return false
}

// PreflightLocal 对计划中的 download 条目做本地冲突预检：目标已被
// 未知本地文件、目录或 symlink 占用时记入冲突表（值为原因）。
// 引擎据其跳过这些条目——永不覆盖未知本地文件。返回非 nil error
// 表示预检本身失败（如 stat 出错）。
func PreflightLocal(localRoot string, plan Plan) (map[string]string, error) {
	conflicts := make(map[string]string)
	for _, e := range plan.Downloads {
		target, err := resolveLocalTarget(localRoot, e.relPath)
		if err != nil {
			conflicts[e.relPath] = err.Error()
			continue
		}
		if err := rejectSymlinkComponents(localRoot, target); err != nil {
			conflicts[e.relPath] = err.Error()
			continue
		}
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("preflight %s: %w", e.relPath, err)
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			conflicts[e.relPath] = "target is a symlink"
		case info.IsDir():
			conflicts[e.relPath] = "target is an existing directory"
		default:
			conflicts[e.relPath] = "target is an unknown local file; never overwrite"
		}
	}
	return conflicts, nil
}
