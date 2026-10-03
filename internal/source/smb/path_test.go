package smb

import (
	"errors"
	"testing"

	"tinysync/internal/source"
)

// remotePath：logical（已 ValidateLogicalPath）与 canonical root 映射
// 为 share 内 native 路径；root 自身映射为空串（share root），层级用
// SMB 分隔符，中间绝不出现 POSIX 分隔符。
func TestRemotePath(t *testing.T) {
	cases := []struct {
		root    string
		logical string
		want    string
	}{
		{root: "/", logical: "/", want: ""},
		{root: "/", logical: "/a.txt", want: `a.txt`},
		{root: "/", logical: "/docs/a.txt", want: `docs\a.txt`},
		{root: "/photos", logical: "/", want: `photos`},
		{root: "/photos", logical: "/2026/a.jpg", want: `photos\2026\a.jpg`},
		{root: "/photos", logical: "/2026", want: `photos\2026`},
		{root: "/a/b", logical: "/c", want: `a\b\c`},
		// Unicode 与空格文件名原样透传。
		{root: "/", logical: "/特别 目录/file name.txt", want: `特别 目录\file name.txt`},
	}
	for _, tc := range cases {
		got, err := remotePath(tc.root, tc.logical)
		if err != nil {
			t.Errorf("remotePath(%q, %q) error = %v", tc.root, tc.logical, err)
			continue
		}
		if got != tc.want {
			t.Errorf("remotePath(%q, %q) = %q, want %q", tc.root, tc.logical, got, tc.want)
		}
	}
}

// root escape 双重防御：clean logical path 经 path.Join 不可能逃逸
// canonical root，该防御保护 root 形态变化时不产生越界访问，对合法
// 输入透明（与 SFTP remoteAbs 对称）。
func TestRemotePathRootEscape(t *testing.T) {
	// root 非 canonical（带尾随斜杠）时防御分支生效，拒绝而不是静默
	// 拼出越界路径。
	if _, err := remotePath("/srv/backup/", "/a.txt"); err == nil {
		t.Error("remotePath with non-canonical root = no error, want rejection")
	} else if !errors.Is(err, source.ErrInvalid) {
		t.Errorf("remotePath non-canonical root error = %v, want ErrInvalid", err)
	}
}

// toLogical：远端条目名 → Source-relative logical path；非法名（反斜
// 杠、dot segment）在转换边界拒绝。
func TestToLogical(t *testing.T) {
	cases := []struct {
		parent string
		name   string
		want   string
	}{
		{parent: "/", name: "a.txt", want: "/a.txt"},
		{parent: "/", name: "docs", want: "/docs"},
		{parent: "/docs", name: "b.txt", want: "/docs/b.txt"},
		{parent: "/docs", name: "名 前.txt", want: "/docs/名 前.txt"},
	}
	for _, tc := range cases {
		got, err := toLogical(tc.parent, tc.name)
		if err != nil {
			t.Errorf("toLogical(%q, %q) error = %v", tc.parent, tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("toLogical(%q, %q) = %q, want %q", tc.parent, tc.name, got, tc.want)
		}
	}

	for name, tc := range map[string]struct{ parent, entry string }{
		"backslash":   {parent: "/", entry: `a\b`},
		"dot segment": {parent: "/", entry: "."},
		"dotdot":      {parent: "/docs", entry: ".."},
	} {
		if _, err := toLogical(tc.parent, tc.entry); err == nil {
			t.Errorf("toLogical %s = no error, want rejection", name)
		}
	}
}
