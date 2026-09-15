# TinySync

[![codecov](https://codecov.io/gh/lz-wang/tinysync/graph/badge.svg?token=2dXeeuTCQq)](https://codecov.io/gh/lz-wang/tinysync)

TinySync 是一个面向 HomeLab 的文件同步服务：单一 Go 二进制，内嵌 React Web UI，
通过 REST API 管理同步源（Sources）与同步任务（Jobs）。

> 当前开发版提供：持久化的 WebDAV Source 管理（创建、编辑、删除与连接测试），
> 以及 WebDAV → 本地单向同步 Job——Copy / Mirror 模式、include / exclude
> 过滤、原子下载与本地文件归属保护（Mirror 只删除本 Job 管理的文件）。
> Job 支持自动调度（once / interval / cron，重叠自动跳过）、受控并发
> （`--max-concurrent-jobs` / `--max-concurrent-transfers`）与持久化
> 运行历史（每轮运行与文件级变更明细经 Web UI 与 REST 可查，重启不丢）。
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
