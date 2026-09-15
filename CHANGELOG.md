# 更新日志

本文件记录 TinySync 每次正式版本包含的用户可见变化，发布内容应与对应的
GitHub Release 摘要一致。

格式遵循 [Keep a Changelog 1.1.0](https://keepachangelog.com/zh-CN/1.1.0/)，
版本遵循[语义化版本 2.0.0](https://semver.org/lang/zh-CN/)。正式版本格式为
`x.y.z`；开发版本格式为 `dev-<commit日期>-<commit7>`。

开发期间向 `[Unreleased]` 写入；正式发布时将 `[Unreleased]` 改为
`[x.y.z] - yyyy-mm-dd`。Release workflow 只读取对应版本段落，
绝不根据 Git commit message 自动生成 Release Notes。

每个版本只使用以下分类：

- `新增`：面向用户的新特性。
- `修复`：面向用户的故障修复。
- `移除`：面向用户的特性或能力移除。

内部重构、测试、构建和文档维护不进入发布摘要，除非它们直接交付新特性或修复用户可见故障。

## [Unreleased]

### 新增

- 提供 Sync Job 的 REST API：创建、查询、更新、删除及手动运行与状态查询。
- 被 Sync Job 引用的 Source 现以 `409` 拒绝删除，不再依赖数据库错误。

### 修复

### 移除

## [0.2.0] - 2026-09-15

### 新增

- 支持持久化管理 WebDAV Source。
- 提供 WebDAV Source 的 REST API 与 Web 管理界面。
- 支持验证 WebDAV Source 连接状态。

## [0.1.0] - 2026-09-14

### 新增

- 提供 TinySync 初始可运行服务骨架。
- 提供内嵌 Web UI。
- 提供服务健康状态与版本查询 API。
- 支持 Linux、macOS、Windows 的 amd64/arm64 构建。
