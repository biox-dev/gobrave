package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// AskUserTool 返回「向用户提问」工具（ask_user）。
//
// 与其他内置工具一样是进程内单例（由容器注册进 tool.Registry）：执行期从工具 ctx
// 取 Runtime（agentruntime.WithToolRuntime，由 Provider 在执行工具前注入），调用
// Runtime.AskUser 阻塞等待用户作答，再把答案格式化为可读文本回填给模型。
// ctx 缺 Runtime 时返回错误（执行器转为失败结果，不会中断整个任务）。
func AskUserTool() tool.Tool {
	def := agentruntime.AskUserToolDefinition()
	return tool.NewFunc(def.Name, def.Description, def.InputSchema,
		func(ctx context.Context, in agenttypes.AskUserInput) (string, error) {
			rt := agentruntime.ToolRuntimeFrom(ctx)
			if rt == nil {
				return "", fmt.Errorf("ask_user: runtime is required in tool context")
			}
			answer, err := rt.AskUser(ctx, in)
			if err != nil {
				return "", err
			}
			return formatAskUserAnswer(in, answer), nil
		})
}

// formatAskUserAnswer 把用户作答格式化为人类可读摘要（作为 ask_user 工具结果文本）。
func formatAskUserAnswer(in agenttypes.AskUserInput, answer agenttypes.AskUserAnswer) string {
	byID := make(map[string]agenttypes.QuestionAnswer, len(answer.Answers))
	for _, ans := range answer.Answers {
		if ans.ID != "" {
			byID[ans.ID] = ans
		}
	}

	var b strings.Builder
	if title := strings.TrimSpace(in.Title); title != "" {
		b.WriteString(title)
		b.WriteString("\n")
	}
	written := 0
	for _, q := range in.Questions {
		ans, ok := byID[q.ID]
		if !ok {
			continue
		}
		text := formatQuestionAnswer(ans)
		if text == "" {
			continue
		}
		if written > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(q.Title))
		b.WriteString(": ")
		b.WriteString(text)
		written++
	}
	// questions 未声明但作答中出现的答案（模型自定义 ID）原样追加。
	declared := make(map[string]bool, len(in.Questions))
	for _, q := range in.Questions {
		declared[q.ID] = true
	}
	for _, ans := range answer.Answers {
		if declared[ans.ID] || ans.ID == "" {
			continue
		}
		text := formatQuestionAnswer(ans)
		if text == "" {
			continue
		}
		if written > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		b.WriteString(ans.ID)
		b.WriteString(": ")
		b.WriteString(text)
		written++
	}
	if written == 0 {
		if data, err := json.Marshal(answer); err == nil {
			return string(data)
		}
		return "用户未作有效选择"
	}
	return b.String()
}

// formatQuestionAnswer 把单个答案拼接为文本（多选用逗号分隔，自定义输入用括号标注）。
func formatQuestionAnswer(ans agenttypes.QuestionAnswer) string {
	parts := make([]string, 0, len(ans.Values)+1)
	parts = append(parts, ans.Values...)
	if value := strings.TrimSpace(ans.Value); value != "" {
		parts = append(parts, value)
	}
	text := strings.Join(parts, ", ")
	if custom := strings.TrimSpace(ans.Custom); custom != "" {
		if text == "" {
			text = custom
		} else if text != custom {
			text += "（自定义：" + custom + "）"
		}
	}
	return text
}
