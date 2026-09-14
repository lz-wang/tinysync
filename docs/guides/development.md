# 开发与验证

涉及代码、测试、格式化、配置或 API 变更时阅读。架构与阶段见
[ROADMAP.md](../../ROADMAP.md)，CI 与发布见[构建与发布](release.md)。

## 命令入口

从仓库根目录执行 [Makefile](../../Makefile)。Go 版本以 [go.mod](../../go.mod)
为准（当前 1.26），Node 与 CI 对齐（当前 24），前端依赖与脚本以
[package.json](../../web/package.json) 和锁文件为准。

| 命令 | 用途 |
| --- | --- |
| `make help` | 查看工程命令 |
| `make setup` | 安装 goimports-reviser、Go 与 Web 依赖；Web 使用 `npm install`，执行后检查锁文件差异 |
| `make web-ci-install` | 使用 `npm ci` 安装锁定的 Web 依赖 |
| `make check` | Go 格式与依赖一致性、`go vet`、Web Biome、TypeScript、全量 Go 测试 |
| `make test` | 仅全量 Go 测试：`CGO_ENABLED=0 go test -timeout 30s ./...` |
| `make web-lint` / `make web-typecheck` | 分别执行 Biome 检查与 `tsc -b` |
| `make coverage` | 生成 `coverage/backend.out` 与 HTML 报告 |
| `make ci` | 安装工具及锁定的 Web 依赖、下载 Go 依赖，再执行 `make check` |
| `make build` / `make serve` | 构建含 Web UI 的当前平台二进制 / 构建后启动服务 |
| `make web-build` | TypeScript 检查与 Vite 构建，产物供 Go 嵌入 |
| `make format` / `make web-format` | 全库 Go + Web / Web 自动格式化，会修改文件；仅在任务需要时执行并审查差异 |

`make serve` 使用默认运行配置；需要指定目录或端口时，构建后按 README 直接运行
`./tinysync serve --datadir <临时目录> --port <端口>`。

## 实现约定

- Go 使用 `gofmt`（Tab 缩进），导入顺序与 Makefile 的 goimports-reviser 配置一致，标准库在前。
- 包保持单一职责，业务逻辑放在 `internal/` 对应领域包；遵守 ROADMAP 中的启动链路和装配边界。
- 错误包含操作与目标上下文，保留底层错误，支持 `errors.Is/As`。
- HTTP handler 与生命周期覆盖成功和失败路径；配置变更覆盖默认值、环境变量、CLI 优先级及无效输入。
- Web 使用严格 TypeScript，不用 `any` 或非空断言绕过边界校验；React 负责 UI，API 访问统一走 [api.ts](../../web/src/api.ts) 的类型化客户端。
- Biome 使用 4 空格、单引号、按需分号；配置见 [biome.json](../../web/biome.json)。只格式化本次相关文件，不顺带重排全库。
- 前后端 API 契约同时修改实现与测试；未知 API 404、方法不允许、SPA fallback 和静态资源缓存行为不得回退。
- 所有 Go 编译/分析保持 `CGO_ENABLED=0`；Makefile 通过 `GOENV` 传递，不用开启 CGO 的方式绕过依赖问题。

## 验证选择

- **仅文档变更**：核对链接、路径、命令、状态表述及差异，无需运行业务测试。已跟踪文件用 `git diff --check`；新增文件另用 `git diff --no-index --check /dev/null <文件>`。
- **代码、测试、脚本或工具配置变更**：交付前执行 `make check`，覆盖全量 lint、typecheck 与 Go test；定向检查仅用于定位问题，不能替代最终门禁。不覆盖 `GOENV` 开启 CGO，不通过额外参数筛掉测试。
- **前端构建或嵌入资源变更**：在上述检查后执行 `make build`，确认 `webui` 路径可构建；`make test` 默认覆盖无 tag 的 fallback 路径。
- **启动、版本、打包或发布链路变更**：按[构建与发布](release.md)补充本机 Smoke 或平台构建；本地 cross-build 不代表其他平台运行通过。

`make check` 已包含全量 Go 测试，成功后无需无故重复 `make test`。
当前 Web 没有独立单测命令，Biome、TypeScript 和构建不能表述为浏览器行为验证；
未要求时不主动执行浏览器 / E2E 验证。

任一门禁失败都要定位并修复，包括既有阻塞；不得通过放宽规则、删除测试或新增跳过凑绿。
缺少依赖、权限或外部环境而无法完成时明确报告阻塞及未覆盖范围，不宣称全绿。
运行服务及未来同步测试使用临时数据目录，保留用户已有改动和真实文件。
