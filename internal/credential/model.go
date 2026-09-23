// Package credential 承载凭据领域：模型、校验、持久化接口与应用服务。
// Credential 是一条命名的 secret 记录，独立于任何单个同步源存在，
// 可被多个源同时引用（ADR 0005）。领域对象不携带 secret 明文：
// secret 只经 Secret 输入结构与 Repository 流转，从结构层避免 API
// 与日志意外泄漏。本版本只实现 ssh_key 类型——私钥与其解密口令是
// 不可分割的整体，随凭据存取；「引用与内联互斥」的约束由 Source
// 域校验承担，凭据域不感知引用方的协议细节。
package credential

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Type 是凭据的类型。创建后不可变。
type Type string

// 支持的凭据类型。本版本只有 ssh_key：协议无关的实体形态先落地，
// 其他协议凭据（WebDAV 密码、GitHub Token 等）按需挂入。
const TypeSSHKey Type = "ssh_key"

// Credential 是凭据的领域对象：命名 + 类型 + 公钥指纹 + 口令状态。
// 刻意不包含 secret 明文字段：secret 只存在于 Secret 输入结构与
// Repository 存储路径中。
type Credential struct {
	ID   string
	Name string
	Type Type
	// Fingerprint 是由私钥派生的公钥 SHA256 指纹（SHA256:<base64>），
	// 用于在人面前区分多把钥匙；不参与任何校验决策，与主机密钥指纹
	// 无关。
	Fingerprint string
	// HasPassphrase 回显私钥是否带解密口令；口令是私钥不可分割的
	// 一部分，随凭据整体存取，不逐源覆盖。
	HasPassphrase bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Secret 是凭据的 secret：仅在 Create / Update 输入与 Repository
// 存储路径流转，绝不进入 Credential 对象与 API 响应。
type Secret struct {
	PrivateKey           string
	PrivateKeyPassphrase string
}

// SourceRef 是引用凭据的同步源轻量标识，供删除守卫回显与引用计数。
type SourceRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// idPrefix 是 Credential ID 的固定前缀，便于在日志与 API 中一眼识别。
const idPrefix = "crd_"

// newIDSize 是随机部分的字节数（128 bit）。
const newIDSize = 16

// NewID 生成 crd_<128-bit random hex> 形式的唯一 ID，不引入 UUID 依赖。
func NewID() (string, error) {
	buf := make([]byte, newIDSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate credential id: %w", err)
	}
	return idPrefix + hex.EncodeToString(buf), nil
}

// CreateInput 是创建凭据的输入。Secret 必填（ssh_key 无匿名形态）。
type CreateInput struct {
	Name   string
	Type   Type
	Secret Secret
}

// UpdateInput 是更新凭据的输入，指针字段区分「未提供」与「零值」：
// Name 为 nil 保留；Secret 为 nil 保留现有 secret，非 nil 即整体
// 替换——私钥与口令一体，部分替换没有现实语义，不设三态。
type UpdateInput struct {
	Name   *string
	Secret *Secret
}
