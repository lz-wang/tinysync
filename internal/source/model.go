// Package source 承载 Source 领域：模型、校验、持久化接口与应用服务。
// Source 是对异构远端（WebDAV / S3 / SFTP）的统一只读抽象；
// 领域对象不携带密码，密码仅经输入结构与 Repository 凭据查询流转。
package source

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Type 是 Source 的协议类型。
type Type string

// TypeWebDAV 是 v0.2.0 唯一支持的协议类型。
const TypeWebDAV Type = "webdav"

// 领域哨兵错误：各实现（Repository、Service）必须以 errors.Is 判定。
var (
	// ErrNotFound 表示目标 Source 不存在。
	ErrNotFound = errors.New("source not found")
	// ErrConflict 表示 name 与现有 Source 冲突。
	ErrConflict = errors.New("source name already exists")
	// ErrInvalid 表示输入校验失败。
	ErrInvalid = errors.New("invalid source")
	// ErrUnsupportedType 表示协议类型暂未支持。
	ErrUnsupportedType = errors.New("unsupported source type")
)

// Source 是 Source 的领域对象。刻意不包含 Password 字段：
// 密码只存在于 CreateInput / UpdateInput、Repository 凭据查询
// 与远端客户端构造路径中，从结构层避免 API 与日志意外泄漏。
type Source struct {
	ID          string
	Name        string
	Type        Type
	Endpoint    string
	Username    string
	PasswordSet bool
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// idPrefix 是 Source ID 的固定前缀，便于在日志与 API 中一眼识别。
const idPrefix = "src_"

// newIDSize 是随机部分的字节数（128 bit）。
const newIDSize = 16

// NewID 生成 src_<128-bit random hex> 形式的唯一 ID，不引入 UUID 依赖。
func NewID() (string, error) {
	buf := make([]byte, newIDSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate source id: %w", err)
	}
	return idPrefix + hex.EncodeToString(buf), nil
}

// CreateInput 是创建 Source 的输入；Password 可为空（匿名访问）。
type CreateInput struct {
	Name     string
	Type     Type
	Endpoint string
	Username string
	Password string
	Enabled  bool
}

// UpdateInput 是更新 Source 的输入，指针字段区分「未提供」与「零值」：
// nil 表示保留现有值；password 语义为 nil 保留、空串清除、非空替换。
type UpdateInput struct {
	Name     *string
	Endpoint *string
	Username *string
	Password *string
	Enabled  *bool
}
