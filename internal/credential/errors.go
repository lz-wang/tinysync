package credential

import (
	"errors"
	"fmt"
)

// 领域哨兵错误：各实现（Repository、Service）必须以 errors.Is 判定。
var (
	// ErrNotFound 表示目标凭据不存在。
	ErrNotFound = errors.New("credential not found")
	// ErrConflict 表示 name 与现有凭据冲突。
	ErrConflict = errors.New("credential name already exists")
	// ErrInvalid 表示输入校验失败（含私钥解析失败）。
	ErrInvalid = errors.New("invalid credential")
	// ErrUnsupportedType 表示凭据类型暂未支持。
	ErrUnsupportedType = errors.New("unsupported credential type")
)

// ErrInUse 是删除被引用凭据的冲突：携带引用源清单供 API 以 409 回显，
// 由用户先解绑再删除——不做强制级联，静默产生失去认证的源。
type ErrInUse struct {
	Sources []SourceRef
}

// Error 实现 error。
func (e *ErrInUse) Error() string {
	return fmt.Sprintf("credential is referenced by %d source(s)", len(e.Sources))
}
