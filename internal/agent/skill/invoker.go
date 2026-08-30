package skill

import (
	"context"
	"encoding/json"
	"fmt"
)

// Call 是一次待执行的技能调用。
type Call struct {
	ID        string          `json:"id,omitempty"`        // 调用唯一 ID（模型生成）
	Name      string          `json:"name"`                // 技能名
	Arguments json.RawMessage `json:"arguments,omitempty"` // 参数（JSON）
}

// Invoker 执行技能调用：解析技能 → 校验参数 → 套用中间件 → 执行。
//
// 语义约定：所有失败（技能不存在 / 参数非法 / 超时 / panic / 技能返回错误）都
// 收敛为 Result.IsError=true，而不会向上抛出中断整个流程——模型据此纠正行为。
type Invoker struct {
	registry    *Registry
	middlewares []Middleware
}

// InvokerOption 配置 Invoker。
type InvokerOption func(*Invoker)

// WithMiddlewares 追加中间件（先加入的越靠内层）。
func WithMiddlewares(ms ...Middleware) InvokerOption {
	return func(i *Invoker) {
		i.middlewares = append(i.middlewares, ms...)
	}
}

// NewInvoker 创建执行器；默认在最内层附带 Recover 中间件（panic 不逃逸）。
func NewInvoker(registry *Registry, opts ...InvokerOption) *Invoker {
	if registry == nil {
		registry = NewRegistry()
	}
	i := &Invoker{registry: registry}
	for _, o := range opts {
		o(i)
	}
	return i
}

// Invoke 执行一次技能调用，始终返回 Result（不会因技能失败而中断）。
func (i *Invoker) Invoke(ctx context.Context, call Call) Result {
	res, err := i.build()(ctx, call)
	if err != nil {
		return Failure(err)
	}
	return res
}

// build 构造中间件链：核心执行在最内层，用户中间件按注册顺序从外向内包裹，
// 并在最内层始终套一层 Recover（兜底 panic）。
func (i *Invoker) build() Next {
	core := func(ctx context.Context, call Call) (Result, error) {
		s, ok := i.registry.Get(call.Name)
		if !ok {
			return Result{}, fmt.Errorf("%w: %s", ErrNotFound, call.Name)
		}
		args := call.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		if !json.Valid(args) {
			return Result{}, fmt.Errorf("%w: arguments must be valid JSON", ErrInvalidArguments)
		}
		return s.Invoke(ctx, args)
	}

	handler := Next(core)
	// 最内层 Recover 兜底。
	handler = Recover()(handler)
	// 用户中间件按注册顺序包裹：后注册的越靠外层。
	for j := len(i.middlewares) - 1; j >= 0; j-- {
		handler = i.middlewares[j](handler)
	}
	return handler
}
