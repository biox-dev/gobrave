package service

import (
	"context"
	"errors"
	"strings"
	"sync"

	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
	coreinterfaces "github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

// 本文件定义「多轮对话」编排层：把一次会话(Session)与一轮执行(Task)拆成两层。
//
//   - Session / Message：跨轮次持久化的会话与历史消息，由 session.Service /
//     message.Service 负责增删改查（参考 opencode 的分层）；
//   - Task：一轮执行，复用 AgentService 既有的 created→running→waiting→completed 状态机。
//
// ChatService 只负责「历史拼接」与「轮次编排」，把每一轮都当作一次普通的 Request
// 交给 AgentService 执行；AgentService 完全感知不到「多轮」这个概念。

// TurnInput 是一次「轮次」的输入，由 HTTP 层构造。
type TurnInput struct {
	UserID    int64
	SessionID int64 // 为 0 则创建新会话
	// ProjectID 是新建会话所属项目（通常取当前用户激活项目），0 时由 ChatService 解析。
	ProjectID int64
	Message   string // 本轮用户输入
	Provider  string
	Model     string
	// RuntimeContext 是本轮调用的运行时上下文（业务类型 / 变量 / 工作目录）。
	RuntimeContext *tool.RuntimeContext
	ProfileID      int64 // AgentProfile ID（为 0 取默认）
	WorkingDir     string
}

// turnState 记录一轮执行期间的中间态（从 CreateTurn 到本轮结束）。
//
// 每轮持有一个 per-session 锁，保证同一会话同一时刻只有一轮在执行；
// 锁在本轮到达终态（done / error / 任务终态回调 / 启动失败）时释放。
type turnState struct {
	sessionID int64
	unlock    func()
}

// ChatService 编排多轮对话：负责历史拼接 + 复用 AgentService 执行每一轮。
type ChatService struct {
	agent    *AgentService
	sessions SessionService
	messages MessageService
	// projects 用于解析当前用户激活项目，使会话按项目隔离。
	projects coreinterfaces.ProjectService

	mu    sync.Mutex
	locks map[int64]*sync.Mutex // sessionID → 会话级轮次锁
	turns map[int64]*turnState  // taskID → 本轮中间态
}

// NewChatService 创建 ChatService。
//
// 构造时把自己的轮次收尾挂到 AgentService 的任务终态回调上：即使 Provider 没有
// 补发 done / error 事件，任务进入终态也会释放会话锁、写入兜底消息。
func NewChatService(agent *AgentService, sessions SessionService, messages MessageService, projects coreinterfaces.ProjectService) *ChatService {
	s := &ChatService{
		agent:    agent,
		sessions: sessions,
		messages: messages,
		projects: projects,
		locks:    make(map[int64]*sync.Mutex),
		turns:    make(map[int64]*turnState),
	}
	if agent != nil {
		agent.SetTaskFinishedHandler(s.onTaskFinished)
	}
	return s
}

// onTaskFinished 是任务终态回调：把「轮次收尾」从流事件驱动改为任务状态机驱动。
//
// finishTurn 自带幂等（以 turns 中的登记为准），因此事件路径（done / error）先触发时，
// 这里的重复调用会直接返回。后端重启后 turns 为空，此时用 Task.SessionID
// 从数据库恢复收尾（补错误消息 + 清理 CurrentTaskID），因此回调仍然有效。
func (s *ChatService) onTaskFinished(_ context.Context, task *Task) {
	if task == nil {
		return
	}
	// failed 且仍有检查点时不要把 CurrentTaskID 清成 0：会话需要继续指向这个
	// 中断任务，前端才能展示「Continue」并从 /agent/task/resume 续跑。
	keepCurrentTask := task.Status == TaskFailed && task.Resumable
	s.finishTurn(task.ID, task.SessionID, task.Error, keepCurrentTask)
}

// lockFor 返回会话级锁（惰性创建）。
func (s *ChatService) lockFor(sessionID int64) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[sessionID]
	if !ok {
		l = &sync.Mutex{}
		s.locks[sessionID] = l
	}
	return l
}

// CreateTurn 准备一轮执行（不启动）：
//
//  1. 取得会话级锁（保证同一会话轮次串行）；
//  2. 加载 / 创建会话，追加 user 消息并持久化；
//  3. 读取完整历史组装 Request（SessionID = 会话 ID），交给 AgentService.CreateTask；
//  4. 登记本轮中间态，返回 task 供上层（handler）在启动前订阅 WS。
//
// 锁在本轮结束时由 finishTurn 释放。
func (s *ChatService) CreateTurn(ctx context.Context, in TurnInput) (*Task, Session, error) {
	sess, err := s.getOrCreate(ctx, in)
	if err != nil {
		return nil, Session{}, err
	}

	l := s.lockFor(sess.ID)
	l.Lock()

	// 追加 user 消息。
	if _, err := s.messages.Create(ctx, MessageCreateParams{
		SessionID: sess.ID,
		Role:      RoleUser,
		Kind:      KindUser,
		Content:   in.Message,
	}); err != nil {
		l.Unlock()
		return nil, Session{}, err
	}

	// 读取完整历史组装请求（快照，避免后续 assistant 回复混入本轮 prompt）。
	history, err := s.messages.List(ctx, sess.ID)
	if err != nil {
		l.Unlock()
		return nil, Session{}, err
	}

	// 用户 / 会话事实统一由 RuntimeContext 承载：这里把会话 ID 与该轮用户写入上下文，
	// 使权限策略、ask_user 归属与记忆检索都能从同一处读取。
	rc := agentruntime.WithRequestFacts(in.RuntimeContext, in.UserID, sess.ID)
	req := Request{
		Provider:       in.Provider,
		ModelID:        in.Model,
		SessionID:      sess.ID,
		RuntimeContext: rc,
		ProfileID:      in.ProfileID,
		Messages:       promptMessagesForRequest(history),
		WorkingDir:     in.WorkingDir,
		Stream:         true, // 会话轮次统一走流式，便于捕获 assistant 文本
	}

	task, err := s.agent.CreateTask(ctx, req)
	if err != nil {
		l.Unlock()
		return nil, Session{}, err
	}

	// 记录当前活跃轮次，供前端刷新 / 切换会话时恢复实时流。
	sess.CurrentTaskID = task.ID
	sess, err = s.sessions.Save(ctx, sess)
	if err != nil {
		l.Unlock()
		return nil, Session{}, err
	}

	s.mu.Lock()
	s.turns[task.ID] = &turnState{sessionID: sess.ID, unlock: l.Unlock}
	s.mu.Unlock()

	return task, sess, nil
}

// StartTurn 启动本轮执行。
//
// 不再注册流式回调：时间线（reasoning / message / tool_call / tool_result）由
// AgentService 的 TimelineRecorder 在轮内即时落库，轮次收尾由任务终态回调驱动。
func (s *ChatService) StartTurn(ctx context.Context, taskID int64) error {
	s.mu.Lock()
	_, ok := s.turns[taskID]
	s.mu.Unlock()
	if !ok {
		return ErrSessionNotFound
	}

	if err := s.agent.StartTask(ctx, taskID); err != nil {
		s.finishTurn(taskID, 0, err.Error(), false)
		return err
	}
	return nil
}

// finishTurn 在本轮结束时收尾：补写错误消息，释放会话锁并清理中间态。
//
// 触发点有两个：
//   - AgentService 的任务终态回调（onTaskFinished，主路径）；
//   - StartTask 启动失败。
//
// 幂等与重启兼容：
//   - 若 turns 中登记了本轮，先“领取”并释放会话锁；
//   - 若未登记（后端重启后内存态丢失），仍按 sessionID 做数据库层收尾，
//     但不释放任何锁（旧进程的锁已随进程消失）；
//   - 错误消息写入前先查重，避免恢复路径与重复回调产生两条相同错误。
func (s *ChatService) finishTurn(taskID, sessionID int64, errMsg string, keepCurrentTask bool) {
	s.mu.Lock()
	ts, registered := s.turns[taskID]
	if registered {
		delete(s.turns, taskID)
		sessionID = ts.sessionID
	}
	s.mu.Unlock()

	// 既没有内存登记、也不知道会话：无从收尾。
	if sessionID <= 0 {
		if registered {
			ts.unlock()
		}
		return
	}

	ctx := context.Background()

	// 时间线（reasoning / message / tool_call / tool_result）与兜底 assistant_final
	// 已由 AgentService/TimelineRecorder 在轮内写入 agent_messages；这里只补「轮末错误」。
	if strings.TrimSpace(errMsg) != "" && !s.hasTaskErrorMessage(ctx, sessionID, taskID) {
		_, _ = s.messages.Create(ctx, MessageCreateParams{
			SessionID: sessionID,
			Role:      RoleAssistant,
			Kind:      KindError,
			TaskID:    taskID,
			Content:   errMsg,
		})
	}

	// 清理当前活跃轮次（只清理指向本任务的指针，避免误清新一轮）。
	// 可续跑的 failed 任务保留指针，供前端恢复「Continue」入口。
	if !keepCurrentTask {
		if sess, err := s.sessions.Get(ctx, sessionID); err == nil && sess.CurrentTaskID == taskID {
			sess.CurrentTaskID = 0
			_, _ = s.sessions.Save(ctx, sess)
		}
	}

	if registered {
		ts.unlock()
	}
}

// hasTaskErrorMessage 判断某任务是否已经写入过 error 消息（恢复路径幂等）。
func (s *ChatService) hasTaskErrorMessage(ctx context.Context, sessionID, taskID int64) bool {
	msgs, err := s.messages.List(ctx, sessionID)
	if err != nil {
		return false
	}
	for _, m := range msgs {
		if m.TaskID == taskID && m.Kind == KindError {
			return true
		}
	}
	return false
}

// promptMessagesForRequest 仅挑选可用于下一轮 prompt 的语义消息。
func promptMessagesForRequest(history []Message) []Message {
	out := make([]Message, 0, len(history))
	for _, m := range history {
		if !shouldIncludeInPrompt(m) {
			continue
		}
		out = append(out, Message{Role: m.Role, Content: m.Content})
	}
	return out
}

func shouldIncludeInPrompt(m Message) bool {
	switch m.Role {
	case RoleUser:
		return true
	case RoleAssistant:
		switch m.Kind {
		case "", KindAssistant, KindAssistantFinal, KindToolCall, KindToolResult:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// ListSessions 分页查询会话。
//
// 会话按用户当前激活项目隔离：先用 GetActiveProjectByUserID 取出活动项目，
// 再按 ProjectID 过滤；userID 为 0 表示全部用户（不做项目过滤）。
func (s *ChatService) ListSessions(ctx context.Context, userID int64, offset, limit int) ([]Session, int64, error) {
	projectID, err := s.activeProjectID(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	return s.sessions.List(ctx, SessionListParams{
		UserID:    userID,
		ProjectID: projectID,
		Offset:    offset,
		Limit:     limit,
	})
}

// activeProjectID 返回用户当前激活项目 ID；未注入项目服务、没有活动项目或 userID 为 0 时返回 0（不按项目过滤）。
func (s *ChatService) activeProjectID(ctx context.Context, userID int64) (int64, error) {
	if s.projects == nil || userID == 0 {
		return 0, nil
	}
	project, err := s.projects.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		// 用户没有激活项目时仓储返回 ErrRecordNotFound，视为 0，保持不按项目过滤的旧行为。
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	if project == nil {
		return 0, nil
	}
	return project.ID, nil
}

// ListMessages 查询某个会话的全部历史消息（按 Seq 升序）。
func (s *ChatService) ListMessages(ctx context.Context, sessionID int64) ([]Message, error) {
	return s.messages.List(ctx, sessionID)
}

// GetSession 查询会话及其完整历史消息。
func (s *ChatService) GetSession(ctx context.Context, id int64) (Session, []Message, error) {
	sess, err := s.sessions.Get(ctx, id)
	if err != nil {
		return Session{}, nil, err
	}
	msgs, err := s.messages.List(ctx, id)
	if err != nil {
		return Session{}, nil, err
	}
	return sess, msgs, nil
}

// getOrCreate 加载已有会话；SessionID 为 0 时创建新会话。
func (s *ChatService) getOrCreate(ctx context.Context, in TurnInput) (Session, error) {
	if in.SessionID == 0 {
		projectID := in.ProjectID
		if projectID == 0 {
			resolved, err := s.activeProjectID(ctx, in.UserID)
			if err != nil {
				return Session{}, err
			}
			projectID = resolved
		}
		return s.sessions.Create(ctx, SessionCreateParams{
			UserID:    in.UserID,
			ProjectID: projectID,
			Provider:  in.Provider,
			Model:     in.Model,
		})
	}
	return s.sessions.Get(ctx, in.SessionID)
}
