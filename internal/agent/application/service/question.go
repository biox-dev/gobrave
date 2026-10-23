package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// QuestionManager 负责用户提问（AskUser）的创建、持久化与作答流转。
//
// 与 PermissionManager 完全同构，是“数据库才是状态源”的又一实现：
//   - Create：把提问写入 Repository（pending），等待 UI 作答；
//   - Answer：更新 Repository 状态并唤醒正在阻塞等待的 Agent；
//   - Wait：Agent 侧阻塞等待作答（内存 channel 只做运行时同步，真正状态在 Repository）。
//
// 事件广播由上层 AgentService 负责（单一事件出口），QuestionManager 只维护状态。
type QuestionManager struct {
	repo QuestionRepository

	mu      sync.Mutex
	waiters map[int64]chan AskUserAnswer // questionID → 作答通道
}

// NewQuestionManager 创建提问管理器。
func NewQuestionManager(repo QuestionRepository) *QuestionManager {
	return &QuestionManager{
		repo:    repo,
		waiters: make(map[int64]chan AskUserAnswer),
	}
}

// Create 创建一个 pending 状态的提问请求并返回。
func (m *QuestionManager) Create(ctx context.Context, taskID int64, sessionID string, input AskUserInput) (*QuestionRequest, error) {
	return m.CreateForToolCall(ctx, taskID, sessionID, input, "")
}

// CreateForToolCall 创建提问请求，并记录发起它的工具调用 ID。
func (m *QuestionManager) CreateForToolCall(ctx context.Context, taskID int64, sessionID string, input AskUserInput, toolCallID string) (*QuestionRequest, error) {
	q := NewQuestionRequest(taskID, sessionID, input)
	q.ToolCallID = toolCallID
	if err := m.repo.Create(ctx, q); err != nil {
		return nil, err
	}
	return q, nil
}

// FindByToolCall 返回某任务下由指定工具调用发起的提问（最新一条）。
//
// 恢复执行时用于复用重启前的提问进度 / 已作答结果，避免重复弹出提问。
func (m *QuestionManager) FindByToolCall(ctx context.Context, taskID int64, toolCallID string) (*QuestionRequest, error) {
	if toolCallID == "" {
		return nil, nil
	}
	list, err := m.repo.ListByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for _, q := range list {
		if q.ToolCallID == toolCallID {
			return q, nil
		}
	}
	return nil, nil
}

// Get 按 ID 查询提问请求。
func (m *QuestionManager) Get(ctx context.Context, id int64) (*QuestionRequest, error) {
	return m.repo.Get(ctx, id)
}

// GetPending 返回某任务当前全部待作答的提问请求。
func (m *QuestionManager) GetPending(ctx context.Context, taskID int64) ([]*QuestionRequest, error) {
	return m.repo.ListPendingByTask(ctx, taskID)
}

// Page 分页查询提问请求；taskID 为 0 表示全部任务，statuses 为空表示全部状态。
func (m *QuestionManager) Page(ctx context.Context, offset, limit int, taskID int64, statuses ...QuestionStatus) ([]*QuestionRequest, int64, error) {
	return m.repo.Page(ctx, offset, limit, taskID, statuses...)
}

// Answer 提交用户作答，返回更新后的提问请求。
//
// 支持「一题一页」的逐题作答：本次作答会合并进 q.Answer 作为暂存进度并持久化，
// 提问保持 pending；直到所有必答问题都有答案，才迁移到 answered 并唤醒 Agent。
// 因此用户可以在未全部答完时翻页、回看并修改之前的答案，界面刷新后进度也不丢失。
//
// 提交的问题 ID 必须属于本次提问（防止误把答案提交到别的提问）。
func (m *QuestionManager) Answer(ctx context.Context, id int64, answer AskUserAnswer, by int64) (*QuestionRequest, error) {
	m.mu.Lock()
	q, err := m.repo.Get(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if q.Status != QuestionPending {
		m.mu.Unlock()
		return nil, ErrQuestionNotPending
	}
	if err := validateAnswerIDs(q.Questions, answer); err != nil {
		m.mu.Unlock()
		return nil, err
	}

	merged := answer.Merge(q.Answer, q.Questions)
	q.Answer = &merged

	var completed bool
	if merged.IsComplete(q.Questions) {
		if !q.TransitionTo(QuestionAnswered) {
			m.mu.Unlock()
			return nil, ErrInvalidQuestionTransition
		}
		now := time.Now()
		q.ResolvedAt = &now
		q.ResolvedBy = &by
		completed = true
	}
	if err := m.repo.Update(ctx, q); err != nil {
		m.mu.Unlock()
		return nil, err
	}

	var ch chan AskUserAnswer
	if completed {
		ch = m.waiters[id]
		delete(m.waiters, id)
	}
	m.mu.Unlock()

	// 全部必答问题作答完成：唤醒阻塞中的 Agent。
	if ch != nil {
		ch <- merged
	}
	return q, nil
}

// validateAnswerIDs 校验本次提交的问题 ID 都属于该提问的 questions。
func validateAnswerIDs(questions []Question, answer AskUserAnswer) error {
	known := make(map[string]bool, len(questions))
	for _, q := range questions {
		known[q.ID] = true
	}
	check := func(id string) error {
		if id == "" || known[id] {
			return nil
		}
		return fmt.Errorf("%w: %q", ErrUnknownQuestionID, id)
	}
	for _, ans := range answer.Answers {
		if err := check(ans.ID); err != nil {
			return err
		}
	}
	for _, id := range answer.AnsweredIDs {
		if err := check(id); err != nil {
			return err
		}
	}
	return nil
}

// Cancel 取消一个待作答的提问（任务被取消等）；被取消的等待返回 ErrQuestionCanceled。
func (m *QuestionManager) Cancel(ctx context.Context, id int64) (*QuestionRequest, error) {
	m.mu.Lock()
	q, err := m.repo.Get(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if q.Status != QuestionPending {
		m.mu.Unlock()
		return nil, ErrQuestionNotPending
	}
	if !q.TransitionTo(QuestionCanceled) {
		m.mu.Unlock()
		return nil, ErrInvalidQuestionTransition
	}
	if err := m.repo.Update(ctx, q); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if ch, ok := m.waiters[id]; ok {
		delete(m.waiters, id)
		close(ch)
	}
	m.mu.Unlock()
	return q, nil
}

// Wait 阻塞等待提问的作答。
//
// 返回：
//   - AskUserAnswer：用户已作答（快路径可直接从 Repository 读取）；
//   - error：ctx 取消 / 提问被取消等。
func (m *QuestionManager) Wait(ctx context.Context, id int64) (AskUserAnswer, error) {
	m.mu.Lock()
	q, err := m.repo.Get(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return AskUserAnswer{}, err
	}

	// 快路径：请求已被作答（例如后端重启后恢复，或 answer 先于 wait 到达）。
	switch q.Status {
	case QuestionAnswered:
		m.mu.Unlock()
		if q.Answer != nil {
			return *q.Answer, nil
		}
		return AskUserAnswer{}, nil
	case QuestionCanceled:
		m.mu.Unlock()
		return AskUserAnswer{}, ErrQuestionCanceled
	case QuestionExpired:
		m.mu.Unlock()
		return AskUserAnswer{}, ErrQuestionExpired
	}

	// 慢路径：pending，注册 waiter 后阻塞。
	ch := make(chan AskUserAnswer, 1)
	m.waiters[id] = ch
	m.mu.Unlock()

	select {
	case answer, ok := <-ch:
		if !ok {
			return AskUserAnswer{}, ErrQuestionCanceled
		}
		return answer, nil
	case <-ctx.Done():
		m.mu.Lock()
		delete(m.waiters, id)
		m.mu.Unlock()
		return AskUserAnswer{}, ctx.Err()
	}
}
