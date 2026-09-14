# 仓库协作指南

TinySync 面向 HomeLab，目标是将异构远端文件单向同步到本地，以单一 Go 二进制提供 Web UI 与 API。

## 开始任务

1. 查看 `git status --short`，保留已有改动，限定本次范围。
2. 阅读 [README.md](README.md) 与 [CHANGELOG.md](CHANGELOG.md)，确认当前命令契约与未发布变更。
3. 从 [ROADMAP.md](ROADMAP.md) 确认架构、当前阶段与优先级；推进阶段后同步更新主线摘要及对应方案。不要将规划、依赖或工作流配置视为已交付能力。
4. 按下表读取任务所需文档，再探索相关代码；无需预读全部阶段方案。

| 任务 | 按需阅读 |
| --- | --- |
| 开发、测试、格式化、配置或 API 变更 | [开发与验证](docs/guides/development.md)，命令入口为 [Makefile](Makefile)（`make help`） |
| CI、版本、打包或发布 | [构建与发布](docs/guides/release.md) → 对应 workflow / script |
| Source、同步、调度、文件访问、认证或 MCP | 从 [ROADMAP.md](ROADMAP.md) 进入对应阶段方案 |

<!-- CODEGRAPH_START -->
## 代码探索

根目录存在 `.codegraph/` 时，理解代码结构、定位符号和追踪调用关系先用 CodeGraph：

- MCP：`codegraph_explore` 查询相关符号与调用路径，`codegraph_node` 查看符号或文件；工具延迟加载时按名称搜索。
- CLI：`codegraph explore "<符号或问题>"`、`codegraph node <符号或文件>`。
- 缩小范围后再用 `rg` 或直接读取补充；索引不存在或工具不可用时用 `rg` / `rg --files`，不要自行创建索引。

代码结构以当前源码为准，不在本文件维护目录清单。
<!-- CODEGRAPH_END -->

## 始终遵守

- 全工程禁止 CGO；版本号唯一事实来源是 Git；保留无 Node 环境的 Go fallback 构建路径。具体架构边界见 [ROADMAP.md](ROADMAP.md)。
- v1 只做远端 → 本地；Mirror 只能删除当前 Job 明确管理的文件，不能凭远端列表缺失删除未知本地文件。
- 不提交密钥、令牌、个人环境配置、构建产物或覆盖率文件；测试使用临时数据目录，不覆盖真实同步文件。
- 遵循附近代码风格，不顺带全库格式化。代码、测试或脚本变更后执行全量 lint、typecheck 和 test；命令与失败处理见[开发与验证](docs/guides/development.md)。
- 用户可见变化同步写入 `CHANGELOG.md` 的 `[Unreleased]`，仅使用 `新增` / `修复` / `移除`；纯内部重构、测试、构建及文档维护按该文件规则处理。
- 文档与交付优先中文；未要求时不提交或推送。提交信息遵循 Conventional Commits，type 为英文，正文用中文补充说明，仅暂存本次相关文件。
- 交付说明修改内容、实际验证结果、未覆盖项及 Git 状态；本地通过不能替代远端 CI、发行资产或镜像验收。
