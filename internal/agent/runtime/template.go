package agentruntime

import (
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// RenderSystemPromptTemplate 用 vars 渲染 AgentProfile 的系统提示词模板。
//
// Profile.SystemPrompt 是一段 pongo2 模板（例如 "workspace_directory: {{ workspace_directory }}"），
// 其中可引用由 Handler 依据业务 env(type,id) 解析出的变量。未定义的变量按 pongo2 默认行为
// 渲染为空串（配合 {% if %} 可让整行按需出现），因此调用方无需保证变量齐全。
//
// template 为空时返回空串；模板语法错误或渲染失败时返回错误。
//
// 渲染实现位于 tool.RenderTemplate（统一模板语法）；技能工具（get_skill /
// load_skill_resource）也复用它，以避免 skill → agent 的循环引用。
func RenderSystemPromptTemplate(template string, vars map[string]any) (string, error) {
	rendered, err := tool.RenderTemplate(template, vars)
	if err != nil {
		return "", fmt.Errorf("agent: %w", err)
	}
	return rendered, nil
}
