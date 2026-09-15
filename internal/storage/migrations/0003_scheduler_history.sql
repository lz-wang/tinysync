-- v3：调度与同步历史（v0.4.0）。时间列为 Unix 毫秒。
-- Schedule 与 Job 严格 1:1 且无独立生命周期，直接扩展 sync_jobs；
-- 默认 manual 保证 v0.3 → v0.4 升级后既有 Job 行为不变（不自动启动）。
-- schedule_anchor_at 是 interval 的相位基准，防止重启后调度相位漂移。
-- trigger 是 SQL 保留字，运行触发器列名使用 trigger_type。
ALTER TABLE sync_jobs ADD COLUMN schedule_type TEXT NOT NULL DEFAULT 'manual'
    CHECK (schedule_type IN ('manual', 'once', 'interval', 'cron'));
ALTER TABLE sync_jobs ADD COLUMN schedule_value TEXT NOT NULL DEFAULT '';
ALTER TABLE sync_jobs ADD COLUMN schedule_timezone TEXT NOT NULL DEFAULT '';
ALTER TABLE sync_jobs ADD COLUMN schedule_anchor_at INTEGER;

-- 运行摘要：status=running 的行由启动恢复收敛（进程异常退出）。
-- scheduled_for 记录 occurrence（计划触发时间），是 once 消费判定与
-- 调度幂等的依据；手动运行为 NULL。error 同时承载失败原因与 skip 原因。
CREATE TABLE sync_runs (
    id                TEXT PRIMARY KEY,
    job_id            TEXT NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
    trigger_type      TEXT NOT NULL
                      CHECK (trigger_type IN ('manual', 'once', 'interval', 'cron')),
    scheduled_for     INTEGER,
    status            TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'skipped')),
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

-- per-Job 历史读取与 retention（保留最近 500 runs）。
CREATE INDEX idx_sync_runs_job_started ON sync_runs(job_id, started_at);
-- occurrence 消费判定：同 Job 同触发器同 occurrence 只消费一次。
CREATE INDEX idx_sync_runs_job_schedule ON sync_runs(job_id, trigger_type, scheduled_for);

-- 文件级变更明细：只为变化 / 冲突 / 失败写行（unchanged 只累计 summary），
-- 防止长期运行的 Job 每轮写入海量 history。path 相对 Job.LocalRoot。
CREATE TABLE sync_run_items (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id  TEXT NOT NULL REFERENCES sync_runs(id) ON DELETE CASCADE,
    path    TEXT NOT NULL,
    action  TEXT NOT NULL CHECK (action IN ('create', 'update', 'delete', 'relinquish')),
    status  TEXT NOT NULL CHECK (status IN ('succeeded', 'failed', 'skipped')),
    bytes   INTEGER NOT NULL DEFAULT 0,
    error   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_sync_run_items_run ON sync_run_items(run_id);
