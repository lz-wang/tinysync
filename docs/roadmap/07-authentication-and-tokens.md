# v0.7.0 — Authentication & API Tokens

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。以下均为规划；模型、接口和路由示例用于设计讨论，不代表当前可用契约。

目标：建立 Web 管理端和程序化 API 的安全边界。

## Web UI Authentication

Web UI 与 API token 分离。

考虑：

```text
session / cookie
```

不要用长期 API token 直接充当浏览器 session。

## API Token

体验类似 LLM provider：

创建时：

```text
显示 raw token 一次
```

数据库只保存：

```text
SHA-256(random high-entropy token)
```

API token 是高熵 secret，不使用慢速 password KDF。

模型：

```text
api_tokens
├── id
├── name
├── prefix
├── token_hash
├── scopes
├── created_at
├── expires_at
├── last_used_at
└── revoked_at
```

## Scopes

第一阶段可以：

```text
read
run
admin
```

后续细化：

```text
sources:read
jobs:read
jobs:run
files:read
...
```

## Web UI

- [ ] Token list。
- [ ] Create。
- [ ] Raw token one-time display。
- [ ] Revoke。
- [ ] Expiration。
- [ ] Last used。
- [ ] Scope selection。

## 完成标准

> REST API 可以安全暴露给自动化脚本和本地服务，而不共享 WebUI 登录凭据。
