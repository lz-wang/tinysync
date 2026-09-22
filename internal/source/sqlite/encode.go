package sqlite

import (
	"encoding/json"
	"fmt"

	"tinysync/internal/source"
)

// configJSON / credentialsJSON 是按协议单选的扁平 JSON 对象编解码：
// type 列承载判别符，JSON 内不重复协议组嵌套。编码形态是持久化
// 契约（migration 0005 backfill 同构），字段名冻结，只能增量新增。

// encodeConfig 把 typed config 编码为当前协议的扁平 JSON 对象。
func encodeConfig(t source.Type, c source.Config) (string, error) {
	var data any
	switch t {
	case source.TypeWebDAV:
		data = c.WebDAV
	case source.TypeS3:
		data = c.S3
	case source.TypeSFTP:
		data = c.SFTP
	case source.TypeGitHubRelease:
		data = c.GitHubRelease
	default:
		return "", fmt.Errorf("encode source config: %w: %q", source.ErrUnsupportedType, t)
	}
	if data == nil {
		return "{}", nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("encode %s config: %w", t, err)
	}
	return string(b), nil
}

// decodeConfig 按协议解码扁平 JSON 对象为 typed config。
func decodeConfig(t source.Type, raw string) (source.Config, error) {
	switch t {
	case source.TypeWebDAV:
		c := &source.WebDAVConfig{}
		if err := json.Unmarshal([]byte(raw), c); err != nil {
			return source.Config{}, fmt.Errorf("decode webdav config: %w", err)
		}
		return source.Config{WebDAV: c}, nil
	case source.TypeS3:
		c := &source.S3Config{}
		if err := json.Unmarshal([]byte(raw), c); err != nil {
			return source.Config{}, fmt.Errorf("decode s3 config: %w", err)
		}
		return source.Config{S3: c}, nil
	case source.TypeSFTP:
		c := &source.SFTPConfig{}
		if err := json.Unmarshal([]byte(raw), c); err != nil {
			return source.Config{}, fmt.Errorf("decode sftp config: %w", err)
		}
		return source.Config{SFTP: c}, nil
	case source.TypeGitHubRelease:
		c := &source.GitHubReleaseConfig{}
		if err := json.Unmarshal([]byte(raw), c); err != nil {
			return source.Config{}, fmt.Errorf("decode github_release config: %w", err)
		}
		return source.Config{GitHubRelease: c}, nil
	default:
		return source.Config{}, fmt.Errorf("decode source config: %w: %q", source.ErrUnsupportedType, t)
	}
}

// credentialsJSON 是凭据的持久化中间形态：按协议直接序列化对应
// secret 组（不嵌套协议判别符）。
type credentialsJSON struct {
	Password             *string `json:"password,omitempty"`
	SecretKey            *string `json:"secret_key,omitempty"`
	PrivateKey           *string `json:"private_key,omitempty"`
	PrivateKeyPassphrase *string `json:"private_key_passphrase,omitempty"`
	Token                *string `json:"token,omitempty"`
}

// encodeCredentials 把凭据集合编码为当前协议的扁平 JSON 对象。
func encodeCredentials(t source.Type, c source.Credentials) (string, error) {
	var raw credentialsJSON
	switch t {
	case source.TypeWebDAV:
		if c.WebDAV != nil {
			raw.Password = strPtr(c.WebDAV.Password)
		}
	case source.TypeS3:
		if c.S3 != nil {
			raw.SecretKey = strPtr(c.S3.SecretKey)
		}
	case source.TypeSFTP:
		if c.SFTP != nil {
			raw.Password = strPtr(c.SFTP.Password)
			raw.PrivateKey = strPtr(c.SFTP.PrivateKey)
			raw.PrivateKeyPassphrase = strPtr(c.SFTP.PrivateKeyPassphrase)
		}
	case source.TypeGitHubRelease:
		if c.GitHubRelease != nil {
			raw.Token = strPtr(c.GitHubRelease.Token)
		}
	default:
		return "", fmt.Errorf("encode source credentials: %w: %q", source.ErrUnsupportedType, t)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("encode %s credentials: %w", t, err)
	}
	return string(b), nil
}

// decodeCredentials 按协议解码扁平 JSON 对象为凭据集合。未知键忽略
// （向前兼容）。
func decodeCredentials(t source.Type, raw string) (source.Credentials, error) {
	var data credentialsJSON
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return source.Credentials{}, fmt.Errorf("decode source credentials: %w", err)
	}
	switch t {
	case source.TypeWebDAV:
		return source.Credentials{WebDAV: &source.WebDAVCredentials{
			Password: derefStr(data.Password),
		}}, nil
	case source.TypeS3:
		return source.Credentials{S3: &source.S3Credentials{
			SecretKey: derefStr(data.SecretKey),
		}}, nil
	case source.TypeSFTP:
		return source.Credentials{SFTP: &source.SFTPCredentials{
			Password:             derefStr(data.Password),
			PrivateKey:           derefStr(data.PrivateKey),
			PrivateKeyPassphrase: derefStr(data.PrivateKeyPassphrase),
		}}, nil
	case source.TypeGitHubRelease:
		return source.Credentials{GitHubRelease: &source.GitHubReleaseCredentials{
			Token: derefStr(data.Token),
		}}, nil
	default:
		return source.Credentials{}, fmt.Errorf("decode source credentials: %w: %q", source.ErrUnsupportedType, t)
	}
}

// strPtr 返回指向副本的指针；空串返回 nil，使空 secret 不写入
// JSON 键（与 backfill 的匿名语义一致）。
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
