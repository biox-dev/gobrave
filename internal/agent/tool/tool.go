// Package tool 定义 Agent 工具调用（function calling）的基础设施。
//
// 它与具体的 Agent / Provider 解耦，只回答三个问题：
//
//   - 有哪些工具（Definition 描述给模型的元信息）；
//   - 如何组织它们（Registry 按名注册 / 解析）；
//   - 如何调用它们（Executor 解析工具 → 校验参数 → 套用中间件 → 执行）。
//
// 上层 internal/agent 通过 ToolRunner 把工具执行事件桥接到 Runtime；权限则由工具
// 在执行期通过 ctx 中的 PermissionRequester 自行请求（见 permission.go）。因此本包
// 不依赖 agent，避免循环引用。
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// 工具相关错误。
var (
	// ErrNotFound 表示工具未注册。
	ErrNotFound = errors.New("tool: tool not found")
	// ErrInvalidArguments 表示工具参数非法（无法解析为输入结构或不是合法 JSON）。
	ErrInvalidArguments = errors.New("tool: invalid arguments")
)

// Definition 描述一个工具，用于暴露给模型（function calling 的 tool 定义）。
//
// InputSchema 是参数的 JSON Schema（object 类型），模型据此生成调用参数。
// 这里只承载模型可见的元信息；授权动作由工具在执行期自行调用
// RequestPermission（见 permission.go）声明，不再作为定义元数据透传给 Provider。
type Definition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// Tool 是可被 Agent 调用的一个工具。
//
// 实现约定：
//   - Execute 返回的 error 会被上层包装为"工具执行失败"的结果（Result.IsError=true），
//     而不会中断整个调用流程——模型可以看到错误信息并尝试纠正。
//   - 返回的 any 会被序列化为结果内容（Result.Content）回传给模型。
type Tool interface {
	// Definition 返回工具的元信息（名称 / 描述 / 参数 schema）。
	Definition() Definition
	// Execute 执行工具；args 是模型生成的参数（JSON，空时视为 {}）。
	Execute(ctx context.Context, args json.RawMessage) (any, error)
}

// Func 把普通函数适配为 Tool：In 为入参结构，Out 为返回结构。
//
// Execute 内部完成 json.Unmarshal(In) → fn(ctx, in) → 返回 Out。
type Func[In, Out any] struct {
	def Definition
	fn  func(context.Context, In) (Out, error)
}

// NewFunc 基于一个强类型函数构建工具。
//
//   - name：唯一工具名（模型用它发起调用）；
//   - description：用途描述（模型据此判断何时使用）；
//   - inputSchema：In 的 JSON Schema（可用 Schema 构造器生成）；
//   - fn：实际执行逻辑。
//
// 需要授权时，用 WithPermission 链式声明（如 NewFunc(...).WithPermission(...)）。
func NewFunc[In, Out any](name, description string, inputSchema map[string]any, fn func(context.Context, In) (Out, error)) *Func[In, Out] {
	return &Func[In, Out]{
		def: Definition{Name: name, Description: description, InputSchema: inputSchema},
		fn:  fn,
	}
}

// PermissionPreview 是授权请求中「批准前预览」的附加信息。
//
// FilePath / Diff 的 JSON 键与 coder edit / write 的授权参数保持一致，上层
// （agent.operationFromToolPermission）会把它们提取到 Operation.Metadata 的
// file_path / diff，前端据此在用户批准前渲染 unified diff。
type PermissionPreview struct {
	FilePath string `json:"file_path,omitempty"`
	Diff     string `json:"diff,omitempty"`
}

// WithPermission 为工具声明授权动作（链式），返回自身便于继续链式调用。
//
// 声明后，工具在解析出入参后、执行前会调用 RequestPermission；被拒绝时返回
// ErrPermissionDenied。pathFrom 指定从入参中取授权路径的字段名（可为空）。
func (f *Func[In, Out]) WithPermission(action, pathFrom, description string) *Func[In, Out] {
	return f.WithPermissionPreview(action, pathFrom, description, nil)
}

// WithPermissionPreview 与 WithPermission 相同，但授权参数改由 preview 生成，
// 用于在批准前向用户展示将要发生的改动（如新建 / 修改 run.sh、params.json）。
//
// preview 返回的内容为空（既无 FilePath 也无 Diff）时回退到工具入参，因此不影响
// 仅有描述、没有 diff 的授权场景；preview 为 nil 时等价于 WithPermission。
func (f *Func[In, Out]) WithPermissionPreview(action, pathFrom, description string, preview func(context.Context, In) PermissionPreview) *Func[In, Out] {
	inner := f.fn
	name := f.def.Name
	f.fn = func(ctx context.Context, in In) (Out, error) {
		var zero Out
		params := any(in)
		if preview != nil {
			if p := preview(ctx, in); p.Diff != "" || p.FilePath != "" {
				params = p
			}
		}
		if err := RequirePermission(ctx, permissionRequestFor(ctx, name, action, pathFrom, description, params)); err != nil {
			return zero, err
		}
		return inner(ctx, in)
	}
	return f
}

// Definition 实现 Tool。
func (f *Func[In, Out]) Definition() Definition { return f.def }

// Execute 实现 Tool：解析入参 → 调用函数。
func (f *Func[In, Out]) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var in In
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	return f.fn(ctx, in)
}
