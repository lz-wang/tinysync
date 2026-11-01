-- TinySync 统一任务管理 schema（全新建库，破坏性变更起点）。
-- 版本策略：无迁移序列；user_version 固定为 storage.SchemaVersion（14），
-- 与旧迁移序列（1..13）断开兼容——旧库启动明确报错，要求使用新数据目录。
-- 表清单：sources / credentials / jobs / job_runs / job_run_items /
-- managed_files / registries / admin_credentials / web_sessions /
-- api_tokens / shares / notification_settings。

-- ---------------------------------------------------------------------------
-- 同步源域
-- ---------------------------------------------------------------------------

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
    updated_at INTEGER NOT NULL,
    config_json      TEXT NOT NULL DEFAULT '{}',
    credentials_json TEXT NOT NULL DEFAULT '{}'
);

-- ---------------------------------------------------------------------------
-- 凭据域
-- ---------------------------------------------------------------------------

CREATE TABLE credentials (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL COLLATE NOCASE UNIQUE,
    type           TEXT NOT NULL,
    secret_json    TEXT NOT NULL DEFAULT '{}',
    fingerprint    TEXT NOT NULL,
    has_passphrase INTEGER NOT NULL DEFAULT 0
                   CHECK (has_passphrase IN (0, 1)),
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

-- ---------------------------------------------------------------------------
-- 统一任务域：一张 jobs 表经 kind + config_json 承载 File / OCI / Git
-- ---------------------------------------------------------------------------

CREATE TABLE jobs (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL COLLATE NOCASE UNIQUE,
    kind             TEXT NOT NULL
                     CHECK (kind IN ('file', 'oci', 'git')),
    enabled          INTEGER NOT NULL DEFAULT 1
                     CHECK (enabled IN (0, 1)),
    config_json      TEXT NOT NULL DEFAULT '{}',

    schedule_type      TEXT NOT NULL DEFAULT 'manual'
                       CHECK (schedule_type IN ('manual', 'once', 'interval', 'cron')),
    schedule_value     TEXT NOT NULL DEFAULT '',
    schedule_timezone  TEXT NOT NULL DEFAULT '',
    schedule_anchor_at INTEGER,
    next_run_at        INTEGER,
    once_consumed_at   INTEGER,

    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX idx_jobs_kind ON jobs(kind);

-- 统一运行历史：固定状态字段 + result_json 保存领域结果摘要。
CREATE TABLE job_runs (
    id            TEXT PRIMARY KEY,
    job_id        TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    job_kind      TEXT NOT NULL
                  CHECK (job_kind IN ('file', 'oci', 'git')),
    trigger_type  TEXT NOT NULL
                  CHECK (trigger_type IN ('manual', 'once', 'interval', 'cron')),
    scheduled_for INTEGER,
    state         TEXT NOT NULL
                  CHECK (state IN ('running', 'succeeded', 'failed', 'skipped', 'canceled')),
    started_at    INTEGER NOT NULL,
    finished_at   INTEGER,
    result_json   TEXT NOT NULL DEFAULT '{}',
    error         TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_job_runs_job_started ON job_runs(job_id, started_at);
CREATE INDEX idx_job_runs_job_schedule ON job_runs(job_id, trigger_type, scheduled_for);

-- 运行明细：action / status / bytes 为公共列，领域字段（文件路径、
-- 镜像引用、平台等）放 detail_json；只为实际变化/失败写行。
CREATE TABLE job_run_items (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT NOT NULL REFERENCES job_runs(id) ON DELETE CASCADE,
    action      TEXT NOT NULL,
    status      TEXT NOT NULL
                CHECK (status IN ('succeeded', 'failed', 'skipped', 'canceled')),
    bytes       INTEGER NOT NULL DEFAULT 0,
    error       TEXT NOT NULL DEFAULT '',
    detail_json TEXT NOT NULL DEFAULT '{}',
    created_at  INTEGER NOT NULL
);

CREATE INDEX idx_job_run_items_run ON job_run_items(run_id);

-- 文件 Mirror 模式的受管文件记录（File 领域专用，删除授权唯一来源）。
CREATE TABLE managed_files (
    job_id          TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
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

CREATE INDEX idx_managed_files_job_local ON managed_files(job_id, local_rel_path);

-- ---------------------------------------------------------------------------
-- OCI Registry 配置与凭据（primary 目标仓库 + 匿名 mirror）
-- ---------------------------------------------------------------------------

CREATE TABLE registries (
    id                   TEXT PRIMARY KEY,
    name                 TEXT NOT NULL COLLATE NOCASE UNIQUE,
    kind                 TEXT NOT NULL DEFAULT 'primary'
                         CHECK (kind IN ('primary', 'mirror')),
    host                 TEXT NOT NULL,
    username             TEXT NOT NULL DEFAULT '',
    password             TEXT NOT NULL DEFAULT '',
    insecure_skip_verify INTEGER NOT NULL DEFAULT 0
                         CHECK (insecure_skip_verify IN (0, 1)),
    plain_http           INTEGER NOT NULL DEFAULT 0
                         CHECK (plain_http IN (0, 1)),
    strip_registries_json  TEXT NOT NULL DEFAULT '[]',
    path_transform_json    TEXT NOT NULL DEFAULT '{}',
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);

-- ---------------------------------------------------------------------------
-- 认证域
-- ---------------------------------------------------------------------------

CREATE TABLE admin_credentials (
    singleton     INTEGER PRIMARY KEY CHECK (singleton = 1),
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    avatar        TEXT NOT NULL DEFAULT ''
);

CREATE TABLE web_sessions (
    id           TEXT PRIMARY KEY,
    session_hash BLOB NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);

CREATE INDEX idx_web_sessions_expires_at ON web_sessions (expires_at);

CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    token_hash   BLOB NOT NULL UNIQUE,
    scopes_json  TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,
    last_used_at INTEGER,
    revoked_at   INTEGER
);

-- ---------------------------------------------------------------------------
-- 共享域
-- ---------------------------------------------------------------------------

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

-- ---------------------------------------------------------------------------
-- 通知域
-- ---------------------------------------------------------------------------

CREATE TABLE notification_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),

    pushover_enabled INTEGER NOT NULL DEFAULT 0
                   CHECK (pushover_enabled IN (0, 1)),
    pushover_token    TEXT NOT NULL DEFAULT '',
    pushover_user_key TEXT NOT NULL DEFAULT '',

    email_enabled INTEGER NOT NULL DEFAULT 0
                CHECK (email_enabled IN (0, 1)),
    email_host     TEXT NOT NULL DEFAULT '',
    email_port     INTEGER NOT NULL DEFAULT 587,
    email_security TEXT NOT NULL DEFAULT 'starttls'
                 CHECK (email_security IN ('none', 'starttls', 'tls')),
    email_username TEXT NOT NULL DEFAULT '',
    email_password TEXT NOT NULL DEFAULT '',
    email_from     TEXT NOT NULL DEFAULT '',
    email_to       TEXT NOT NULL DEFAULT '',

    updated_at INTEGER NOT NULL
);

-- 通知配置是单例行：初始化即插入默认行（Load / Save 都按 id=1 读写）。
INSERT INTO notification_settings (id, updated_at)
VALUES (1, 0);
