// Package skill 定义 Agent 技能（skill）调用框架的基础设施。
//
// 技能是比工具（tool）更高一层的可复用能力单元：一个技能既包含"元信息"
// （Definition：名称 / 描述 / 入参 schema，用于暴露给模型做 function calling），
// 也包含"指令正文"（Instructions：注入到上下文中的 markdown 说明），以及
// 可选的"执行体"（Invoke：真正运行技能逻辑，产出结果）。
//
// 与 tool 包一样，本包只回答三个问题：
//
//   - 有哪些技能（Definition / Instructions 描述给模型）；
//   - 如何组织它们（Registry 按名注册 / 解析）；
//   - 如何调用它们（Invoker 解析技能 → 校验参数 → 套用中间件 → 执行）。
//
// 上层 internal/agent 通过 SkillRunner 把技能执行桥接到 Runtime（事件流 + 权限门禁），
// 因此本包不依赖 agent，避免循环引用。
package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// 技能相关错误。
var (
	// ErrNotFound 表示技能未注册。
	ErrNotFound = errors.New("skill: skill not found")
	// ErrInvalidArguments 表示技能参数非法（无法解析为输入结构或不是合法 JSON）。
	ErrInvalidArguments = errors.New("skill: invalid arguments")
)

// Definition 描述一个技能，用于暴露给模型（function calling 的技能定义）。
//
// 与工具不同，技能的 InputSchema 可为 nil：纯指令型技能无需参数，
// 模型只需在上下文里看到 Instructions 并按需"启用"它。
type Definition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// Manifest 是一个技能的完整元信息（Definition + 指令正文 + 版本）。
type Manifest struct {
	Definition
	Version      string `json:"version,omitempty"`
	Instructions string `json:"instructions,omitempty"` // markdown 指令正文，注入到上下文
}

// Skill 是可被 Agent 启用并调用的一个技能。
//
// 实现约定：
//   - Invoke 返回的 error 会被上层包装为"技能执行失败"的结果（Result.IsError=true），
//     而不会中断整个调用流程——模型可以看到错误信息并尝试纠正。
//   - 返回的 any 会被序列化为结果内容（Result.Content）回传给模型。
//   - 纯指令型技能可以只提供 Instructions，Invoke 直接返回指令正文即可。
type Skill interface {
	// Definition 返回技能的元信息（名称 / 描述 / 入参 schema）。
	Definition() Definition
	// Instructions 返回注入到模型上下文的 markdown 指令正文；可为空。
	Instructions() string
	// Invoke 执行技能；args 是模型生成的参数（JSON，空时视为 {}）。
	Invoke(ctx context.Context, args json.RawMessage) (Result, error)
}

// Func 把普通函数适配为 Skill：In 为入参结构，Out 为返回结构。
//
// Instructions 可用于附带指令正文；对纯函数型技能可留空。
type Func[In, Out any] struct {
	manifest Manifest
	fn       func(context.Context, In) (Out, error)
}

// NewFunc 基于一个强类型函数构建技能。
//
//   - manifest：技能的元信息（名称 / 描述 / 入参 schema / 指令正文）；
//   - fn：实际执行逻辑。
func NewFunc[In, Out any](manifest Manifest, fn func(context.Context, In) (Out, error)) *Func[In, Out] {
	return &Func[In, Out]{manifest: manifest, fn: fn}
}

// Definition 实现 Skill。
func (f *Func[In, Out]) Definition() Definition { return f.manifest.Definition }

// Instructions 实现 Skill。
func (f *Func[In, Out]) Instructions() string { return f.manifest.Instructions }

// Version 返回技能版本号（可为空）。
func (f *Func[In, Out]) Version() string { return f.manifest.Version }

// Invoke 实现 Skill：解析入参 → 调用函数。
func (f *Func[In, Out]) Invoke(ctx context.Context, args json.RawMessage) (Result, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var in In
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	out, err := f.fn(ctx, in)
	if err != nil {
		return Result{}, err
	}
	return Success(out), nil
}

// Static 是一个纯指令型技能：只有指令正文，无独立执行逻辑。
//
// Invoke 直接返回指令正文作为结果，便于把技能作为"可调用的提示词片段"使用。
type Static struct {
	manifest Manifest
}

// NewStatic 创建一个纯指令型技能。
func NewStatic(manifest Manifest) *Static {
	return &Static{manifest: manifest}
}

// Definition 实现 Skill。
func (s *Static) Definition() Definition { return s.manifest.Definition }

// Instructions 实现 Skill。
func (s *Static) Instructions() string { return s.manifest.Instructions }

// Version 返回技能版本号（可为空）。
func (s *Static) Version() string { return s.manifest.Version }

// Invoke 实现 Skill：直接返回指令正文。
func (s *Static) Invoke(_ context.Context, _ json.RawMessage) (Result, error) {
	return Success(s.manifest.Instructions), nil
}
