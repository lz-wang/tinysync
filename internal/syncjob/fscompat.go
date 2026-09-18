package syncjob

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// FilenamePolicy 描述本地映射的文件名兼容性规则。Windows 保留名与
// 非法字符按目标平台语义启用（GOOS=windows）；纯函数
// checkWindowsFilename 可在任何平台测试。这是 local mapping 限制，
// 不进入 source.ValidateLogicalPath 的远端协议模型：远端允许存在的
// 文件，本地文件系统无法表示时必须在 mutation 前整体失败。
type FilenamePolicy struct {
	// WindowsNames 启用 Windows 保留设备名与非法字符检查。
	WindowsNames bool
}

// DefaultFilenamePolicy 返回当前平台的本地映射规则。
func DefaultFilenamePolicy() FilenamePolicy {
	return FilenamePolicy{WindowsNames: runtime.GOOS == "windows"}
}

// windowsReservedNames 是 Windows 保留设备名：任意扩展名组合
// （CON.txt、aux.tar.gz）都不可创建，按「主名」匹配（大小写不敏感）。
var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// windowsForbiddenChars 是 Windows 文件名非法字符。
const windowsForbiddenChars = `<>:"|?*`

// check 校验单个路径组件；nil 表示当前策略下合法。
func (p FilenamePolicy) check(component string) error {
	if p.WindowsNames {
		return checkWindowsFilename(component)
	}
	return nil
}

// checkWindowsFilename 校验 Windows 文件名组件：保留设备名（可带
// 扩展名）、非法字符、控制字符、尾随点与尾随空格一律拒绝。
func checkWindowsFilename(component string) error {
	if component == "" {
		return fmt.Errorf("filename component is empty")
	}
	// 主名 = 第一个点之前的部分（CON.txt 的主名是 CON）。
	base, _, _ := strings.Cut(component, ".")
	if windowsReservedNames[strings.ToUpper(base)] {
		return fmt.Errorf("filename %q uses a reserved Windows device name", component)
	}
	for _, r := range component {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("filename %q contains a control character", component)
		}
		if strings.ContainsRune(windowsForbiddenChars, r) {
			return fmt.Errorf("filename %q contains character %q which is invalid on Windows", component, r)
		}
	}
	if strings.HasSuffix(component, ".") {
		return fmt.Errorf("filename %q ends with a dot", component)
	}
	if strings.HasSuffix(component, " ") {
		return fmt.Errorf("filename %q ends with a space", component)
	}
	return nil
}

// PreflightFilesystemCompat 在任何本地 mutation 之前验证本轮计划的
// 本地映射与实际文件系统的兼容性，位于「完整 remote scan → selector
// → build plan → preflight → mutation」链上，任何失败使整轮失败、
// 零本地变更：
//   - 平台文件名规则（Windows 保留名 / 非法字符）fail-fast；
//   - case-insensitive 文件系统上，本轮计划内出现仅大小写不同的
//     路径冲突（Foo.txt / foo.txt）时整体失败——不允许后下载覆盖
//     先下载的平台相关结果。
//
// 校验覆盖计划内全部本地映射路径（download / update / skip /
// delete）。LocalRoot 不存在时跳过大小写探测（文件名规则仍然生效；
// 此时新建目录的大小写语义由首次下载的原子替换兜底——冲突在
// O_EXCL 创建处失败，同样不会静默覆盖）。
func PreflightFilesystemCompat(localRoot string, plan Plan, policy FilenamePolicy) error {
	rels := planMappedRelPaths(plan)
	for _, rel := range rels {
		for _, component := range strings.Split(rel, "/") {
			if err := policy.check(component); err != nil {
				return fmt.Errorf("local mapping %q: %w", rel, err)
			}
		}
	}
	caseInsensitive, err := rootIsCaseInsensitive(localRoot)
	if err != nil {
		return fmt.Errorf("probe local root case sensitivity: %w", err)
	}
	return checkCaseCollisions(rels, caseInsensitive)
}

// planMappedRelPaths 汇总计划内全部本地映射相对路径并排序（去重）：
// 冲突判定必须覆盖本轮所有会触碰本地的条目。
func planMappedRelPaths(plan Plan) []string {
	seen := make(map[string]bool)
	rels := make([]string, 0, len(plan.Downloads)+len(plan.Updates)+len(plan.Skips)+len(plan.Deletes))
	add := func(entries []planEntry) {
		for _, e := range entries {
			if !seen[e.relPath] {
				seen[e.relPath] = true
				rels = append(rels, e.relPath)
			}
		}
	}
	add(plan.Downloads)
	add(plan.Updates)
	add(plan.Skips)
	for _, rel := range plan.Deletes {
		if !seen[rel] {
			seen[rel] = true
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	return rels
}

// checkCaseCollisions 在 case-insensitive 文件系统上检测本轮计划内
// 仅大小写不同的路径冲突。
func checkCaseCollisions(rels []string, caseInsensitive bool) error {
	if !caseInsensitive {
		return nil
	}
	seen := make(map[string]string, len(rels))
	for _, rel := range rels {
		lower := strings.ToLower(rel)
		if prev, dup := seen[lower]; dup {
			return fmt.Errorf(
				"local filesystem is case-insensitive; remote contains %q and %q which map to the same local path; run aborted before any change",
				prev, rel)
		}
		seen[lower] = rel
	}
	return nil
}

// rootIsCaseInsensitive 探测 LocalRoot 的实际大小写敏感性：在 root
// 下创建带随机后缀的探针文件，再以翻转大小写的名称 Lstat。root
// 不存在时返回 false（大小写敏感假设，见 PreflightFilesystemCompat
// 的兜底说明）。
func rootIsCaseInsensitive(root string) (bool, error) {
	if _, err := os.Lstat(root); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return false, fmt.Errorf("generate probe name: %w", err)
	}
	f, err := os.CreateTemp(root, ".tinysync-case-probe-"+hex.EncodeToString(buf)+"-*")
	if err != nil {
		return false, err
	}
	name := filepath.Base(f.Name())
	_ = f.Close()
	defer func() { _ = os.Remove(f.Name()) }()

	flipped := flipCase(name)
	if _, err := os.Lstat(filepath.Join(root, flipped)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// flipCase 翻转字符串中第一个字母的大小写；无字母时原样返回。
func flipCase(s string) string {
	for i, r := range s {
		if r >= 'a' && r <= 'z' {
			return s[:i] + string(r-'a'+'A') + s[i+1:]
		}
		if r >= 'A' && r <= 'Z' {
			return s[:i] + string(r-'A'+'a') + s[i+1:]
		}
	}
	return s
}
