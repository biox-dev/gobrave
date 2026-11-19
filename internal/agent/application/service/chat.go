package service

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
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
	RuntimeContext *runtimecontext.RuntimeContext
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
	s.finishTurn(task.ID, task.SessionID, task.ResolvedProfileID, task.Error, keepCurrentTask)
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

	// 首轮对话时用用户输入初始化会话标题（与 opencode 一致：截断到固定长度）。
	// 标题一旦有值（用户改名 / 已有历史）就不再覆盖，避免多轮对话把标题改掉。
	if strings.TrimSpace(sess.Title) == "" {
		if title := sessionTitleFromMessage(in.Message); title != "" {
			sess.Title = title
			if saved, saveErr := s.sessions.Save(ctx, sess); saveErr == nil {
				sess = saved
			}
		}
	}

	l := s.lockFor(sess.ID)
	l.Lock()

	// 追加 user 消息。
	if _, err := s.messages.Create(ctx, MessageCreateParams{
		SessionID: sess.ID,
		Role:      RoleUser,
		Kind:      KindUser,
		UserID:    in.UserID,
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

	// 执行事实（用户 / 会话 / 项目）是任务的属性：随 CreateTask 显式传入，
	// 不再写进 RuntimeContext。
	req := Request{
		Provider:       in.Provider,
		ModelID:        in.Model,
		RuntimeContext: in.RuntimeContext,
		ProfileID:      in.ProfileID,
		Messages:       promptMessagesForRequest(history),
		Stream:         true, // 会话轮次统一走流式，便于捕获 assistant 文本
	}
	facts := TaskFacts{
		UserID:     in.UserID,
		SessionID:  sess.ID,
		ProjectID:  sess.ProjectID,
		WorkingDir: in.WorkingDir,
	}

	task, err := s.agent.CreateTask(ctx, req, facts)
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
		s.finishTurn(taskID, 0, 0, err.Error(), false)
		return err
	}
	return nil
}

// finishTurn 在本轮结束时收尾：补写错误消息，释放会话锁并清理中间态。
//
// ownerID 是错误消息的归属者：错误由 Agent 侧产生，通常传任务的 ResolvedProfileID。
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
func (s *ChatService) finishTurn(taskID, sessionID, ownerID int64, errMsg string, keepCurrentTask bool) {
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
			UserID:    ownerID,
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

// sessionTitleMaxRunes 是自动生成的会话标题最大字符数（与 opencode 一致）。
const sessionTitleMaxRunes = 50

// sessionTitleFromMessage 从首条用户消息生成会话标题。
//
// 只取第一行非空内容并压缩连续空白；超长时截断到 sessionTitleMaxRunes 个字符
// （按 rune 截断，避免把中文 / emoji 切成乱码）。空消息返回空串，调用方保持原标题。
func sessionTitleFromMessage(message string) string {
	for _, line := range strings.Split(message, "\n") {
		title := strings.Join(strings.Fields(line), " ")
		if title == "" {
			continue
		}
		runes := []rune(title)
		if len(runes) > sessionTitleMaxRunes {
			title = string(runes[:sessionTitleMaxRunes]) + "…"
		}
		return title
	}
	return ""
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

// ListSessions 分页查询当前激活项目下的所有会话（不限定发起人）。
//
// 会话按用户当前激活项目隔离：先用 GetActiveProjectByUserID 取出活动项目，
// 再按 ProjectID 过滤，因此同一项目内不同用户发起的会话都会返回（带 username）。
// keyword 会同时匹配会话标题与发起人用户名，结果按 updated_at 倒序。
// 用户没有激活项目时退化为「仅当前用户的会话」，避免把全部项目的会话混在一起。
func (s *ChatService) ListSessions(ctx context.Context, userID int64, keyword string, offset, limit int) ([]Session, int64, error) {
	projectID, err := s.activeProjectID(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	params := SessionListParams{
		ProjectID: projectID,
		Keyword:   keyword,
		Offset:    offset,
		Limit:     limit,
	}
	if projectID == 0 {
		params.UserID = userID
	}
	return s.sessions.List(ctx, params)
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

// ListTaskMessages 查询某个会话中属于指定任务的消息（按 Seq 升序）。
//
// 用于「一轮执行的事实流回填」：刷新 / 切换会话后，前端用该任务的已落库时间线
// （reasoning / assistant / tool_call / tool_result / error）还原执行过程，
// 弥补增量事件（text / reasoning_delta）只广播不落库导致的空白。
//
// 状态源是 agent_messages；同一会话里其它轮次的消息不会返回。
func (s *ChatService) ListTaskMessages(ctx context.Context, sessionID, taskID int64) ([]Message, error) {
	msgs, err := s.messages.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		// user 消息由 CreateTurn 写入时不带 TaskID，按任务回填只取 assistant 侧时间线。
		if m.TaskID != taskID || m.Role != RoleAssistant {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// GetTaskTimeline 按任务 ID 返回该任务的执行时间线（已落库的事实流）。
//
// 访问控制与会话详情一致：会话按项目共享，项目成员都可以读取该会话下任务的
// 时间线（其他用户打开同一会话页时也能回填实时流）；不属于任何会话的一次性
// 任务仍然只允许任务归属者读取。
func (s *ChatService) GetTaskTimeline(ctx context.Context, taskID, userID int64) ([]Message, error) {
	task, err := s.agent.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrTaskNotFound
	}
	// 后台任务（UserID == 0）不对外暴露。
	if task.Facts().UserID == 0 {
		return nil, ErrTaskNotFound
	}
	if task.SessionID <= 0 {
		if task.Facts().UserID != userID {
			return nil, ErrTaskNotFound
		}
		return []Message{}, nil
	}
	// 项目归属优先取任务自带的 ProjectID：它是任务创建时写入的生命周期属性
	// （见 NewTask ← TaskFacts），直接用它省掉一次会话查询。
	projectID := task.ProjectID
	if projectID <= 0 {
		// 历史任务该列为 0（AutoMigrate 补列 / 创建时无激活项目）：回退到会话的项目，
		// 保证与 /agent/session/get 的可见性判定一致。
		sess, err := s.sessions.Get(ctx, task.SessionID)
		if err != nil {
			return nil, err
		}
		if sess.ProjectID <= 0 {
			// 会话未归属项目时退化为「仅发起人可见」。
			if sess.UserID != userID {
				return nil, ErrTaskNotFound
			}
			return s.ListTaskMessages(ctx, task.SessionID, task.ID)
		}
		projectID = sess.ProjectID
	}
	if err := s.ensureProjectMember(ctx, projectID, userID); err != nil {
		return nil, err
	}
	return s.ListTaskMessages(ctx, task.SessionID, task.ID)
}

// ensureProjectMember 校验用户是否属于指定项目。
//
// 会话 / 任务按项目共享：项目成员即可读取；项目未归属（projectID <= 0）或
// 未注入 ProjectService 时一律拒绝。不可访问时返回 ErrTaskNotFound
// （不暴露资源是否存在）。
func (s *ChatService) ensureProjectMember(ctx context.Context, projectID, userID int64) error {
	if projectID <= 0 || userID <= 0 || s.projects == nil {
		return ErrTaskNotFound
	}
	projectUserIDs, err := s.projects.ListProjectUserIDs(ctx, projectID)
	if err != nil {
		return err
	}
	for _, id := range projectUserIDs {
		if id == userID {
			return nil
		}
	}
	return ErrTaskNotFound
}

// 会话详情分页参数默认值与上限。
const (
	// DefaultSessionMessageLimit 是会话详情默认返回的消息条数。
	DefaultSessionMessageLimit = 20
	// MaxSessionMessageLimit 是单次可请求的最大消息条数（防止前端一次拉全量）。
	MaxSessionMessageLimit = 200
)

// SessionMessageQuery 是会话消息的分页参数。
type SessionMessageQuery struct {
	// BeforeSeq 是向上翻页游标：只返回 Seq 小于该值的消息；0 表示取最新一页。
	BeforeSeq int
	// Limit 是本页最大条数；<=0 使用 DefaultSessionMessageLimit。
	Limit int
}

// SessionDetail 是会话详情：会话本体 + 一页历史消息 + 向上翻页游标。
type SessionDetail struct {
	Session
	Messages []Message `json:"messages"`
	// Users 是使用过该会话的用户（发过 user 消息的用户，去重后按用户 ID 升序）。
	// 由消息表实时统计 + users 表回填用户名，不需要额外维护 session_user 关系表。
	Users []SessionUser `json:"users"`
	// HasMore 表示是否还有更早的消息（前端向上滚动时继续加载）。
	HasMore bool `json:"has_more"`
	// NextCursor 是继续向上翻页的游标（本页最小 Seq）；HasMore 为 false 时无意义。
	NextCursor int `json:"next_cursor"`
}

// GetSession 查询会话及一页历史消息（默认最新一页，按 Seq 升序）。
//
// 历史消息可能非常大（长回复 / 工具输出），因此不再一次返回全部：
// 首次只取最新一页，用户向上滚动时把上一页返回的 NextCursor 作为 BeforeSeq 继续加载。
func (s *ChatService) GetSession(ctx context.Context, id int64, q SessionMessageQuery) (SessionDetail, error) {
	sess, err := s.sessions.Get(ctx, id)
	if err != nil {
		return SessionDetail{}, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultSessionMessageLimit
	}
	if limit > MaxSessionMessageLimit {
		limit = MaxSessionMessageLimit
	}

	msgs, hasMore, err := s.messages.ListPage(ctx, id, q.BeforeSeq, limit)
	if err != nil {
		return SessionDetail{}, err
	}

	// 使用过该会话的用户（去重）：直接统计 user 消息，避免维护 session_user 关系表。
	// 与消息分页解耦：每次打开会话 / 翻页都会重新统计（命中复合索引，代价很小），
	// 用户改名或多用户接入后无需任何回填 / 同步逻辑。
	users, err := s.messages.ListParticipants(ctx, id)
	if err != nil {
		return SessionDetail{}, err
	}

	// 本页最小 Seq 即下一页的游标（Seq 升序，第一页为最早一条）。
	nextCursor := 0
	if len(msgs) > 0 {
		nextCursor = msgs[0].Seq
	}
	return SessionDetail{
		Session:    sess,
		Messages:   msgs,
		Users:      users,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}, nil
}

// GetSessionMeta 只返回会话本体（不含历史消息），用于鉴权等轻量场景。
func (s *ChatService) GetSessionMeta(ctx context.Context, id int64) (Session, error) {
	return s.sessions.Get(ctx, id)
}

// ForkSession 把源会话的历史复制成一个新的独立会话。
//
// 与 opencode 的「分叉对话」一致：新会话是顶层会话（出现在会话列表中），
// 消息、Seq 重新分配，不带 CurrentTaskID，因此可以直接继续对话；
// 源会话保持不变，两个分支互不影响（per-session 轮次锁天然隔离）。
func (s *ChatService) ForkSession(ctx context.Context, params SessionForkParams) (SessionDetail, error) {
	forked, err := s.sessions.Fork(ctx, params)
	if err != nil {
		return SessionDetail{}, err
	}
	return s.GetSession(ctx, forked.ID, SessionMessageQuery{})
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
