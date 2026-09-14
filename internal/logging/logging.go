// Package logging 提供项目全局 logger（沿 CloudCenter 的双 sink 设计）。
//
// 在启动时调用 Init(dataDir) 完成初始化；之后各处直接用包级函数：
//
//	logging.Infof("tinysync ready, listening on %s", addr)
//	logging.Errorf("sync failed: %v", err)
//
// 输出为易读的单行纯文本（非 JSON），格式：
//
//		2026-09-14 21:24:26 INFO   tinysync ready, listening on :9466
//
//	  - 时间：2006-01-02 15:04:05；级别大写、固定 6 字符宽左对齐，消息列对齐
//	  - 双 sink 不同级别阈值：stderr 仅写 INFO 及以上（debug 不进控制台），
//	    日志文件写 DEBUG 及以上（debug 只落盘）；dataDir 为空或目录不可写时
//	    降级为仅 stderr，保证日志不丢
//	  - 禁用 caller/stacktrace，确保始终单行
//
// 包级初始化为写向 stderr 的默认实例，未调用 Init（如测试）也始终可用。
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// 日志文件轮转参数（固定常量，不对外配置）。
const (
	logFilename   = "tinysync.log"
	maxLogSizeMB  = 50
	maxLogBackups = 7
	compressLogs  = true
)

// sugar 是全局 sugared logger，保证未显式 Init 时也非 nil。
var sugar = newSugared(zapcore.AddSync(os.Stderr))

// Init 在启动时初始化全局 logger：日志文件写 DEBUG 及以上（按 maxLogSizeMB
// 轮转，保留 maxLogBackups 份并 gzip 压缩），stderr 写 INFO 及以上。
// dataDir 为空时仅写 stderr；创建日志目录失败时同样降级为仅 stderr。
// 应在解析出数据目录后最先调用，早于任何 goroutine 开始记日志。
func Init(dataDir string) {
	stderrSyncer := zapcore.AddSync(os.Stderr)
	if dataDir == "" {
		sugar = newSugared(stderrSyncer)
		return
	}

	logPath := filepath.Join(dataDir, "logs", logFilename)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create log dir %s failed, falling back to stderr only: %v\n", filepath.Dir(logPath), err)
		sugar = newSugared(stderrSyncer)
		return
	}

	fileSyncer := zapcore.AddSync(&lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    maxLogSizeMB,
		MaxBackups: maxLogBackups,
		Compress:   compressLogs,
		LocalTime:  true,
	})
	// stderr 仅 INFO 及以上；日志文件 DEBUG 及以上（debug 只落盘）。
	stderrCore := newCore(stderrSyncer, zapcore.InfoLevel)
	fileCore := newCore(fileSyncer, zapcore.DebugLevel)
	sugar = zap.New(zapcore.NewTee(stderrCore, fileCore)).Sugar()
}

// ---- 包级日志函数：各处直接调用即可 ----

func Debugf(format string, args ...any) { sugar.Debugf(format, args...) }

func Infof(format string, args ...any) { sugar.Infof(format, args...) }

func Warnf(format string, args ...any) { sugar.Warnf(format, args...) }

func Errorf(format string, args ...any) { sugar.Errorf(format, args...) }

// Sync 刷新底层缓冲；程序退出前调用。
func Sync() error { return sugar.Sync() }

// ---- 内部构造 ----

// newSugared 用给定 WriteSyncer 构造单 sink 的 sugared logger（debug 级），
// 用于未初始化或测试场景：所有级别都写到该 sink，保证日志不丢。
func newSugared(sync zapcore.WriteSyncer) *zap.SugaredLogger {
	return zap.New(newCore(sync, zapcore.DebugLevel)).Sugar()
}

// newCore 构造一个使用项目统一控制台编码与指定级别阈值的 zap core。
func newCore(sync zapcore.WriteSyncer, level zapcore.Level) zapcore.Core {
	return zapcore.NewCore(zapcore.NewConsoleEncoder(encoderConfig()), sync, level)
}

// encoderConfig 返回统一的控制台编码配置：时间、大写级别、单行消息。
func encoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		TimeKey:          "ts",
		LevelKey:         "level",
		NameKey:          zapcore.OmitKey,
		CallerKey:        zapcore.OmitKey,
		FunctionKey:      zapcore.OmitKey,
		MessageKey:       "msg",
		StacktraceKey:    zapcore.OmitKey,
		LineEnding:       zapcore.DefaultLineEnding,
		EncodeLevel:      encodeLevel,
		EncodeTime:       encodeTime,
		EncodeName:       nil,
		ConsoleSeparator: " ",
	}
}

// encodeTime 格式化为 "2006-01-02 15:04:05"。
func encodeTime(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("2006-01-02 15:04:05"))
}

// encodeLevel 输出大写级别并左对齐填充至 6 字符，使消息列对齐。
func encodeLevel(lv zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(fmt.Sprintf("%-6s", lv.CapitalString()))
}
