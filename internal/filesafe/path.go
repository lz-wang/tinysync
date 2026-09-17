// Package filesafe 提供本地文件访问的受限路径原语：逻辑路径校验、
// root confinement 与普通文件解析。Local Browser 下载与 Published
// HTTP Serving 共用本包，保证「校验 → 解析 → 打开」只存在一套安全
// 语义；远端协议路径的基础规则也由此包提供，避免两套 path 校验。
package filesafe

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrNotRegularFile 表示目标存在但不是普通文件（目录、symlink、
// 设备等）。调用方以 errors.Is 区分「不是普通文件」与其他失败；
// symlink 无论指向 root 内还是外都归入本错误。
var ErrNotRegularFile = errors.New("not a regular file")

// ErrEscape 表示解析结果越出 root（dot segments 在校验层拒绝，
// 本错误针对 symlink 组件导致的逃逸）。调用方以 errors.Is 判定。
var ErrEscape = errors.New("path escapes root")

// ValidateLogicalPath 校验以 / 分隔的绝对逻辑路径；"/" 表示 root。
// 规则：非空、以 / 开头、不含反斜杠与 NUL、除 "/" 外不以 / 结尾、
// path.Clean 后不变（拒绝 dot segments 与重复分隔符）。反斜杠必须
// 拒绝——进入本地 filepath 后在 Unix 与 Windows 上语义不同。
func ValidateLogicalPath(p string) error {
	if p == "" {
		return fmt.Errorf("logical path is empty")
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("logical path %q must be absolute", p)
	}
	if strings.ContainsRune(p, '\\') {
		return fmt.Errorf("logical path %q must not contain backslash", p)
	}
	if strings.ContainsRune(p, '\x00') {
		return fmt.Errorf("logical path %q must not contain NUL", p)
	}
	if p != "/" && strings.HasSuffix(p, "/") {
		return fmt.Errorf("logical path %q must not end with /", p)
	}
	if p != "/" && path.Clean(p) != p {
		return fmt.Errorf("logical path %q is not clean", p)
	}
	return nil
}

// ResolveWithinRoot 把逻辑路径解析为 root 之下的文件系统路径。
// root 必须是已 canonicalize（abs + EvalSymlinks）的目录。逻辑路径
// 先过 ValidateLogicalPath，再以 Join + Rel 双向验证结果不越出
// root。本函数是纯路径代数，不做存在性检查：列目录场景下条目
// 可能随时增删，打开场景的存在性检查属于 ResolveRegularFile。
func ResolveWithinRoot(root, logicalPath string) (string, error) {
	if err := ValidateLogicalPath(logicalPath); err != nil {
		return "", err
	}
	if logicalPath == "/" {
		return root, nil
	}
	target := filepath.Join(root, filepath.FromSlash(logicalPath))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("logical path %q escapes root %s", logicalPath, root)
	}
	return target, nil
}

// ResolveRegularFile 在 ResolveWithinRoot 之上要求目标已存在且是
// 普通文件，整条路径不含 symlink：
//   - Lstat 拒绝目标自身是 symlink（无论指向 root 内还是外，
//     浏览 / 下载 / 发布语义一致：symlink 显示不跟随）；
//   - EvalSymlinks 解析全部组件后用 Rel 验证仍在 root 内，防御
//     父目录组件中的 symlink 逃逸。
//
// 返回 canonical 绝对路径与目标 FileInfo。目标不存在时返回包装
// fs.ErrNotExist 的错误，调用方按 404 处理。
func ResolveRegularFile(root, logicalPath string) (string, os.FileInfo, error) {
	// root 先归一为 canonical 形态：调用方（LocalRoot）通常已按
	// abs + EvalSymlinks 规则处理，这里再归一一次，防御未经归一的
	// root（如 macOS 的 /var -> /private/var）与解析后的目标比较时
	// 把 root 内路径误判为逃逸。root 不存在时报错，属无效输入。
	if canonicalRoot, err := filepath.EvalSymlinks(root); err != nil {
		return "", nil, err
	} else if canonicalRoot != root {
		root = canonicalRoot
	}
	target, err := ResolveWithinRoot(root, logicalPath)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("%w: %s is a symlink", ErrNotRegularFile, logicalPath)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%w: %s", ErrNotRegularFile, logicalPath)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", nil, err
	}
	if resolved != target {
		// target 自身不是 symlink，解析差异只能来自父目录组件中的
		// symlink（或大小写归一）；确认解析结果仍在 root 内。
		rel, relErr := filepath.Rel(root, resolved)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", nil, fmt.Errorf("%w: logical path %q escapes root %s via symlink", ErrEscape, logicalPath, root)
		}
		// info 取自 target，与 resolved 指向同一文件；重新 Stat 保证
		// 返回的 FileInfo 对应最终打开的路径。
		info, err = os.Stat(resolved)
		if err != nil {
			return "", nil, err
		}
	}
	return resolved, info, nil
}

// LstatWithinRoot 把逻辑路径解析为 root 之下的文件系统路径并返回
// 最终组件的 Lstat 信息：父目录组件不得经 symlink 逃逸 root，最终
// 组件保留 Lstat 语义（symlink 原样呈现、不跟随）。浏览 Stat 场景
// 专用：symlink 本身可以显示，但不能作为路径中间节点把 root 外的
// 文件元数据（存在性、类型、size、mtime）泄露进来。
//
// 返回加固后的文件系统路径（父目录已解析为真实形态）与 FileInfo。
func LstatWithinRoot(root, logicalPath string) (string, os.FileInfo, error) {
	// root 先归一为 canonical 形态，与 ResolveRegularFile 同一防御。
	if canonicalRoot, err := filepath.EvalSymlinks(root); err != nil {
		return "", nil, err
	} else if canonicalRoot != root {
		root = canonicalRoot
	}
	target, err := ResolveWithinRoot(root, logicalPath)
	if err != nil {
		return "", nil, err
	}
	if logicalPath == "/" {
		info, err := os.Lstat(target)
		if err != nil {
			return "", nil, err
		}
		return target, info, nil
	}
	// 词法 containment 不约束 symlink 组件的运行时解析：对父目录做
	// EvalSymlinks 后重建目标，确认父目录真实形态仍在 root 内。父
	// 目录链上有不存在组件时 EvalSymlinks 报 ENOENT，与直接 Lstat
	// 目标的行为一致。
	resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return "", nil, err
	}
	rel, relErr := filepath.Rel(root, resolvedDir)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", nil, fmt.Errorf("%w: logical path %q escapes root %s via symlink", ErrEscape, logicalPath, root)
	}
	final := filepath.Join(resolvedDir, filepath.Base(target))
	info, err := os.Lstat(final)
	if err != nil {
		return "", nil, err
	}
	return final, info, nil
}

// OpenCanonicalRegularFile 打开持久化 canonical 绝对路径所指的普通
// 文件。Published serving 以「创建时校验、之后长期复用」的 canonical
// local_path 为身份，文件系统可能在创建之后发生变化；本函数在每次
// serving 前重放身份校验，任何原组件后来变成 symlink 都会立即拒绝：
//   - Lstat 最终组件：目标自身是 symlink 一律拒绝；
//   - EvalSymlinks 全链解析：结果必须仍等于持久化路径，否则按
//     ErrEscape 拒绝（canonical 路径已不再指向同一文件身份）；
//   - 打开后以句柄上的 Stat 再确认普通文件，防御校验与打开之间
//     的类型替换。
//
// 完全消除「检查后、打开前替换」的 TOCTOU 需要 openat/O_NOFOLLOW
// 级别的原语，属后续 hardening 范围；本函数封死的是请求发起之前
// 已经存在的 symlink 替换。
func OpenCanonicalRegularFile(canonicalPath string) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(canonicalPath)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("%w: %s is a symlink", ErrNotRegularFile, canonicalPath)
	}
	resolved, err := filepath.EvalSymlinks(canonicalPath)
	if err != nil {
		return nil, nil, err
	}
	if resolved != canonicalPath {
		return nil, nil, fmt.Errorf("%w: %s now resolves to %s via symlink", ErrEscape, canonicalPath, resolved)
	}
	f, err := os.Open(canonicalPath)
	if err != nil {
		return nil, nil, err
	}
	info, err = f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%w: %s", ErrNotRegularFile, canonicalPath)
	}
	return f, info, nil
}

// NormalizePublicPath 校验并归一 Publish 的 public path：以 / 开头、
// clean、非 root。public path 是 /published 之下的唯一公开标识，
// 与逻辑路径共用基础规则；"/" 表示发布整个根，与单文件模型冲突，
// 显式拒绝。
func NormalizePublicPath(p string) (string, error) {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return "", fmt.Errorf("public path is empty")
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("public path %q must be absolute", trimmed)
	}
	if strings.ContainsRune(trimmed, '\\') {
		return "", fmt.Errorf("public path %q must not contain backslash", trimmed)
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", fmt.Errorf("public path %q must not contain NUL", trimmed)
	}
	if strings.HasSuffix(trimmed, "/") {
		return "", fmt.Errorf("public path %q must not end with /", trimmed)
	}
	cleaned := path.Clean(trimmed)
	if cleaned != trimmed {
		return "", fmt.Errorf("public path %q is not clean", trimmed)
	}
	if cleaned == "/" {
		return "", fmt.Errorf("public path must not be the root /")
	}
	return cleaned, nil
}
