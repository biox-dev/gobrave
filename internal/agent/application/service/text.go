package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	. "github.com/biox-dev/gobrave/internal/agent/types"
)

// TextRequest 是一次「无任务、无 Profile、无工具」的纯文本补全请求。
//
// 它面向内部工具型调用（如会话标题生成）：不需要权限、时间线、事件推送，
// 也不应套用用户选择的 Agent Profile（系统提示词 / 技能 / 记忆 / 工具）。
type TextRequest struct {
	// SystemPrompt 是本次补全使用的系统提示词（直接发送，不做模板渲染）。
	SystemPrompt string
	// Prompt 是用户消息。
	Prompt string
	// Provider / Model 是模型路由；为空时由 Client 的默认 Provider 兜底。
	Provider string
	Model    string
	// Facts 提供工作目录等执行事实（custom Provider 需要工作目录）。
	Facts TaskFacts
	// MaxTokens 限制输出长度，0 表示使用模型配置的默认值。
	MaxTokens int
}

// CompleteText 执行一次轻量纯文本补全并返回聚合内容。
//
// 与 RunTaskSync 的区别：不创建 agent_tasks、不写 agent_messages、不广播事件、
// 不解析 Profile、不暴露任何工具。适合标题生成这类短小、可失败降级的内部调用。
func (s *AgentService) CompleteText(ctx context.Context, in TextRequest) (string, error) {
	if s == nil || s.client == nil {
		return "", errors.New("agent: no client configured")
	}

	result, err := s.client.InvokeRuntime(ctx, Request{
		Provider:     strings.TrimSpace(in.Provider),
		ModelID:      strings.TrimSpace(in.Model),
		SystemPrompt: in.SystemPrompt,
		Messages:     []Message{{Role: RoleUser, Content: in.Prompt}},
		MaxTokens:    in.MaxTokens,
		DisableTools: true,
	}, textRuntime{facts: in.Facts})
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("agent: empty result")
	}
	return strings.TrimSpace(result.Content), nil
}

// textRuntime 是内部纯文本补全的最小 Runtime：不落库、不推送、不请求权限。
//
// 由于请求设置了 DisableTools，正常情况下不会触发权限或提问；这些方法只需
// 提供安全兜底（拒绝 / 无 UI），避免 Provider 在异常路径上阻塞。
type textRuntime struct {
	facts TaskFacts
}

func (r textRuntime) Facts() TaskFacts { return r.facts }

func (textRuntime) Emit(context.Context, StreamEvent) error { return nil }

func (textRuntime) RequestPermission(context.Context, Operation) (PermissionDecision, error) {
	return DecisionDeny, nil
}

func (textRuntime) WaitPermission(context.Context, int64) (PermissionDecision, error) {
	return DecisionDeny, nil
}

func (textRuntime) AskUser(context.Context, AskUserInput) (AskUserAnswer, error) {
	return AskUserAnswer{}, ErrNoUserInputResolver
}
