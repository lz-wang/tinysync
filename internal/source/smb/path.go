package smb

import (
	"fmt"
	"path"
	"strings"

	"tinysync/internal/source"
)

// 路径模型：tinysync 内部只存在 POSIX 风格 logical path（"/" 表示
// Source root），SMB 反斜杠 native path 只在 adapter 边界内出现。
// share 由 tree connect 绑定，native path 是 share 内的相对路径
//（go-smb2 拒绝前导 '\'），share root 表示为空串。中间拼接一律用
// path 包而非 filepath——Windows build 下 filepath.Join 会改变路径
// 语义，这也是 SFTP adapter 的同一约定。

// remotePath 把 canonical root 与 logical path 映射为 share 内的 SMB
// native 路径。logical 必须已通过 ValidateLogicalPath（clean 绝对
// 路径），root 必须是归一化后的 canonical form（"/" 或 "/a/b"）；
// 二者经 path 包拼接后仍显式验证结果落在 root 之内——双重防御
// root escape（canonical 输入下恒通过，保护 root 形态异常时不产生
// 越界映射），与 SFTP 的 remoteAbs 对称。
func remotePath(root, logical string) (string, error) {
	cleaned := path.Clean(logical)
	abs := path.Join(root, cleaned)
	// root == "/" 时 root+"/" 是 "//"，前缀规则需要特例：Join 结果
	// 以 / 开头即不可能逃逸 share。
	if abs != root && root != "/" && !strings.HasPrefix(abs, root+"/") {
		return "", fmt.Errorf("%w: smb path %q escapes remote root %q", source.ErrInvalid, logical, root)
	}
	if abs == "" || abs[0] != '/' {
		return "", fmt.Errorf("%w: smb path %q resolves outside remote root %q", source.ErrInvalid, logical, root)
	}
	// 去掉前导 /：share 内相对路径；root 自身映射为空串（share root）。
	rel := strings.TrimPrefix(abs, "/")
	// 最后一步才转换为 SMB native 分隔符。
	return strings.ReplaceAll(rel, "/", `\`), nil
}

// toLogical 把远端条目名转换为 Source-relative logical path。条目名
// 必须是单段：空名、dot segment 与含分隔符的名字显式拒绝——
// name=".." 依赖 ValidateLogicalPath 无法拦截（Join 归一后恰好落在
// root 上），name="a/b" 会静默变成子路径，都在转换边界 fail-closed。
func toLogical(dirLogical, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("%w: invalid smb entry name %q", source.ErrInvalid, name)
	}
	if dirLogical == "/" {
		dirLogical = ""
	}
	logical := "/" + strings.TrimPrefix(path.Join(dirLogical, name), "/")
	if err := source.ValidateLogicalPath(logical); err != nil {
		return "", err
	}
	return logical, nil
}
