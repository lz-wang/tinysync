# AGENTS.md

本文件是仓库协作入口，只保留所有任务都需要遵守的规则。

- 面向用户的命令契约与示例：`README.md`
- 未发布与已发布变更记录：`CHANGELOG.md`

## 工作原则

- 开始前先阅读 `README.md` 与 `CHANGELOG.md`，确认当前命令契约与未发布变更。
- 先理解现有调用链和测试，再做范围最小、可验证的修改。
- 提交信息遵循 Conventional Commits：type 为英文，正文用中文补充说明。
- 不提交密钥、令牌、个人环境配置、构建产物或覆盖率文件。
- 未实现的能力必须标注为规划，不得在文档中宣称已经可用。
- 用户可见变化必须同步写入 `CHANGELOG.md` 的 `[Unreleased]`，只使用
  `新增` / `修复` / `移除` 三个分类。

## 架构边界

- 启动链路固定为 `main -> cmd -> app -> api`：`main` 只做 signal context
  与前端资源嵌入，`internal/cmd` 负责 CLI 解析，`internal/app` 是
  composition root，`internal/api` 承载 HTTP 路由与生命周期。
- 版本号唯一事实来源是 Git：Makefile 解析后经
  `-ldflags "-X tinysync/internal/buildinfo.Version=..."` 注入。
  不引入 VERSION 文件、package version、config version 等第二事实来源。
- Web 构建产物由 Go 嵌入：`-tags webui` 嵌入 `web/dist`，无 tag 时嵌入
  `web/fallback`，保证无 Node 环境可完整编译与测试 Go 代码。
- 未知 `/api/v1/*` 路径必须 404，不得被 SPA fallback 吞掉；前后端 API
  契约变更必须同时更新两端实现与测试。
- 配置只保留 `--datadir` / `--port`（env：`TINYSYNC_DATADIR` /
  `TINYSYNC_PORT`），新增配置项必须先有明确用途。

## Go 约定

- 使用 `gofmt`（Tab 缩进），标准库优先的导入顺序；静态检查走 `go vet`
  与 `make check`。
- 包保持职责单一；除入口装配外，业务逻辑放在 `internal/`。
- 错误应包含操作与目标上下文，并保留底层错误以支持 `errors.Is/As`。
- HTTP handler 与生命周期必须覆盖成功与失败路径测试。

## Web 约定

- 使用严格 TypeScript，不以 `any` 或非空断言绕过边界校验。
- 使用 Biome 统一检查与格式化（4 空格、单引号、按需分号）。
- React 只负责 UI；API 访问统一走 `web/src/api.ts` 的类型化客户端。

## CI/CD 边界

- `build.yml`（main push / PR）验证"当前代码是否健康"：静态检查、单测、
  Codecov 覆盖率、六平台构建、三平台原生 Smoke。不生成 GitHub Release。
- `release.yml`（tag push / 手动指定 tag）发布"已明确版本号的不可变
  Git tag"：校验 tag 与 `make version` 一致、CHANGELOG 存在对应段落、
  三平台 Smoke，publish 在单一 runner 一次性 cross-build 六平台并生成
  发行档 + `checksums.txt`，然后发布 GitHub Release。
- 不使用 Actions Artifact 在 job 间传输或分发构建产物。构建产物按版本
  目录镜像到 WebDAV：开发快照（main push / 手动触发）附逐文件
  `.sha256`，正式版本在 GitHub Release 成功后镜像 archives +
  `checksums.txt`；镜像失败不阻断 CI，GitHub Release 始终是权威分发
  渠道。
- 全工程禁止 CGO：Makefile `GOENV` 约束所有 Go 编译/分析命令，workflow
  顶层 `CGO_ENABLED: "0"` 双保险。
- GitHub Release Notes 唯一来源是 `CHANGELOG.md` 对应版本段
  （经 `scripts/release-notes.sh` 提取）。
