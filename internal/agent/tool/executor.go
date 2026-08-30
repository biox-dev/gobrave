package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// Call 是一次待执行的工具调用。
type Call struct {
	ID        string          `json:"id,omitempty"`        // 调用唯一 ID（模型生成）
	Name      string          `json:"name"`                // 工具名
	Arguments json.RawMessage `json:"arguments,omitempty"` // 参数（JSON）
}

// Executor 执行工具调用：解析工具 → 校验参数 → 套用中间件 → 执行。
//
// 语义约定：所有失败（工具不存在 / 参数非法 / 超时 / panic / 工具返回错误）都
// 收敛为 Result.IsError=true，而不会向上抛出中断整个流程——模型据此纠正行为。
type Executor struct {
	registry    *Registry
	middlewares []Middleware
}

// ExecutorOption 配置 Executor。
type ExecutorOption func(*Executor)

// WithMiddlewares 追加中间件（先加入的越靠内层）。
func WithMiddlewares(ms ...Middleware) ExecutorOption {
	return func(e *Executor) {
		e.middlewares = append(e.middlewares, ms...)
	}
}

// NewExecutor 创建执行器；默认在最内层附带 Recover 中间件（panic 不逃逸）。
func NewExecutor(registry *Registry, opts ...ExecutorOption) *Executor {
	if registry == nil {
		registry = NewRegistry()
	}
	e := &Executor{registry: registry}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Execute 执行一次工具调用，始终返回 Result（不会因工具失败而中断）。
func (e *Executor) Execute(ctx context.Context, call Call) Result {
	res, err := e.build()(ctx, call)
	if err != nil {
		return Failure(err)
	}
	return Success(res)
}

// build 构造中间件链：核心执行在最内层，用户中间件按注册顺序从外向内包裹，
// 并在最内层始终套一层 Recover（兜底 panic）。
func (e *Executor) build() Next {
	core := func(ctx context.Context, call Call) (any, error) {
		t, ok := e.registry.Get(call.Name)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, call.Name)
		}
		args := call.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		if !json.Valid(args) {
			return nil, fmt.Errorf("%w: arguments must be valid JSON", ErrInvalidArguments)
		}
		return t.Execute(ctx, args)
	}

	handler := Next(core)
	// 最内层 Recover 兜底。
	handler = Recover()(handler)
	// 用户中间件按注册顺序包裹：后注册的越靠外层。
	for i := len(e.middlewares) - 1; i >= 0; i-- {
		handler = e.middlewares[i](handler)
	}
	return handler
}

// panicError 表示工具执行中发生 panic。
type panicError struct {
	call  Call
	value any
}

func (e *panicError) Error() string {
	return fmt.Sprintf("tool %s panicked: %v", e.call.Name, e.value)
}
