package tool

import (
	"context"
	"log"
	"time"
)

// Next 是中间件链中的下一个处理器。
type Next func(ctx context.Context, call Call) (any, error)

// Middleware 包裹工具执行：在真正调用工具前后做校验 / 超时 / 恢复 / 日志等。
//
// 中间件在 Executor 中按注册顺序包裹（先注册的越靠内层）。约定：中间件应调用
// next 继续执行，并在需要时改造 ctx / call / 返回值。
type Middleware func(next Next) Next

// Recover 把工具执行中的 panic 转为 error，避免单个工具拖垮整个调用流程。
func Recover() Middleware {
	return func(next Next) Next {
		return func(ctx context.Context, call Call) (res any, err error) {
			defer func() {
				if r := recover(); r != nil {
					res = nil
					err = &panicError{call: call, value: r}
				}
			}()
			return next(ctx, call)
		}
	}
}

// Timeout 为工具执行设置超时；超时返回 error。
func Timeout(d time.Duration) Middleware {
	return func(next Next) Next {
		return func(ctx context.Context, call Call) (any, error) {
			ctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return next(ctx, call)
		}
	}
}

// Logging 在工具执行前后记录日志；logf 为 nil 时使用标准库 log。
func Logging(logf func(format string, args ...any)) Middleware {
	if logf == nil {
		logf = log.Printf
	}
	return func(next Next) Next {
		return func(ctx context.Context, call Call) (any, error) {
			start := time.Now()
			res, err := next(ctx, call)
			logf("tool call name=%s duration=%s err=%v", call.Name, time.Since(start), err)
			return res, err
		}
	}
}
