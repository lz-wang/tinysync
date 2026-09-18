package syncjob

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Windows 文件名规则：保留设备名（任意扩展名、大小写不敏感）、
// 非法字符、控制字符、尾随点 / 空格拒绝；普通名字接受。
func TestCheckWindowsFilename(t *testing.T) {
	rejected := []string{
		"CON", "con", "Con.txt", "aux", "AUX.tar.gz", "prn", "nul.dat",
		"COM1", "com9.md", "LPT1", "lpt4.bak",
		"a<b", "a>b", "a:b", `a"b`, "a|b", "a?b", "a*b",
		"bell\x07", "del\x7f",
		"dot.", "space ",
	}
	for _, name := range rejected {
		if err := checkWindowsFilename(name); err == nil {
			t.Errorf("checkWindowsFilename(%q) = nil, want error", name)
		}
	}
	accepted := []string{
		"notes.txt", "CON course.txt", "secondary", "auxiliary",
		"报告-v2.final.md", "a.b.c", "data 2026.csv", "dot.name.x",
	}
	for _, name := range accepted {
		if err := checkWindowsFilename(name); err != nil {
			t.Errorf("checkWindowsFilename(%q) = %v, want nil", name, err)
		}
	}
}

// 策略开关：Windows 规则只在启用时生效（Linux / macOS 上 CON 是
// 合法文件名，同步不应被误伤）。
func TestFilenamePolicySwitch(t *testing.T) {
	off := FilenamePolicy{}
	if err := off.check("CON"); err != nil {
		t.Errorf("policy without Windows rules rejected CON: %v", err)
	}
	on := FilenamePolicy{WindowsNames: true}
	if err := on.check("CON"); err == nil {
		t.Error("policy with Windows rules accepted CON")
	}
	// 默认策略跟随平台。
	d := DefaultFilenamePolicy()
	if d.WindowsNames != (runtime.GOOS == "windows") {
		t.Errorf("DefaultFilenamePolicy.WindowsNames = %v, want %v", d.WindowsNames, runtime.GOOS == "windows")
	}
}

// planMappedRelPaths 汇总 download / update / skip / delete 全部
// 映射路径并去重排序。
func TestPlanMappedRelPaths(t *testing.T) {
	plan := Plan{
		Downloads: []planEntry{{relPath: "b.txt"}, {relPath: "a.txt"}},
		Updates:   []planEntry{{relPath: "a.txt"}}, // 与 download 重复
		Skips:     []planEntry{{relPath: "c.txt"}},
		Deletes:   []string{"d.txt", "a.txt"},
	}
	got := planMappedRelPaths(plan)
	want := []string{"a.txt", "b.txt", "c.txt", "d.txt"}
	if len(got) != len(want) {
		t.Fatalf("planMappedRelPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("planMappedRelPaths = %v, want %v", got, want)
		}
	}
}

// case 冲突检测：case-insensitive 时仅大小写不同的路径整体失败；
// case-sensitive 时允许共存。
func TestCheckCaseCollisions(t *testing.T) {
	rels := []string{"docs/Foo.txt", "docs/foo.txt", "other.txt"}
	err := checkCaseCollisions(rels, true)
	if err == nil {
		t.Fatal("case collision on insensitive fs = nil, want error")
	}

	if err := checkCaseCollisions(rels, false); err != nil {
		t.Errorf("same-case paths on sensitive fs = %v, want nil", err)
	}

	if err := checkCaseCollisions([]string{"a.txt", "b.txt"}, true); err != nil {
		t.Errorf("distinct paths on insensitive fs = %v, want nil", err)
	}
}

// PreflightFilesystemCompat 端到端：真实文件系统上，case-insensitive
// 时冲突计划失败、无冲突计划通过；文件名规则独立于大小写语义。
func TestPreflightFilesystemCompat(t *testing.T) {
	root := t.TempDir()
	insensitive, err := rootIsCaseInsensitive(root)
	if err != nil {
		t.Fatalf("probe case sensitivity: %v", err)
	}

	plan := Plan{Downloads: []planEntry{{relPath: "docs/Foo.txt"}, {relPath: "docs/foo.txt"}}}
	err = PreflightFilesystemCompat(root, plan, FilenamePolicy{})
	if insensitive && err == nil {
		t.Error("collision plan on insensitive fs = nil, want error")
	}
	if !insensitive && err != nil {
		t.Errorf("collision plan on sensitive fs = %v, want nil", err)
	}

	okPlan := Plan{Downloads: []planEntry{{relPath: "docs/唯一.txt"}}}
	if err := PreflightFilesystemCompat(root, okPlan, FilenamePolicy{WindowsNames: true}); err != nil {
		t.Errorf("clean plan = %v, want nil", err)
	}

	badPlan := Plan{Downloads: []planEntry{{relPath: "docs/CON.txt"}}}
	err = PreflightFilesystemCompat(root, badPlan, FilenamePolicy{WindowsNames: true})
	if err == nil {
		t.Error("reserved-name plan = nil, want error")
	}

	// LocalRoot 不存在：跳过大小写探测但文件名规则仍然生效。
	missing := filepath.Join(root, "not-created")
	if err := PreflightFilesystemCompat(missing, badPlan, FilenamePolicy{WindowsNames: true}); err == nil {
		t.Error("reserved-name plan on missing root = nil, want error")
	}
	if err := PreflightFilesystemCompat(missing, okPlan, FilenamePolicy{}); err != nil {
		t.Errorf("clean plan on missing root = %v, want nil", err)
	}
}

// flipCase 翻转字符串中第一个字母的大小写（含扩展名内的字母）。
func TestFlipCase(t *testing.T) {
	cases := map[string]string{
		"abc.txt": "Abc.txt",
		"ABC.txt": "aBC.txt",
		"123.txt": "123.Txt",
	}
	for in, want := range cases {
		if got := flipCase(in); got != want {
			t.Errorf("flipCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// 引擎集成：case-insensitive 文件系统上，冲突计划必须整轮失败且
// 零本地变更（先于任何 mutation）。case-sensitive 文件系统上同名
// 不同大小写可共存。
func TestRunAbortsOnCaseCollisionBeforeMutation(t *testing.T) {
	f := newEngineFixture(t, ModeCopy)
	insensitive, err := rootIsCaseInsensitive(f.root)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !insensitive {
		t.Skip("local root is case-sensitive; collision abort semantics not applicable here")
	}

	remote := buildRemote(map[string]string{
		"/Foo.txt": "upper",
		"/foo.txt": "lower",
	}, nil)

	_, runErr := f.run(remote)
	if runErr == nil {
		t.Fatal("run with case collision = nil, want preflight failure")
	}
	// 零本地变更：LocalRoot 内不出现任何文件。
	entries, readErr := os.ReadDir(f.root)
	if readErr != nil {
		t.Fatalf("readdir: %v", readErr)
	}
	for _, e := range entries {
		t.Errorf("local mutation happened before preflight failure: %s", e.Name())
	}
}
