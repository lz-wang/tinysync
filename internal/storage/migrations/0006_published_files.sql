-- v6：Published file policy（v0.6.0）。local_path 是创建 Policy 时
-- 已 canonicalize 的绝对文件路径（EvalSymlinks 后），与 Job 生命周期
-- 解耦：Job 后续修改 LocalRoot 或 relinquish 文件不会隐式 retarget
-- 既有 URL；文件被真正删除后公开 URL 自然 404。public_path 全局
-- 唯一；disabled / 过期 / 文件缺失在 serving 侧一律 404，不区分
-- 「存在但禁止」与「不存在」。
CREATE TABLE published_files (
    id          TEXT PRIMARY KEY,
    local_path  TEXT NOT NULL,
    public_path TEXT NOT NULL UNIQUE,
    enabled     INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    expires_at  INTEGER,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
