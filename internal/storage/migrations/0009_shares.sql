-- v9：Share 共享策略（v0.10.0，发布 → 共享破坏性重构）。local_path
-- 是创建 Share 时已 canonicalize 的绝对路径（EvalSymlinks 后），与
-- Job 生命周期解耦：Job 后续修改 LocalRoot 不会隐式 retarget 既有
-- 共享；目标被删除后公开侧自然 404。slug 全局唯一：自定义共享名称
-- 即 slug，未命名时系统生成随机 slug；name 为 NULL 表示未命名
-- （展示回落 basename(local_path)）。is_dir 记录创建时目标类型
-- （文件共享的公开浏览根是恰含自身一条目的虚拟目录）。disabled /
-- 过期 / 目标缺失在公开侧一律 404，不区分「存在但禁止」与「不
-- 存在」。published_files 表由 0010 迁移随发布域退役同期废弃，
-- 旧策略数据不迁移。
CREATE TABLE shares (
    id         TEXT PRIMARY KEY,
    local_path TEXT NOT NULL,
    slug       TEXT NOT NULL UNIQUE,
    name       TEXT,
    is_dir     INTEGER NOT NULL CHECK (is_dir IN (0, 1)),
    enabled    INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    expires_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
