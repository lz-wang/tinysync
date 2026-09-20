-- v10：废弃 published_files（v0.10.0 发布 → 共享破坏性重构，与发布
-- 域代码退役同期生效）。共享功能由 v9 的 shares 表承接；旧发布策略
-- 数据不迁移（破坏性变更，CHANGELOG 已声明），升级后共享列表为空，
-- 按需重建。
DROP TABLE published_files;
