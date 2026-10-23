package agentruntime

import (
	"github.com/biox-dev/gobrave/internal/agent/tool"
	. "github.com/biox-dev/gobrave/internal/agent/types"
)

// AskUserToolDefinition 返回 ask_user 工具的定义（名称 / 描述 / 入参 schema）。
//
// 各 Provider 用它把 ask_user 暴露给模型：模型发起调用时，Provider 解析入参为
// AskUserInput，并调用 Runtime.AskUser 阻塞等待用户作答，再把结果回填给模型。
func AskUserToolDefinition() tool.Definition {
	return tool.Definition{
		Name:        AskUserToolName,
		Description: "当需要用户在多个方案间做决策、或需要补充关键信息才能继续时，调用本工具向用户提问。支持单选、多选与文本输入；一次可提出多个问题，界面按一题一页逐题作答（可返回上一题修改）。所有必答问题作答完成后才会把汇总结果返回给你。注意：选择题请把能想到的常见选项都给全（一般 3~6 个），无法穷举时务必设置 allow_custom=true 允许用户自行输入；本题的参考答案不要写进选项描述，避免直接泄露答案。",
		InputSchema: askUserInputSchema(),
	}
}

// askUserInputSchema 描述 AskUserInput 的 JSON Schema。
func askUserInputSchema() map[string]any {
	optionSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"label":       map[string]any{"type": "string", "description": "展示文案"},
			"value":       map[string]any{"type": "string", "description": "提交值"},
			"description": map[string]any{"type": "string", "description": "选项说明"},
		},
		"required": []string{"label", "value"},
	}
	questionSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":           map[string]any{"type": "string", "description": "问题标识，答案按此 ID 回填"},
			"type":         map[string]any{"type": "string", "enum": []string{QuestionTypeSingleChoice, QuestionTypeMultipleChoice, QuestionTypeText}},
			"title":        map[string]any{"type": "string", "description": "问题文本"},
			"description":  map[string]any{"type": "string", "description": "问题补充说明（不要用来透露参考答案）"},
			"required":     map[string]any{"type": "boolean", "description": "是否必答"},
			"allow_custom": map[string]any{"type": "boolean", "description": "是否允许用户自定义输入；选择题无法穷举所有可能时务必设为 true"},
			"placeholder":  map[string]any{"type": "string", "description": "自定义 / 文本输入的占位提示"},
			"options": map[string]any{
				"type":        "array",
				"description": "选项列表（choice 类型）；尽量把常见可能都列出来，用 description 简要说明选项含义，不要写出正确答案",
				"items":       optionSchema,
			},
		},
		"required": []string{"id", "type", "title"},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"request_id":  map[string]any{"type": "string", "description": "请求 ID（可选）"},
			"title":       map[string]any{"type": "string", "description": "提问卡片标题"},
			"description": map[string]any{"type": "string", "description": "补充说明"},
			"questions": map[string]any{
				"type":        "array",
				"minItems":    1,
				"description": "需要用户回答的问题列表（可多个；界面按一题一页逐题作答，所有必答题完成后一次性返回全部答案）",
				"items":       questionSchema,
			},
		},
		"required": []string{"title", "questions"},
	}
}
