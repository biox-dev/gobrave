package tool

import (
	"context"

	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// 本文件把「会话内文件版本历史」作为进程级服务能力挂到工具执行上下文上。
//
// 约定与 FileReader / MemorySaver 一致：HistoryService 由上层（agent.Options）
// 一次性注入，工具在 Execute 中通过 FileHistoryFrom 取值，避免 builtin 工具直接
// 依赖数据库或 agent 包。会话 ID 通过 SessionIDFrom(ctx) 取得。

// fileHistoryKey 是 HistoryService 在 context 中的私有键类型。
type fileHistoryKey struct{}

// WithFileHistory 把文件历史服务注入 context；nil 时返回原 context。
func WithFileHistory(ctx context.Context, svc agentinterfaces.HistoryService) context.Context {
	if ctx == nil || svc == nil {
		return ctx
	}
	return context.WithValue(ctx, fileHistoryKey{}, svc)
}

// FileHistoryFrom 取出注入的文件历史服务；未注入时返回 nil。
func FileHistoryFrom(ctx context.Context) agentinterfaces.HistoryService {
	if ctx == nil {
		return nil
	}
	svc, _ := ctx.Value(fileHistoryKey{}).(agentinterfaces.HistoryService)
	return svc
}
