package service

import (
	"context"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/logging"
	. "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/logger"
)

const (
	// sessionTitleTimeout 是异步标题生成的整体超时：标题只是锦上添花，
	// 超时即放弃，保留 CreateTurn 写入的占位标题。
	sessionTitleTimeout = 30 * time.Second
	// sessionTitlePromptMaxRunes 限制送给模型的用户问题长度，避免超长输入。
	sessionTitlePromptMaxRunes = 4000
	// sessionTitleMaxTokens 限制标题生成的最大输出 token（给推理型模型留少量余量）。
	sessionTitleMaxTokens = 256

	sessionTitleSystemPrompt = `你是会话标题生成助手。根据用户的第一条问题，生成一个简洁、具体、可检索的标题。
要求：
- 使用与用户问题相同的语言；
- 不超过 20 个汉字（或其他语言约 40 个字符）；
- 只输出标题本身，不要引号、编号、前缀、句末标点或任何解释。`
)

// scheduleSessionTitle 在新建会话后异步生成标题并回写。
//
// 设计要点：
//   - 占位标题已由 CreateTurn 同步写入，这里只做「润色」；失败 / 超时保留占位值；
//   - 使用独立 goroutine + detached ctx，不阻塞本轮对话，也不受 HTTP 请求取消影响；
//   - 调用 AgentService.CompleteText：不建任务、不写时间线、不暴露工具；
//   - 仅当标题仍等于占位值时才覆盖，避免覆盖用户手动改名。
func (s *ChatService) scheduleSessionTitle(ctx context.Context, sess Session, in TurnInput) {
	if s.agent == nil || strings.TrimSpace(in.Message) == "" {
		return
	}

	priorTitle := sess.Title
	// 保留 trace 等 ctx 值，但取消信号与请求生命周期解耦。
	base := context.WithoutCancel(ctx)

	go func() {
		defer logging.RecoverPanic("chat.sessionTitle", nil)

		ctx, cancel := context.WithTimeout(base, sessionTitleTimeout)
		defer cancel()

		title, err := s.generateSessionTitle(ctx, sess, in)
		if err != nil {
			logger.Warnf(ctx, "[Chat] generate session title failed (session=%d): %v", sess.ID, err)
			return
		}
		if title == "" || title == priorTitle {
			return
		}
		if err := s.updateSessionTitle(ctx, sess.ID, priorTitle, title); err != nil {
			logger.Warnf(ctx, "[Chat] update session title failed (session=%d): %v", sess.ID, err)
			return
		}
		logger.Debugf(ctx, "[Chat] session title generated (session=%d): %s", sess.ID, title)
	}()
}

// generateSessionTitle 调用一次无工具的 Agent 补全，根据用户问题生成标题。
func (s *ChatService) generateSessionTitle(ctx context.Context, sess Session, in TurnInput) (string, error) {
	// 与正式轮次保持一致：Profile 指定了 Provider / Model 时以 Profile 为准。
	provider, model := in.Provider, in.Model
	if profile, err := s.agent.ResolveProfile(ctx, in.ProfileID); err == nil && profile != nil {
		if v := strings.TrimSpace(profile.Provider); v != "" {
			provider = v
		}
		if v := strings.TrimSpace(profile.Model); v != "" {
			model = v
		}
	}

	raw, err := s.agent.CompleteText(ctx, TextRequest{
		SystemPrompt: sessionTitleSystemPrompt,
		Prompt:       truncateRunes(strings.TrimSpace(in.Message), sessionTitlePromptMaxRunes),
		Provider:     provider,
		Model:        model,
		MaxTokens:    sessionTitleMaxTokens,
		Facts: TaskFacts{
			UserID: in.UserID,
			// 不绑定真实会话：标题补全不使用工具，但 Provider 可能按 SessionID
			// 清理会话级资源（如持久 shell）；绑定真实会话会干扰正在执行的轮次。
			SessionID:  0,
			ProjectID:  sess.ProjectID,
			WorkingDir: in.WorkingDir,
		},
	})
	if err != nil {
		return "", err
	}
	return normalizeSessionTitle(raw), nil
}

// updateSessionTitle 在标题未被改动（仍是占位值）时写入生成结果。
//
// 条件更新在数据库层原子完成：用户手动改名 / 轮次收尾的整行 Save 都不会被
// 异步标题生成覆盖，也不会把陈旧的 CurrentTaskID 写回去。
func (s *ChatService) updateSessionTitle(ctx context.Context, sessionID int64, priorTitle, title string) error {
	updated, err := s.sessions.UpdateTitleIfUnchanged(ctx, sessionID, priorTitle, title)
	if err != nil {
		return err
	}
	if !updated {
		logger.Debugf(ctx, "[Chat] session title changed concurrently, skip generated title (session=%d)", sessionID)
	}
	return nil
}

// normalizeSessionTitle 清洗模型输出：取首行、压缩空白、去掉常见包裹符号与前缀。
func normalizeSessionTitle(raw string) string {
	title := sessionTitleFromMessage(raw)
	// 反复剥离「标题：」「「...」」等包裹，直到不再变化（最多几轮，避免异常输入死循环）。
	for range 3 {
		trimmed := strings.TrimSpace(strings.Trim(title, "\"'`“”‘’「」【】*_# "))
		for _, prefix := range []string{"标题：", "标题:", "Title:", "title:"} {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
		if trimmed == title {
			break
		}
		title = trimmed
	}
	return strings.TrimSpace(strings.TrimRight(title, "。．.：:；;，,、！!？?"))
}

// truncateRunes 按 rune 截断字符串，避免把中文 / emoji 切成乱码。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
