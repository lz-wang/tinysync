-- v13：运行完成通知配置（ADR 0006）。singleton 表：id 恒为 1，
-- migration 预置默认行，应用只 UPDATE 不 INSERT。secret（Pushover
-- token / user key、SMTP 密码）明文落库，与 sources.credentials_json
-- 同一信任模型（DB 文件 0600）；API 层永不回显，只回 configured 布尔。
-- email_to 是逗号分隔的收件地址列表；updated_at 为 Unix 毫秒
--（0 表示从未配置）。
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

INSERT INTO notification_settings (id, updated_at)
VALUES (1, 0);
