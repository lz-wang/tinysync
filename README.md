# TinySync

[![codecov](https://codecov.io/gh/lz-wang/tinysync/graph/badge.svg?token=2dXeeuTCQq)](https://codecov.io/gh/lz-wang/tinysync)

TinySync 是一个面向 HomeLab 的文件同步服务：单一 Go 二进制，内嵌 React Web UI，
通过 REST API 管理同步源（Sources）与同步任务（Jobs）。

> 当前开发版提供：持久化的多协议 Source 管理（WebDAV / S3 / SFTP，
> 创建、编辑、删除与连接测试），以及远端 → 本地单向同步 Job——
> Copy / Mirror 模式、include / exclude 过滤、原子下载与本地文件
> 归属保护（Mirror 只删除本 Job 管理的文件）。三种协议共用同一个
> 同步引擎。Job 支持自动调度（once / interval / cron，重叠自动
> 跳过）、受控并发（`--max-concurrent-jobs` /
> `--max-concurrent-transfers`）与持久化运行历史（每轮运行与
> 文件级变更明细经 Web UI 与 REST 可查，重启不丢）。文件访问与
> 发布：远端与本地文件浏览、文件下载与把同步后的受管本地文件
> 显式发布为受控 HTTP URL。
>
> **认证**：REST API 与 Web UI 全面 default-deny——除 health /
> version / 登录与 `/published` 公开文件外，所有端点都需要认证。
> 单一 Local Admin 经密码登录建立 Web Session（HttpOnly Cookie），
> 自动化脚本使用 scoped API Token（`Authorization: Bearer`）。
> 管理员密码未初始化时 `serve` 拒绝启动。
>
> **部署提示**：认证凭据应经 HTTPS 传输——反向代理场景请设置
> `X-Forwarded-Proto: https`，会话 Cookie 会自动附加 `Secure`；
> 纯 HTTP 部署仅建议用于本机或完全可信的网络。

## 技术栈

- 后端：Go + Gin，单二进制，无 CGO；SQLite（pure-Go driver）
- 前端：React + MUI，构建产物嵌入二进制
- 平台：Linux / macOS / Windows × amd64 / arm64

## 安装

从 [Releases](https://github.com/lz-wang/tinysync/releases) 下载对应平台的
压缩包（linux/darwin/windows × amd64/arm64），解压即用，无需任何运行时依赖。

## 快速开始

```bash
# 1. 初始化管理员密码（首次部署必需；未初始化时 serve 拒绝启动）
./tinysync auth set-password --datadir ./data

# 2. 启动服务
./tinysync serve --datadir ./data --port 9466
```

启动后打开 `http://127.0.0.1:9466`，用管理员密码登录 Web UI。

```bash
tinysync serve            # 启动服务（默认 :9466，数据目录 ./data）
tinysync auth set-password  # 初始化 / 重置管理员密码
tinysync version          # 打印版本号（同 --version）
tinysync --version        # 打印版本号
```

环境变量 `TINYSYNC_DATADIR`、`TINYSYNC_PORT`、`TINYSYNC_MAX_CONCURRENT_JOBS`、
`TINYSYNC_MAX_CONCURRENT_TRANSFERS` 可作为 `--datadir`、`--port`、
`--max-concurrent-jobs`（同时运行的同步 Job 数上限，默认 1）、
`--max-concurrent-transfers`（同时进行的远端文件下载上限，默认 4）
的默认值；命令行参数优先。

## 认证与 API Token

REST API 与 Web UI 默认拒绝匿名访问（401）；公开端点只有
`GET /api/v1/health`、`GET /api/v1/version`、`POST /api/v1/auth/login`
与 `/published/*path` 公开文件。

Web UI 使用 HttpOnly Session Cookie（7 天绝对过期、`SameSite=Strict`、
HTTPS 下自动 `Secure`）；凭据绝不进入 URL，跨源变更请求一律拒绝。
忘记密码由 operator 在服务器执行 `tinysync auth set-password --datadir ...`
重置（同时立即废弃全部已有会话）；不提供匿名 Web setup 与认证
绕过开关。

自动化脚本使用 API Token（`Authorization: Bearer`，唯一 machine
credential 入口），在 Web UI 的 API Tokens 页创建：

```bash
# 登录换取会话（Web UI 即此流程）
curl -i -X POST http://127.0.0.1:9466/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"password": "..."}'

# 创建只读 token（raw token 仅此一次返回，之后不可查询）
curl -X POST http://127.0.0.1:9466/api/v1/api-tokens \
  -H "Authorization: Bearer $TINYSYNC_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name": "automation", "scopes": ["read"], "expires_at": "2026-12-31T00:00:00Z"}'

# 用 token 访问 API
curl -H "Authorization: Bearer $TINYSYNC_TOKEN" \
  http://127.0.0.1:9466/api/v1/sources
```

Scope 语义（创建后不可变，变更需撤销重建）：

| Scope | 权限 |
| --- | --- |
| `read` | 查询 Source / Job / Run / File / Published metadata，下载文件 |
| `run` | 手动触发 Job（不含 read） |
| `admin` | 全部权限（read + run + 配置修改 + Token 管理） |

Token 可设过期时刻；撤销幂等且立即生效；`last_used_at` 以 1 分钟
阈值节流记录。`GET /api-tokens` 只返回 `prefix` 前缀等元数据，
raw token 与 SHA-256 摘要绝不出现。

### v0.6 → v0.7 升级

```text
1. 停止 v0.6 服务
2. 安装 v0.7 二进制
3. tinysync auth set-password --datadir <datadir>   # migration 0007 自动完成
4. 启动 v0.7：未初始化管理员密码时 serve 会拒绝启动
5. Web 登录；为既有脚本逐一创建 API Token
```

v0.6 的匿名脚本访问自 v0.7 起必须携带 Bearer Token；
`/published/*path` 公开语义不受升级影响。

## Sources：多协议同步源

Source 配置按协议分为非敏感 `config` 与 secret `credentials` 两组；
响应只回显 `credential_state` 布尔集合，任何 secret 永不回显。
Type 创建后不可变。

WebDAV：

```bash
curl -X POST http://127.0.0.1:9466/api/v1/sources \
  -H "Authorization: Bearer $TINYSYNC_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "NAS",
    "type": "webdav",
    "config": {"endpoint": "https://nas.example.com:5006/dav", "username": "user"},
    "credentials": {"password": "..."}
  }'
```

S3 / MinIO（自建 S3 用显式 endpoint 与 path-style；凭据只用 Source 自身的
static access key / secret key，不使用宿主机 ambient credential chain）：

```bash
curl -X POST http://127.0.0.1:9466/api/v1/sources \
  -H "Authorization: Bearer $TINYSYNC_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "backup-s3",
    "type": "s3",
    "config": {
      "endpoint": "https://s3.example.com",
      "region": "us-east-1",
      "bucket": "backup",
      "prefix": "tinysync",
      "path_style": true,
      "access_key": "AKID..."
    },
    "credentials": {"secret_key": "..."}
  }'
```

SFTP（host key 以 SHA256 fingerprint 严格校验，不支持跳过校验；
symlink 不跟随，发现即失败）：

```bash
curl -X POST http://127.0.0.1:9466/api/v1/sources \
  -H "Authorization: Bearer $TINYSYNC_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "nas-sftp",
    "type": "sftp",
    "config": {
      "host": "nas.example.com",
      "port": 22,
      "username": "user",
      "remote_root": "/srv/backups",
      "auth_method": "private_key",
      "host_key_fingerprint": "SHA256:UC1Dk4I9LLQOV3B8eZ5FlrUUcbbNie4INffe2TDTz3k"
    },
    "credentials": {"private_key": "...PEM...", "private_key_passphrase": "..."}
  }'
```

被 Sync Job 引用的 Source 拒绝修改 remote identity（WebDAV 的
endpoint + username、S3 的 endpoint / region / bucket / prefix /
path-style、SFTP 的 host / port / username / remote_root / host key
fingerprint），防止 Mirror 把既有本地文件误判为远端消失而删除；
secret 轮换始终允许。更换远端的正确路径是新建 Source 后切换 Job 的
source_id。

## Files：文件浏览与发布

Remote 浏览经 `GET /api/v1/sources/:id/files`（分页查询参数
`path` / `limit` / `cursor`，limit 默认 100、上限 500）分页浏览远端
目录；`.../files/stat` 与 `.../files/download` 提供元信息与流式下载。
本地浏览以 Job 为唯一入口：`GET /api/v1/jobs/:id/files` 只能访问该
Job 的 LocalRoot 之下的内容，条目携带 `managed` 标记（TinySync 当前
管理 vs 目录原有 / 已 relinquish 的文件）；本地下载支持 Range / HEAD。

发布策略把同步后的 managed 本地文件显式暴露为受控 URL：

```bash
curl -X POST http://127.0.0.1:9466/api/v1/published-files \
  -H "Authorization: Bearer $TINYSYNC_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "job_id": "job_xxx",
    "path": "/photos/a.jpg",
    "public_path": "/photos/a.jpg",
    "enabled": true
  }'
```

- 目标必须是该 Job 管理的普通文件（路径逃逸、symlink 与目录一律
  拒绝）；`local_path` 创建后不可变，要换文件就新建策略。
- 策略与 Job 生命周期解耦：Job 修改 LocalRoot 不会隐式改写既有
  URL；Mirror 删除文件后 URL 自然 404。
- 公开访问地址为 `/published/<public_path>`（同源拼接），支持
  Range / HEAD；禁用、过期或文件缺失统一返回 404，不区分原因；
  响应固定 `Cache-Control: no-store`。

## 版本机制

版本号只有一个事实来源：Git。

- HEAD 位于 `vX.Y.Z` tag 时，版本为 `X.Y.Z`；
- 普通开发提交为 `dev-<commit日期>-<commit7>`（日期取 commit date，构建可重复）。

由 Makefile 解析并通过 `-ldflags` 注入 `tinysync/internal/buildinfo.Version`，
`make version` 可查看当前解析结果。

## 开发

```bash
make setup     # 安装开发工具与依赖
make build     # 构建当前平台二进制（含 Web UI）
make serve     # 构建并启动
make test      # 运行测试
make check     # 静态检查
make help      # 查看全部 target
```

发布流程：更新 `CHANGELOG.md` 对应版本段 → 提交 → 打 `vX.Y.Z` tag 并推送，
Release workflow 自动完成构建、验证与 GitHub Release 发布。

## License

[MIT](LICENSE)
