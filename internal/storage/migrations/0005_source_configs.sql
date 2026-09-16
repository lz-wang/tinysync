-- v5：多协议 Source 配置与凭据（v0.5.0）。
-- sources 表从 WebDAV 扁平列演进为通用持久化：非敏感协议配置存
-- config_json，secret 存 credentials_json，均按协议存扁平 JSON 对象
-- （type 是单选判别符，由 type 列承载，不在 JSON 内重复）。存量
-- WebDAV 行一次性 backfill 后清空 legacy 列——endpoint / username /
-- password 不再是事实来源，仅作为 schema tombstone 保留（避免重建
-- 牵动 sync_jobs → sources 的 FK 链路；后续 schema compact 时移除）。
-- 迁移后 Repository 只读写 JSON 字段，普通读取路径永不取回
-- credentials_json 明文。
ALTER TABLE sources ADD COLUMN config_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE sources ADD COLUMN credentials_json TEXT NOT NULL DEFAULT '{}';

-- 回填：存量 WebDAV 行展开为 config / credentials JSON；空密码保持
-- 匿名语义（credentials_json 为空对象，不写入空串键）。
UPDATE sources SET
    config_json = json_object('endpoint', endpoint, 'username', username),
    credentials_json = CASE
        WHEN password != '' THEN json_object('password', password)
        ELSE json_object()
    END
WHERE type = 'webdav';

-- 清空 legacy 列：不允许新旧字段同时作为事实来源。
UPDATE sources SET endpoint = '', username = '', password = '';
