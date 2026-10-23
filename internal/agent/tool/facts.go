package tool

import (
	"context"

	"github.com/biox-dev/gobrave/internal/agent/types"
)

// 本文件提供工具执行期的「执行事实」载体。
//
// 与 RuntimeContext（业务变量：SystemPromptVars / 工作目录）分开：
// 这里回答的是「谁在执行」——用户 / 会话 / 项目 / 任务，由 Provider 从 Runtime
// 的 TaskFacts 快照注入。工具只读取，不关心事实来自任务模式还是 standalone 调用。

type taskFactsKey struct{}

// WithTaskFacts 把执行事实注入工具 ctx。
func WithTaskFacts(ctx context.Context, facts types.TaskFacts) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, taskFactsKey{}, facts)
}

// TaskFactsFrom 取出执行事实；未注入时返回零值。
func TaskFactsFrom(ctx context.Context) types.TaskFacts {
	if ctx == nil {
		return types.TaskFacts{}
	}
	facts, _ := ctx.Value(taskFactsKey{}).(types.TaskFacts)
	return facts
}

// UserIDFrom 取出执行事实中的用户 ID（0 表示未知）。
func UserIDFrom(ctx context.Context) int64 { return TaskFactsFrom(ctx).UserID }

// SessionIDFrom 取出执行事实中的会话 ID（0 表示未关联会话）。
func SessionIDFrom(ctx context.Context) int64 { return TaskFactsFrom(ctx).SessionID }

// ProjectIDFrom 取出执行事实中的项目 ID（0 表示未解析到激活项目）。
func ProjectIDFrom(ctx context.Context) int64 { return TaskFactsFrom(ctx).ProjectID }
