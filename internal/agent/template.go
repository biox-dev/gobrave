package agent

import (
	"fmt"
	"strings"

	"github.com/flosch/pongo2/v6"
)

// RenderSystemPromptTemplate 用 vars 渲染 AgentProfile 的系统提示词模板。
//
// Profile.SystemPrompt 是一段 pongo2 模板（例如 "workspace_directory: {{ workspace_directory }}"），
// 其中可引用由 Handler 依据业务 env(type,id) 解析出的变量。未定义的变量按 pongo2 默认行为
// 渲染为空串（配合 {% if %} 可让整行按需出现），因此调用方无需保证变量齐全。
//
// template 为空时返回空串；模板语法错误或渲染失败时返回错误。
func RenderSystemPromptTemplate(template string, vars map[string]any) (string, error) {
	if strings.TrimSpace(template) == "" {
		return "", nil
	}

	tpl, err := pongo2.FromString(template)
	if err != nil {
		return "", fmt.Errorf("agent: parse system prompt template: %w", err)
	}

	ctx := pongo2.Context{}
	for k, v := range vars {
		ctx[k] = v
	}

	rendered, err := tpl.Execute(ctx)
	if err != nil {
		return "", fmt.Errorf("agent: render system prompt template: %w", err)
	}
	return rendered, nil
}
