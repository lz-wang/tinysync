-- v1：Source 管理（v0.2.0）。后续领域的表由对应阶段的 migration 创建，
-- 不预先设计。时间列为 Unix 毫秒；name 大小写不敏感唯一。
CREATE TABLE sources (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL COLLATE NOCASE UNIQUE,
    type       TEXT NOT NULL,
    endpoint   TEXT NOT NULL,
    username   TEXT NOT NULL DEFAULT '',
    password   TEXT NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1
               CHECK (enabled IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
