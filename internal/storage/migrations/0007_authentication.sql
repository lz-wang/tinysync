-- v7：认证（v0.7.0）。单一 Local Admin：admin_credentials 是 singleton
-- 单例行，密码存 Argon2id PHC 完整编码。web_sessions / api_tokens 只存
-- 凭据的 SHA-256 hash（高熵随机 secret），raw 值绝不落库。时间列为
-- Unix 毫秒（与既有表一致）；api_tokens 的 expires_at 可空表示永不过期，
-- revoked_at 可空表示未撤销（软撤销，不 DELETE 记录，保留生命周期证据）。
CREATE TABLE admin_credentials (
    singleton     INTEGER PRIMARY KEY CHECK (singleton = 1),
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE web_sessions (
    id           TEXT PRIMARY KEY,
    session_hash BLOB NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);

-- 过期 session 清理按 expires_at 扫描。
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
