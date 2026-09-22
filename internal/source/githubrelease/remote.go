package githubrelease

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"tinysync/internal/source"
)

// remote 是 source.Remote 的 GitHub Release 实现：把选中 Releases 的
// 版本目录与 Asset 映射为统一的只读逻辑目录树。
//
// 逻辑结构（契约见设计文档 §3）：
//
//	/                              ← Source root
//	/<encode(tag)>__<release_id>/  ← 版本目录（虚拟目录）
//	/<encode(tag)>__<release_id>/<asset name>
//
// 对象身份：Asset 的 Fingerprint 全部来自 asset 元数据（size /
// updated_at / digest / id 组合 Version），与浏览（Stat / List）或
// 扫描（ScanTree）的访问路径无关——同一 Asset 在任何入口得到同一
// 指纹。实例内缓存选中 Release 快照与 per-Release Asset 列表：一次
// 同步 run / 浏览会话内不重复打 API；缓存构建整体失败不留半成品。
type remote struct {
	client *client
	cfg    source.GitHubReleaseConfig

	mu     sync.Mutex
	snap   *selectedSnapshot
	assets map[int64][]Asset
}

// 编译期断言：实现 Remote 与可选的 TreeScanner 能力。
var (
	_ source.Remote      = (*remote)(nil)
	_ source.TreeScanner = (*remote)(nil)
)

// selectedSnapshot 是一次版本发现的快照：选中 Release 的目录名索引
// 与确定性顺序。版本选择策略（身份字段）在 Source 不可变期间保证
// 快照稳定；构建失败整体失败，不缓存半成品。
type selectedSnapshot struct {
	// byDir 索引目录名 → Release。
	byDir map[string]Release
	// ordered 是按目录名字典序的确定性顺序（扫描与列表共用）。
	ordered []Release
}

// fingerprintOf 把 Asset 元数据映射为 TinySync 指纹（契约见设计文档
// §4）：Size=size、ModifiedAt=updated_at（UTC）、Checksum=digest
// （原样含 sha256: 前缀）、Version=<assetID>:<updated_at>:<digest>。
// Version 携带内容身份：同一 Release 下重新上传 Asset（新 id / 新
// updated_at）即产生新 Version，增量同步经 fingerprintChanged 的
// Version 优先级直接识别。
func fingerprintOf(a Asset) source.Fingerprint {
	updated := a.UpdatedAt.UTC().Format(time.RFC3339)
	return source.Fingerprint{
		Size:       a.Size,
		ModifiedAt: a.UpdatedAt.UTC(),
		Checksum:   a.Digest,
		Version:    fmt.Sprintf("%d:%s:%s", a.ID, updated, a.Digest),
	}
}

// notFound 构造「不存在」错误：ErrInvalid 保持既有判定语义，
// fs.ErrNotExist 供上层（browser 404 映射）区分「不存在」与「非法
// 路径」（与 S3 adapter 的双标记同构）。
func notFound(p string) error {
	return fmt.Errorf("%w: github_release entry %s not found (%w)", source.ErrInvalid, p, fs.ErrNotExist)
}

// snapshot 返回实例内缓存的选中 Release 快照，首次访问时构建。
func (r *remote) snapshot(ctx context.Context) (*selectedSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snap != nil {
		return r.snap, nil
	}
	releases, err := r.client.selectReleases(ctx, r.cfg)
	if err != nil {
		return nil, err
	}
	snap := &selectedSnapshot{byDir: make(map[string]Release, len(releases))}
	for _, rel := range releases {
		name := versionDirName(rel.TagName, rel.ID)
		// Release ID 唯一 ⇒ 目录名唯一；重复即 API 异常，fail-fast。
		if _, dup := snap.byDir[name]; dup {
			return nil, fmt.Errorf("%w: duplicate github_release version dir %q", source.ErrInvalid, name)
		}
		snap.byDir[name] = rel
		snap.ordered = append(snap.ordered, rel)
	}
	sort.Slice(snap.ordered, func(i, j int) bool {
		return versionDirName(snap.ordered[i].TagName, snap.ordered[i].ID) <
			versionDirName(snap.ordered[j].TagName, snap.ordered[j].ID)
	})
	r.snap = snap
	return snap, nil
}

// releaseAssets 返回 per-Release Asset 列表（实例内缓存）。
func (r *remote) releaseAssets(ctx context.Context, releaseID int64) ([]Asset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cached, ok := r.assets[releaseID]; ok {
		return cached, nil
	}
	assets, err := r.client.listAssets(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		// 防御非法 asset 名进入本地 filepath：GitHub 侧名字可能含
		// 反斜杠 / dot-segment 等不可移植分量，扫描与浏览统一在此
		// fail-fast（整体失败，契约见设计文档 §3.1）。
		if err := source.ValidateLogicalPath("/" + a.Name); err != nil {
			return nil, fmt.Errorf("github: asset name %q of release %d: %w", a.Name, releaseID, err)
		}
	}
	if r.assets == nil {
		r.assets = make(map[int64][]Asset)
	}
	r.assets[releaseID] = assets
	return assets, nil
}

// splitReleasePath 把逻辑路径拆为（版本目录, asset 名）："/" → 两段
// 皆空；"/<dir>" → (dir, "")；"/<dir>/<asset>" → (dir, asset)；更深
// 层次非法（版本目录下没有子目录）。
func splitReleasePath(p string) (dir, asset string, err error) {
	if err := source.ValidateLogicalPath(p); err != nil {
		return "", "", err
	}
	if p == "/" {
		return "", "", nil
	}
	trimmed := strings.TrimPrefix(p, "/")
	dir, rest, found := strings.Cut(trimmed, "/")
	if !found {
		return dir, "", nil
	}
	if strings.Contains(rest, "/") {
		return "", "", fmt.Errorf("%w: github_release path %q exceeds version directory depth", source.ErrInvalid, p)
	}
	return dir, rest, nil
}

// Stat 实现 source.Remote。入口统一校验 logical path：非法路径
// fail-fast。
func (r *remote) Stat(ctx context.Context, p string) (source.FileInfo, error) {
	dir, asset, err := splitReleasePath(p)
	if err != nil {
		return source.FileInfo{}, err
	}
	if dir == "" {
		// root：验证仓库可达（一次请求，经 ETag 条件缓存）。
		if err := r.client.verifyRepo(ctx); err != nil {
			return source.FileInfo{}, fmt.Errorf("github: stat %s: %w", p, err)
		}
		return source.FileInfo{Path: "/", IsDir: true}, nil
	}
	if _, _, ok := splitVersionDir(dir); !ok {
		return source.FileInfo{}, notFound(p)
	}
	snap, err := r.snapshot(ctx)
	if err != nil {
		return source.FileInfo{}, err
	}
	rel, selected := snap.byDir[dir]
	if !selected {
		// 选中范围外的版本目录一律不可见：浏览与同步都不绕过策略。
		return source.FileInfo{}, notFound(p)
	}
	if asset == "" {
		return source.FileInfo{Path: path.Clean("/" + dir), IsDir: true}, nil
	}
	assets, err := r.releaseAssets(ctx, rel.ID)
	if err != nil {
		return source.FileInfo{}, fmt.Errorf("github: stat %s: %w", p, err)
	}
	for _, a := range assets {
		if a.Name != asset {
			continue
		}
		return source.FileInfo{
			Path:        path.Clean("/" + dir + "/" + a.Name),
			IsDir:       false,
			Fingerprint: fingerprintOf(a),
		}, nil
	}
	return source.FileInfo{}, notFound(p)
}

// List 实现 source.Remote（非递归列目录）：GitHub 侧没有目录分页，
// 单层完整枚举后在 adapter 边界切片分页（cursor 为 opaque offset
// token），与 WebDAV / SFTP 的切片分页同构。
func (r *remote) List(ctx context.Context, p string, opts source.ListOptions) (source.FilePage, error) {
	dir, _, err := splitReleasePath(p)
	if err != nil {
		return source.FilePage{}, err
	}
	if dir == "" {
		snap, err := r.snapshot(ctx)
		if err != nil {
			return source.FilePage{}, fmt.Errorf("github: list %s: %w", p, err)
		}
		entries := make([]source.FileInfo, 0, len(snap.ordered))
		for _, rel := range snap.ordered {
			name := versionDirName(rel.TagName, rel.ID)
			entries = append(entries, source.FileInfo{
				Path: "/" + name,
				// 目录条目指纹留空：版本目录是虚拟目录，增量语义
				// 全部由其下 Asset 承载。
				IsDir: true,
			})
		}
		return source.PageSlice(entries, opts)
	}
	if _, _, ok := splitVersionDir(dir); !ok {
		return source.FilePage{}, notFound(p)
	}
	snap, err := r.snapshot(ctx)
	if err != nil {
		return source.FilePage{}, err
	}
	rel, selected := snap.byDir[dir]
	if !selected {
		return source.FilePage{}, notFound(p)
	}
	assets, err := r.releaseAssets(ctx, rel.ID)
	if err != nil {
		return source.FilePage{}, fmt.Errorf("github: list %s: %w", p, err)
	}
	entries := make([]source.FileInfo, 0, len(assets))
	for _, a := range assets {
		// Path 使用编码后的版本目录名（dir），不是解码 tag：逻辑路径
		// 契约以编码形态寻址，解码仅用于展示（设计文档 §3.2）。
		entries = append(entries, source.FileInfo{
			Path:        "/" + dir + "/" + a.Name,
			IsDir:       false,
			Fingerprint: fingerprintOf(a),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return source.PageSlice(entries, opts)
}

// Open 实现 source.Remote：按逻辑路径定位 Asset 后打开二进制流
// （见 download.go openAsset）。按路径中的 Release ID 与 asset 名精
// 确匹配，选中范围外的路径一律拒绝。
func (r *remote) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	dir, asset, err := splitReleasePath(p)
	if err != nil {
		return nil, err
	}
	if dir == "" || asset == "" {
		return nil, fmt.Errorf("%w: github_release path %s is not a file", source.ErrInvalid, p)
	}
	snap, err := r.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	rel, selected := snap.byDir[dir]
	if !selected {
		return nil, notFound(p)
	}
	assets, err := r.releaseAssets(ctx, rel.ID)
	if err != nil {
		return nil, fmt.Errorf("github: open %s: %w", p, err)
	}
	for _, a := range assets {
		if a.Name == asset {
			return r.client.openAsset(ctx, a.ID)
		}
	}
	return nil, notFound(p)
}

// Close 实现 source.Remote：GitHub adapter 基于 HTTP 无持久会话，
// 连接复用由 http.Transport 管理，显式关闭为空操作。
func (r *remote) Close() error {
	return nil
}

// ScanTree 实现 source.TreeScanner：全树扫描 root 子树，文件与目录
// 都 visit（root 自身除外）。root="/" 输出全部选中版本目录与其下
// 全部 Asset；root=版本目录输出该版本的全部 Asset。每个版本目录恰
// 一次 Asset 枚举请求；任何一页失败即整体失败，绝不返回部分结果
// （Mirror 的删除授权依赖完整快照，契约见 source.TreeScanner）。
func (r *remote) ScanTree(ctx context.Context, root string, visit func(source.FileInfo) error) error {
	dir, _, err := splitReleasePath(root)
	if err != nil {
		return err
	}
	snap, err := r.snapshot(ctx)
	if err != nil {
		return err
	}
	visitDir := func(name string) error {
		return visit(source.FileInfo{Path: "/" + name, IsDir: true})
	}
	if dir != "" {
		// 单版本子树：root 自身不 visit。
		rel, selected := snap.byDir[dir]
		if !selected {
			return notFound(root)
		}
		return r.visitReleaseAssets(ctx, rel, dir, visit)
	}
	for _, rel := range snap.ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := versionDirName(rel.TagName, rel.ID)
		if err := visitDir(name); err != nil {
			return err
		}
		if err := r.visitReleaseAssets(ctx, rel, name, visit); err != nil {
			return err
		}
	}
	return nil
}

// visitReleaseAssets 枚举一个版本的 Asset 并逐条 visit 文件。条目
// 循环内逐条检查 ctx：取消不能等到下一个版本的请求边界才生效。
func (r *remote) visitReleaseAssets(ctx context.Context, rel Release, dirName string, visit func(source.FileInfo) error) error {
	assets, err := r.releaseAssets(ctx, rel.ID)
	if err != nil {
		return err
	}
	for _, a := range assets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(source.FileInfo{
			Path:        "/" + dirName + "/" + a.Name,
			IsDir:       false,
			Fingerprint: fingerprintOf(a),
		}); err != nil {
			return err
		}
	}
	return nil
}
