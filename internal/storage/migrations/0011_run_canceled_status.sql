-- v11：运行状态机引入 canceled（用户手动停止的终态）。sync_runs 与
-- sync_run_items 的 status CHECK 约束同步扩展；SQLite 不能修改 CHECK，
-- 需重建两张表。
--
-- 本 migration 在单事务内执行，且连接 PRAGMA foreign_keys=ON 常开
--（DSN 基线），事务内无法切换该 PRAGMA。以下顺序经过设计，任何一步
-- 都不会触发 DROP TABLE 的隐式 DELETE 级联清空子表：
--   1. 先 rename 子表：其外键引用尚指向 sync_runs，安全；
--   2. rename 父表：rename 会同步改写子表旧表的 REFERENCES 为
--      sync_runs_old（SQLite 默认 legacy_alter_table=OFF 语义）；
--   3. 建新 sync_runs / sync_run_items（新 CHECK，外键指向新表名）；
--   4. 先拷贝 runs 再拷贝 items（外键约束下顺序必需）；
--   5. 删除旧表：此刻已无任何表引用旧表，隐式 DELETE 不级联伤及
--      新表数据；
--   6. 重建索引（旧索引随旧表删除，名称已释放）。
-- items 的 AUTOINCREMENT 序列经显式拷贝 id 后自动对齐 max(id)，既有
-- 单调性保持不变。

ALTER TABLE sync_run_items RENAME TO sync_run_items_old;
ALTER TABLE sync_runs RENAME TO sync_runs_old;

CREATE TABLE sync_runs (
    id                TEXT PRIMARY KEY,
    job_id            TEXT NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
    trigger_type      TEXT NOT NULL
                      CHECK (trigger_type IN ('manual', 'once', 'interval', 'cron')),
    scheduled_for     INTEGER,
    status            TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'skipped', 'canceled')),
    started_at        INTEGER NOT NULL,
    finished_at       INTEGER,
    files_total       INTEGER NOT NULL DEFAULT 0,
    files_created     INTEGER NOT NULL DEFAULT 0,
    files_updated     INTEGER NOT NULL DEFAULT 0,
    files_deleted     INTEGER NOT NULL DEFAULT 0,
    files_skipped     INTEGER NOT NULL DEFAULT 0,
    bytes_transferred INTEGER NOT NULL DEFAULT 0,
    error             TEXT NOT NULL DEFAULT ''
);

CREATE TABLE sync_run_items (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id  TEXT NOT NULL REFERENCES sync_runs(id) ON DELETE CASCADE,
    path    TEXT NOT NULL,
    action  TEXT NOT NULL CHECK (action IN ('create', 'update', 'delete', 'relinquish')),
    status  TEXT NOT NULL CHECK (status IN ('succeeded', 'failed', 'skipped', 'canceled')),
    bytes   INTEGER NOT NULL DEFAULT 0,
    error   TEXT NOT NULL DEFAULT ''
);

INSERT INTO sync_runs (
    id, job_id, trigger_type, scheduled_for, status, started_at, finished_at,
    files_total, files_created, files_updated, files_deleted, files_skipped,
    bytes_transferred, error
)
SELECT
    id, job_id, trigger_type, scheduled_for, status, started_at, finished_at,
    files_total, files_created, files_updated, files_deleted, files_skipped,
    bytes_transferred, error
FROM sync_runs_old;

INSERT INTO sync_run_items (id, run_id, path, action, status, bytes, error)
SELECT id, run_id, path, action, status, bytes, error FROM sync_run_items_old;

DROP TABLE sync_run_items_old;
DROP TABLE sync_runs_old;

CREATE INDEX idx_sync_runs_job_started ON sync_runs(job_id, started_at);
CREATE INDEX idx_sync_runs_job_schedule ON sync_runs(job_id, trigger_type, scheduled_for);
CREATE INDEX idx_sync_run_items_run ON sync_run_items(run_id);
