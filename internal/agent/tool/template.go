package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/flosch/pongo2/v6"
)

// 本文件提供「模板渲染」的基础实现，供需要按本轮业务变量渲染文本的工具使用。
//
// 模板语言统一为 pongo2（与 Agent Profile 系统提示词同一套写法）。之所以放在 tool 包
// 而不是 agent 包：技能工具（get_skill / load_skill_resource）位于 skill 包，而
// agent 包依赖 skill 包，skill 包再依赖 agent 会形成循环引用；因此渲染能力下沉到
// tool 包，由 agent.RenderSystemPromptTemplate 与技能工具共同复用。

// RenderTemplate 用 vars 渲染一段 pongo2 模板。
//
// 未定义的变量按 pongo2 默认行为渲染为空串（配合 {% if %} 可让整段按需出现），
// 因此调用方无需保证变量齐全。text 为空（或仅空白）时返回空串；
// 模板语法错误或渲染失败时返回错误。
func RenderTemplate(text string, vars map[string]any) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}

	tpl, err := pongo2.FromString(text)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	ctx := pongo2.Context{}
	for k, v := range vars {
		ctx[k] = v
	}

	rendered, err := tpl.Execute(ctx)
	if err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}
	return rendered, nil
}

// RenderRuntimeTemplate 用注入的 RuntimeContext 的业务变量渲染 pongo2 模板。
//
// 变量取自 RuntimeContext.SystemPromptVars（与 Agent Profile 系统提示词同一套变量）。
// 未注入 RuntimeContext 时按空变量渲染：未定义占位符渲染为空串，无业务上下文的场景
// 不受影响；text 为空时直接返回空串。
func RenderRuntimeTemplate(ctx context.Context, text string) (string, error) {
	var vars map[string]any
	if rc := RuntimeContextFrom(ctx); rc != nil {
		vars = rc.Vars()
	}
	return RenderTemplate(text, vars)
}
