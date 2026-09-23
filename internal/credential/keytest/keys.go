// Package keytest 提供测试用 SSH 私钥固定具：各层测试与 e2e 复用
// 同一形态的合法钥匙，避免在测试里手写 PEM。只在测试代码中导入。
package keytest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// UnencryptedEd25519 生成未加密 ed25519 私钥的 openssh PEM。
func UnencryptedEd25519() (string, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ed25519 key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "tinysync test key")
	if err != nil {
		return "", fmt.Errorf("marshal private key: %w", err)
	}
	return string(pem.EncodeToMemory(block)), nil
}

// EncryptedEd25519 生成带口令的 ed25519 私钥 PEM（openssh bcrypt KDF）。
func EncryptedEd25519(passphrase string) (string, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ed25519 key: %w", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "tinysync test key", []byte(passphrase))
	if err != nil {
		return "", fmt.Errorf("marshal encrypted private key: %w", err)
	}
	return string(pem.EncodeToMemory(block)), nil
}
