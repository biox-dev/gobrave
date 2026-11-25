package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestConvertMessagesPassesReasoningContent 验证思考内容会以厂商扩展字段
// reasoning_content 回传给模型。
//
// 思考模式下带 tool_calls 的 assistant 消息缺少该字段会被厂商拒绝
// （DeepSeek: The `reasoning_content` in the thinking mode must be passed back to the API），
// 而 openai-go 没有对应字段，只能作为额外字段并入消息 JSON，因此这里直接校验序列化结果。
func TestConvertMessagesPassesReasoningContent(t *testing.T) {
	c := &openaiClient{}
	messages := c.convertMessages([]Message{{
		Role:      RoleAssistant,
		Reasoning: "think",
		ToolCalls: []ToolCall{{ID: "call-1", Name: "echo", Input: `{"a":1}`}},
	}})
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}

	raw, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := decoded["reasoning_content"].(string); got != "think" {
		t.Fatalf("reasoning_content = %v, want think (json=%s)", decoded["reasoning_content"], raw)
	}
	if calls, ok := decoded["tool_calls"].([]any); !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %v, want the original call", decoded["tool_calls"])
	}
}

// TestConvertMessagesOmitsEmptyReasoningContent 验证没有思考内容时不发送该字段，
// 避免非思考模型收到未知字段。
func TestConvertMessagesOmitsEmptyReasoningContent(t *testing.T) {
	c := &openaiClient{}
	messages := c.convertMessages([]Message{{Role: RoleAssistant, Content: "hi"}})

	raw, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "reasoning_content") {
		t.Fatalf("json = %s, want no reasoning_content", raw)
	}
}
