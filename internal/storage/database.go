// Package storage 承载 SQLite 持久化基础设施：数据库生命周期、
// schema migration 与事务辅助。全工程约束 CGO_ENABLED=0，
// driver 为 pure-Go 的 modernc.org/sqlite。
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// 数据库文件与连接参数（固定常量，不对外配置）。
const (
	// DatabaseFileName 是数据目录下的 SQLite 数据库文件名。
	DatabaseFileName = "tinysync.db"

	// busyTimeoutMS 是单条 SQL 等待锁的最长时间（毫秒）。
	busyTimeoutMS = 5000

	// pingTimeout 是打开后探测连接可用性的最长时间。
	pingTimeout = 10 * time.Second
)

// pragmaQuery 是经 DSN 下发的 per-connection PRAGMA 基线：
//   - foreign_keys：强制外键约束；
//   - busy_timeout：锁等待上限，缓解偶发 SQLITE_BUSY；
//   - journal_mode：WAL 提升并发并保证崩溃可恢复（持久属性）；
//   - synchronous：NORMAL 在 WAL 下兼顾持久性与性能。
//
// 不含 defensive / dqs：modernc.org/sqlite 当前构建（SQLite 3.53.4）未注册
// 这两个 PRAGMA，写入只会静默无效；后续 driver 支持后再补入基线。
const pragmaQuery = "_pragma=foreign_keys(1)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)"

// dsnPathEscaper 转义 SQLite URI 路径中的保留字符：
// % 是转义前缀，? 与 # 分别终止 path 进入 query / fragment，空格规范要求转义；
// 其余字符（含中文等 UTF-8 字节）SQLite 按原样处理，无需转义。
var dsnPathEscaper = strings.NewReplacer(
	"%", "%25",
	"?", "%3F",
	"#", "%23",
	" ", "%20",
)

// BuildDSN 返回指向 dbPath 的 SQLite file: URI，附 per-connection PRAGMA 基线。
// 路径经保留字符转义，数据目录可包含 ?、#、%、空格等特殊字符；
// Windows 盘符路径归一为 /C:/... 形式，避免盘符被 URI 解析为 authority，
// POSIX 绝对路径输出为 file:/// 前缀，相对路径保持相对语义。
func BuildDSN(dbPath string) string {
	return encodeSQLiteURI(dbPath) + "?" + pragmaQuery
}

// encodeSQLiteURI 把数据库文件路径编码为 SQLite URI 的 scheme + path
// 部分（不含 query）。
func encodeSQLiteURI(dbPath string) string {
	p := filepath.ToSlash(dbPath)
	if vol := filepath.VolumeName(p); vol != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	scheme := "file:"
	if strings.HasPrefix(p, "/") {
		scheme = "file://"
	}
	return scheme + dsnPathEscaper.Replace(p)
}

// Open 打开（必要时创建）数据目录下的 SQLite 数据库并验证连接可用。
// 目录不存在时自动创建；返回的 *sql.DB 已配置单连接池，调用方负责 Close。
func Open(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create datadir %s: %w", dataDir, err)
	}
	dbPath := filepath.Join(dataDir, DatabaseFileName)
	db, err := sql.Open("sqlite", BuildDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	// Source 配置属低频 CRUD，无并发连接需求；单连接同时规避
	// 多连接下 WAL 读写锁竞争与 PRAGMA 状态不一致问题。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pingCtx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", dbPath, err)
	}

	// 密码明文落库，收敛数据库文件权限（Windows 的 Chmod 只影响
	// 只读位，0o600 保留写位属无害操作，这里直接跳过）。
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dbPath, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("chmod %s: %w", dbPath, err)
		}
	}
	return db, nil
}
