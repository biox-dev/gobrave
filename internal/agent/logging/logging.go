// Package logging 是 opencode internal/logging 的最小替代。
//
// 迁移过来的 opencode 代码（lsp 包、tool/coder 工具）只用到
// Debug / Warn / Error / ErrorPersist / RecoverPanic，这里把它们桥接到
// gobrave 的 logrus logger，避免为迁移代码再引入一套日志设施。
package logging

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/biox-dev/gobrave/internal/logger"
)

// Debug 输出 debug 级别日志。
func Debug(msg string, kv ...any) { logger.GetLogger(context.Background()).Debug(format(msg, kv)) }

// Warn 输出 warn 级别日志。
func Warn(msg string, kv ...any) { logger.GetLogger(context.Background()).Warn(format(msg, kv)) }

// Error 输出 error 级别日志。
func Error(msg string, kv ...any) { logger.GetLogger(context.Background()).Error(format(msg, kv)) }

// ErrorPersist 输出 error 级别日志（LSP 中表示「必须留痕」的错误）。
func ErrorPersist(msg string, kv ...any) {
	logger.GetLogger(context.Background()).Error(format(msg, kv))
}

// RecoverPanic 用于 defer：捕获 panic，记录堆栈后执行 handler。
func RecoverPanic(name string, handler func()) {
	if r := recover(); r != nil {
		ErrorPersist(fmt.Sprintf("%s: panic: %v\n%s", name, r, debug.Stack()))
		if handler != nil {
			handler()
		}
	}
}

// format 把 slog 风格的 key/value 追加到消息末尾。
func format(msg string, kv []any) string {
	if len(kv) == 0 {
		return msg
	}
	for i := 0; i < len(kv); i += 2 {
		if i+1 < len(kv) {
			msg += fmt.Sprintf(" %v=%v", kv[i], kv[i+1])
			continue
		}
		msg += fmt.Sprintf(" %v", kv[i])
	}
	return msg
}
