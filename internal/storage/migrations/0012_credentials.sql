-- v12：凭据库（ADR 0005）。协议无关的命名 secret 实体，本版本只
-- 实现 ssh_key 类型：私钥与其解密口令是不可分割的整体。secret 以
-- 明文 JSON 存储，与 sources.credentials_json 同一信任模型；fingerprint
-- 是保存时派生的公钥指纹（仅用于人前区分钥匙），has_passphrase 是
-- 口令存在性回显布尔（普通读取路径永不取回 secret 明文）。时间列为
-- Unix 毫秒；name 大小写不敏感唯一，与 sources 同规则。
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
