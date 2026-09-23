package source

import (
	"context"
	"fmt"
)

// 引用态 secret 解析：SFTP 源经 config 的 credential_id 引用凭据库
// （ADR 0005）。引用与内联互斥——引用态的生效 secret 在远端客户端
// 构造时（OpenRemote / TestConnection 唯一凭据读取路径）从凭据库
// 解析，同步引擎、MCP 与文件浏览零改动透传。

// ReferencedKeySecret 是凭据引用解析出的 SSH 私钥 secret：私钥与其
// 解密口令不可分割。
type ReferencedKeySecret struct {
	PrivateKey           string
	PrivateKeyPassphrase string
}

// CredentialResolver 是凭据引用的校验与解析入口，由凭据域实现。
// source 域只依赖本接口，不接触凭据存储细节。
type CredentialResolver interface {
	// CredentialExists 报告凭据是否存在：引用态源在创建 / 更新时的
	// 存在性校验。
	CredentialExists(ctx context.Context, credentialID string) (bool, error)

	// ReferencedKeySecret 返回凭据的私钥 secret；凭据不存在时
	// found=false（存储故障返回错误）。
	ReferencedKeySecret(ctx context.Context, credentialID string) (ReferencedKeySecret, bool, error)
}

// validateCredentialReference 校验凭据引用目标存在：引用态源必须在
// 写入时指向真实凭据。resolver 未装配时跳过（测试便利；生产装配
// 必有，且引用失效在解析路径仍 fail-closed）。
func (s *Service) validateCredentialReference(ctx context.Context, cfg Config) error {
	if s.Credentials == nil || cfg.SFTP == nil || cfg.SFTP.CredentialID == "" {
		return nil
	}
	exists, err := s.Credentials.CredentialExists(ctx, cfg.SFTP.CredentialID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: referenced credential %q does not exist", ErrInvalid, cfg.SFTP.CredentialID)
	}
	return nil
}

// credentialsFor 返回源的生效凭据：引用态从凭据库解析私钥与口令
// 合成 SFTP 凭据，内联态取源自身 secret。凭据不存在是确定性失败
// （permanent）：源持有失效引用时同步明确报错，绝不静默跳过。
func (s *Service) credentialsFor(ctx context.Context, src Source) (Credentials, error) {
	creds, err := s.repo.GetCredentials(ctx, src.ID)
	if err != nil {
		return Credentials{}, err
	}
	if src.Config.SFTP == nil || src.Config.SFTP.CredentialID == "" {
		return creds, nil
	}
	if s.Credentials == nil {
		return Credentials{}, fmt.Errorf("%w: credential resolver is not configured", ErrInvalid)
	}
	ref, found, err := s.Credentials.ReferencedKeySecret(ctx, src.Config.SFTP.CredentialID)
	if err != nil {
		return Credentials{}, fmt.Errorf("resolve referenced credential %q: %w", src.Config.SFTP.CredentialID, err)
	}
	if !found {
		return Credentials{}, MarkPermanent(fmt.Errorf("%w: referenced credential %q not found", ErrInvalid, src.Config.SFTP.CredentialID))
	}
	creds.SFTP = &SFTPCredentials{
		PrivateKey:           ref.PrivateKey,
		PrivateKeyPassphrase: ref.PrivateKeyPassphrase,
	}
	return creds, nil
}
