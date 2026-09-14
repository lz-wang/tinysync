# v0.1.0 — 工程与发布基线

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。本页勾选项表示仓库中已有实现或配置，不代表远端工作流已验证通过。

目标：建立后续领域开发依赖的稳定工程底座。

## Runtime

- [x] Go 1.26 工程。
- [x] `main -> cmd -> app -> api` 分层。
- [x] Gin HTTP server。
- [x] `serve` 命令。
- [x] `version` / `--version`。
- [x] `--datadir` / `TINYSYNC_DATADIR`。
- [x] `--port` / `TINYSYNC_PORT`。
- [x] SIGINT / SIGTERM 优雅关闭。
- [x] zap + lumberjack 日志。
- [x] 全工程禁止 CGO。

## HTTP

- [x] `GET /api/v1/health`。
- [x] `GET /api/v1/version`。
- [x] API 404 与 SPA fallback 正确隔离。
- [x] Method Not Allowed 处理。
- [x] 静态资源缓存策略。
- [x] HTTP server timeout 基线。

## Web UI

- [x] React。
- [x] TypeScript。
- [x] MUI。
- [x] Vite。
- [x] Biome。
- [x] 服务状态与版本展示。
- [x] WebUI 内嵌 Go binary。
- [x] `webui` build tag / fallback 构建机制。

## Engineering

- [x] Git 驱动版本号。
- [x] Keep a Changelog。
- [x] Semantic Versioning。
- [x] Makefile 统一工程入口。
- [x] Go tests。
- [x] Codecov。
- [x] Linux / macOS / Windows native smoke。
- [x] Linux / macOS / Windows × amd64 / arm64 cross-build。
- [x] Build / Release GitHub Actions 分离。
- [x] 开发版 WebDAV 镜像。
- [x] 正式版 WebDAV 镜像机制。
- [x] 移除 GitHub Actions Artifact 依赖。
- [ ] 核实 `v0.1.0` Release workflow、发行资产与校验和，并记录远端验收结果。

## 完成标准

```text
v0.1.0 tag
  → release validation
  → make ci
  → 3-platform native smoke
  → 6-platform dist
  → checksums.txt
  → GitHub Release
```

GitHub Release 成功后执行 WebDAV mirror；镜像失败不阻断发布。
Pushover 按工作流结果通知，未配置凭证时跳过。发布与验收规则见
[构建与发布](../guides/release.md)。本地已有 tag 不等于上述远端链路已通过。
