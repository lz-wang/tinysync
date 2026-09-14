# TinySync

TinySync 是一个面向 HomeLab 的文件同步服务：单一 Go 二进制，内嵌 React Web UI，
通过 REST API 管理同步源（Sources）与同步任务（Jobs）。

> v0.1.0 为工程基线版本：版本机制、HTTP 服务、Web UI 骨架与 CI/CD
> 已建立；Source / Sync Job 等同步领域能力尚未实现。

## 技术栈

- 后端：Go + Gin，单二进制，无 CGO
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

环境变量 `TINYSYNC_DATADIR`、`TINYSYNC_PORT` 可作为 `--datadir`、`--port`
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
