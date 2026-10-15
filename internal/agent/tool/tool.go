// Package tool 定义 Agent 工具调用（function calling）的基础设施。
//
// 它与具体的 Agent / Provider 解耦，只回答三个问题：
//
//   - 有哪些工具（Definition 描述给模型的元信息）；
//   - 如何组织它们（Registry 按名注册 / 解析）；
//   - 如何调用它们（Executor 解析工具 → 校验参数 → 套用中间件 → 执行）。
//
// 上层 internal/agent 通过 ToolRunner 把工具执行桥接到 Runtime（事件流 + 权限门禁），
// 因此本包不依赖 agent，避免循环引用。
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

// Permission 描述一个工具执行所需的授权动作。
//
// 它是工具自带的“权限元信息”，由上层（ToolRunner / Provider）在调用前消费，
// 映射为 Runtime 的 Operation 以走统一的权限门禁。tool 包本身不执行权限判定，
// 因此这里只做声明，不依赖 agent 包。
type Permission struct {
	// Action 是授权动作：execute / write / read / fetch / delete ...
	Action string
	// PathFrom 可选：从入参 JSON 中取路径的字段名（如 "path" / "file_path"）。
	PathFrom string
	// Description 可选：展示给用户的授权描述；为空时由上层生成默认描述。
	Description string
}

// Definition 描述一个工具，用于暴露给模型（function calling 的 tool 定义）。
//
// InputSchema 是参数的 JSON Schema（object 类型），模型据此生成调用参数。
// Permission 为 nil 表示该工具免授权（与内置只读工具语义一致）。
type Definition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
	Permission  *Permission    `json:"permission,omitempty"`
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

// WithPermission 声明工具的授权动作（链式），返回自身便于继续链式调用。
//
// 声明后，上层在执行该工具前会先走权限门禁（Runtime.RequestPermission）。
func (f *Func[In, Out]) WithPermission(action, pathFrom, description string) *Func[In, Out] {
	f.def.Permission = &Permission{
		Action:      action,
		PathFrom:    pathFrom,
		Description: description,
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
