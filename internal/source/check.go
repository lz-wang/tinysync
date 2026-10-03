package source

import (
	"context"
	"fmt"
)

// CheckSFTPInput 检查当前表单，不持久化配置。编辑时只沿用 SourceID 的
// 内联凭据；显式提供的 secret 临时替换或清除，引用按提案配置解析。
type CheckSFTPInput struct {
	SourceID    string
	Config      SFTPConfig
	Credentials *SFTPCredentialsUpdate
}

// CheckSFTP 验证配置、认证与远端根目录的存在性、目录类型及读取权限。
// 与连接测试一致，远端失败以 OK=false 表达；请求错误单独返回。
func (s *Service) CheckSFTP(ctx context.Context, input CheckSFTPInput) (TestResult, error) {
	start := s.Now()
	checkCtx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()

	cfg := Config{SFTP: &input.Config}
	if err := ValidateConfig(TypeSFTP, cfg); err != nil {
		return TestResult{}, err
	}
	cfg = cfg.Normalized(TypeSFTP)
	inline := SFTPCredentials{}
	if input.SourceID != "" {
		stored, err := s.repo.Get(checkCtx, input.SourceID)
		if err != nil {
			return TestResult{}, err
		}
		if stored.Type != TypeSFTP {
			return TestResult{}, fmt.Errorf("%w: check requires an sftp source", ErrInvalid)
		}
		creds, err := s.repo.GetCredentials(checkCtx, input.SourceID)
		if err != nil {
			return TestResult{}, err
		}
		if creds.SFTP != nil && cfg.SFTP.CredentialID == "" {
			inline = *creds.SFTP
		}
	}
	if update := input.Credentials; update != nil {
		if update.Password != nil {
			inline.Password = *update.Password
		}
		if update.PrivateKey != nil {
			inline.PrivateKey = *update.PrivateKey
		}
		if update.PrivateKeyPassphrase != nil {
			inline.PrivateKeyPassphrase = *update.PrivateKeyPassphrase
		}
	}
	creds := Credentials{SFTP: &inline}
	if err := ValidateCredentials(TypeSFTP, cfg, creds); err != nil {
		return TestResult{}, err
	}
	creds, err := s.resolveCredentialReference(checkCtx, cfg, creds)
	if err != nil {
		return TestResult{}, err
	}
	failed := func(err error) TestResult {
		return TestResult{LatencyMS: s.Now().Sub(start).Milliseconds(), Error: err.Error()}
	}
	remote, err := s.factory.Create(checkCtx, Source{Type: TypeSFTP, Config: cfg}, creds)
	if err != nil {
		return failed(err), nil
	}
	defer func() { _ = remote.Close() }()
	info, err := remote.Stat(checkCtx, "/")
	if err != nil {
		return failed(err), nil
	}
	if !info.IsDir {
		return failed(fmt.Errorf("sftp remote root is not a directory")), nil
	}
	if _, err := remote.List(checkCtx, "/", ListOptions{Limit: 1}); err != nil {
		return failed(err), nil
	}
	return TestResult{OK: true, LatencyMS: s.Now().Sub(start).Milliseconds()}, nil
}
