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
> 文件级变更明细经 Web UI 与 REST 可查，重启不丢）。
>
> **安全提示**：TinySync 尚未实现自身的认证与鉴权，Source / Job 管理
> 与同步运行 API 无任何访问控制，请仅部署在可信的 HomeLab 网络中。

## 技术栈

- 后端：Go + Gin，单二进制，无 CGO；SQLite（pure-Go driver）
- 前端：React + MUI，构建产物嵌入二进制
- 平台：Linux / macOS / Windows × amd64 / arm64

## 安装

从 [Releases](https://github.com/lz-wang/tinysync/releases) 下载对应平台的
压缩包（linux/darwin/windows × amd64/arm64），解压即用，无需任何运行时依赖。

## 快速开始

```bash
./tinysync serve --datadir ./data --port 9466
```

启动后打开 `http://127.0.0.1:9466` 查看 Web UI。

```bash
tinysync serve      # 启动服务（默认 :9466，数据目录 ./data）
tinysync version    # 打印版本号（同 --version）
tinysync --version  # 打印版本号
```

环境变量 `TINYSYNC_DATADIR`、`TINYSYNC_PORT`、`TINYSYNC_MAX_CONCURRENT_JOBS`、
`TINYSYNC_MAX_CONCURRENT_TRANSFERS` 可作为 `--datadir`、`--port`、
`--max-concurrent-jobs`（同时运行的同步 Job 数上限，默认 1）、
`--max-concurrent-transfers`（同时进行的远端文件下载上限，默认 4）
的默认值；命令行参数优先。

## Sources：多协议同步源

Source 配置按协议分为非敏感 `config` 与 secret `credentials` 两组；
响应只回显 `credential_state` 布尔集合，任何 secret 永不回显。
Type 创建后不可变。

WebDAV：

```bash
curl -X POST http://127.0.0.1:9466/api/v1/sources \
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
