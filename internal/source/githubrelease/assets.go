package githubrelease

import (
	"context"
	"fmt"
	"strings"
	"time"

	"tinysync/internal/source"
)

// Asset 是 GitHub Release Asset 的元数据子集。
type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	// UpdatedAt 是 GitHub 侧的最后更新时间（RFC3339）；同一 Release
	// 下重新上传 Asset 会改变 id 与 updated_at，进入 Fingerprint.Version
	// 后使增量同步可识别（契约见设计文档 §4）。
	UpdatedAt time.Time `json:"updated_at"`
	// Digest 是 GitHub 提供的内容摘要（"sha256:<hex>" 形式）；并非
	// 所有 Asset 都携带（历史 Asset 等），缺失为空串。
	Digest string `json:"digest"`
	// State 是上传状态：仅 uploaded 的 Asset 可下载。
	State string `json:"state"`
}

// uploaded 判断 Asset 是否已完成上传：仅 uploaded 状态进入快照。
// 上传中的条目缺席是安全的——完成上传后下一轮扫描表现为新增，
// Copy / Mirror 语义均正确（契约见设计文档 §3.3）。
func (a Asset) uploaded() bool {
	return a.State == "uploaded"
}

// listAssets 分页枚举一个 Release 的全部 Asset，仅保留已完成上传的
// 条目。同一 Release 内 asset name 大小写折叠冲突即整体失败：本地
// 大小写不敏感文件系统上会产生路径歧义，不落地（契约见设计文档 §3.3）。
func (c *client) listAssets(ctx context.Context, releaseID int64) ([]Asset, error) {
	all, err := listAll[Asset](ctx, c, c.apiPath(fmt.Sprintf("/releases/%d/assets?per_page=%d", releaseID, pageSize)))
	if err != nil {
		return nil, fmt.Errorf("github: list assets of release %d: %w", releaseID, err)
	}
	uploaded := make([]Asset, 0, len(all))
	seen := make(map[string]int64, len(all))
	for _, a := range all {
		if !a.uploaded() {
			continue
		}
		folded := strings.ToLower(a.Name)
		if prevID, conflict := seen[folded]; conflict {
			return nil, fmt.Errorf("%w: release %d has conflicting asset names %d/%d (%q case-insensitive)",
				source.ErrInvalid, releaseID, prevID, a.ID, a.Name)
		}
		seen[folded] = a.ID
		uploaded = append(uploaded, a)
	}
	return uploaded, nil
}
