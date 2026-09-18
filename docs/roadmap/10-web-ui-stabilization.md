# v0.10.0 — Web UI & UX Stabilization

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。本阶段用于 v1.0 前最后一轮集中 WebUI 收口，不预先冻结具体页面方案或组件设计。

目标：

> 在不改变 TinySync 核心同步与安全语义的前提下，允许对现有 Web UI 进行大范围重构，完成信息架构、交互和视觉层面的统一，为 v1.0 稳定版冻结用户工作流。

## 调整范围

本阶段可按实际问题自由调整：

- 应用壳、导航、页面层级与信息架构。
- Sources、Jobs、History、Files、API Tokens、登录等现有管理界面的布局和操作流程。
- 表单、对话框、列表 / 表格、状态反馈、确认流程以及 loading / empty / error 等通用交互。
- 响应式布局、键盘操作、可访问性与视觉一致性。
- 前端组件和代码组织；继续使用 React + MUI + TypeScript，并保持类型化 API client。

具体 UI 方案随实现迭代，不在 ROADMAP 中提前锁定。

## 必要约束

- TinySync 仍保持远端 → 本地单向同步；Copy / Mirror、Source / Job / Publish 等核心领域语义不因 UI 重构改变。
- 优先只调整前端。确需补充 REST API 时应保持现有 `/api/v1` 兼容，前后端类型与测试同步更新；不为 UI 重构引入 REST / MCP breaking change。
- Web Session、CSRF、API Token scope、REST default-deny、公开发布与文件 confinement 等既有安全边界不得弱化；`/api/*`、`/mcp`、`/published/*` 与 SPA fallback 的路由边界保持。
- 不应仅为保存 UI 状态或偏好新增 SQLite schema；确有业务持久化需求时先独立说明理由。
- 保持单 Go 二进制、内嵌 Web UI、fallback 构建和 `CGO_ENABLED=0` 等既有交付约束。

## 完成标准

- 现有 Web 管理能力在重构后的 UI 中仍可访问，核心工作流能够完整完成。
- Sources → Jobs → Run / History → Files / Publish，以及认证 / Token 管理等主要路径没有已知阻断性回归。
- 桌面与移动端主要布局、状态反馈和交互达到可用于 v1.0 冻结的稳定状态。
- 相关变更通过 `make check` 与 `make build`；涉及真实运行链路时继续通过现有 native smoke / E2E 门禁。
- 用户可见变化在实际交付时进入 `CHANGELOG.md`；纯内部组件重构不单独记录。

## 非目标

本阶段不借 WebUI 重构扩展新的产品方向：不新增远端上传、双向同步、新协议、分布式执行、多用户 / RBAC，也不重写已经稳定的同步、存储、认证或 MCP 架构。发现 correctness / security regression 时可以独立修复。

## 与 v1.0 的边界

v0.10 完成后进入 v1.0 Stable Single-Node Release。大规模 WebUI 架构与核心交互调整应在本阶段完成；v1.0 主要负责全链路验收、升级 / 恢复验证、回归修复、兼容性契约与正式发布收口，而不是再次进行一轮 UI 重设计。
