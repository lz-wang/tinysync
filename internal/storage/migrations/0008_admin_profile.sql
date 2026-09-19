-- v8：管理员个人资料。v7 已创建的数据库在此补上头像字段；新库中该字段
-- 已由 v7 的 CREATE TABLE 预留，因此 migration 只在旧库执行。
ALTER TABLE admin_credentials ADD COLUMN avatar TEXT NOT NULL DEFAULT '';
