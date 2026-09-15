-- v2：Sync Job 与 managed files（v0.3.0）。时间列为 Unix 毫秒；
-- name 大小写不敏感唯一。managed_files 是 Mirror 删除的唯一授权来源：
-- 只有记录在案的文件才允许被 Job 删除；删除 Job 级联清理其元数据。
CREATE TABLE sync_jobs (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL COLLATE NOCASE UNIQUE,
    source_id        TEXT NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
    remote_root      TEXT NOT NULL,
    local_root       TEXT NOT NULL,
    mode             TEXT NOT NULL CHECK (mode IN ('copy', 'mirror')),
    include_patterns TEXT NOT NULL DEFAULT '[]',
    exclude_patterns TEXT NOT NULL DEFAULT '[]',
    enabled          INTEGER NOT NULL DEFAULT 1
                     CHECK (enabled IN (0, 1)),
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

CREATE TABLE managed_files (
    job_id          TEXT NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
    remote_path     TEXT NOT NULL,
    local_rel_path  TEXT NOT NULL,
    state           TEXT NOT NULL CHECK (state IN ('pending', 'synced')),
    remote_size     INTEGER NOT NULL,
    remote_mtime_ns INTEGER,
    remote_etag     TEXT NOT NULL DEFAULT '',
    remote_checksum TEXT NOT NULL DEFAULT '',
    remote_version  TEXT NOT NULL DEFAULT '',
    local_size      INTEGER,
    local_mtime_ns  INTEGER,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (job_id, remote_path),
    UNIQUE (job_id, local_rel_path)
);
