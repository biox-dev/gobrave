package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
	coreinterfaces "github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
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
//
// 会话工作目录由 Session 统一派生（见 Session.EnsureWorkingDir），调用方只需提供
// 当前激活项目 ID：新建会话时必填，续接已有会话时可省略（沿用会话自身的项目）。
type TurnInput struct {
	UserID int64
	// ProjectID 是当前激活项目 ID：新建会话时必填，续接会话时可为 0。
	ProjectID int64
	SessionID int64  // 为 0 则创建新会话
	Message   string // 本轮用户输入
	Provider  string
	Model     string
	// RuntimeContext 是本轮调用的运行时上下文（业务类型 / 变量 / 工作目录）。
	RuntimeContext *runtimecontext.RuntimeContext
	ProfileID      int64 // AgentProfile ID（为 0 取默认）
	// TaskID 是调用方预先分配的雪花任务 ID（为 0 时由 AgentService 分配）。
	//
	// user 消息在任务创建之前就要写入（历史拼接依赖它），因此任务 ID 必须先确定：
	// 前端据此可以按 task_id 精确删除 / 重试这一轮的消息。
	TaskID int64
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
	// uiActions 用于把异步标题生成等轻量 UI 状态变化推给当前用户。
	uiActions runtimecontext.UIActionPusher
	// projects 用于校验会话 / 任务的项目成员可见性。
	projects coreinterfaces.ProjectService
	// baseDir 是存储根目录（config.Storage.BaseDir）：会话工作目录 = <项目目录>/sessions/<sessionID>。
	baseDir string

	mu    sync.Mutex
	locks map[int64]*sync.Mutex // sessionID → 会话级轮次锁
	turns map[int64]*turnState  // taskID → 本轮中间态
}

// NewChatService 创建 ChatService。
//
// 构造时把自己的轮次收尾挂到 AgentService 的任务终态回调上：即使 Provider 没有
// 补发 done / error 事件，任务进入终态也会释放会话锁、写入兜底消息。
func NewChatService(agent *AgentService, sessions SessionService, messages MessageService, projects coreinterfaces.ProjectService, baseDir string, uiActions ...runtimecontext.UIActionPusher) *ChatService {
	s := &ChatService{
		agent:    agent,
		sessions: sessions,
		messages: messages,
		projects: projects,
		baseDir:  baseDir,
		locks:    make(map[int64]*sync.Mutex),
		turns:    make(map[int64]*turnState),
	}
	if len(uiActions) > 0 {
		s.uiActions = uiActions[0]
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
	// 只要仍保留检查点（resumable）就不要把 CurrentTaskID 清成 0：会话需要继续指向
	// 这个中断任务，前端才能在该轮末尾展示「Continue」并从 /agent/task/resume 续跑。
	// 覆盖三种中断来源：failed（后端重启 / 崩溃 / Provider 错误）与 canceled（用户手动终止）。
	keepCurrentTask := task.Resumable
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
	isNewSession := in.SessionID == 0

	// 会话归属项目：新建会话用调用方提供的激活项目；续接会话沿用会话自身的项目。
	sess, err := s.getOrCreate(ctx, in)
	if err != nil {
		return nil, Session{}, err
	}
	// 历史会话未归属项目（早期数据）：用本轮提供的激活项目补齐。
	if sess.ProjectID <= 0 {
		if in.ProjectID <= 0 {
			return nil, Session{}, fmt.Errorf("session project id is missing")
		}
		sess.ProjectID = in.ProjectID
	}

	// 首轮对话时先用用户输入写入「占位标题」（截断到固定长度），保证会话列表立即可读；
	// 新建会话时会再异步调用 Agent 生成更贴切的标题并覆盖这个占位值。
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

	// 任务 ID 在写 user 消息之前确定：user 消息必须带上 TaskID，
	// 才能按任务精确删除 / 重试（见 DeleteTaskMessages / RetryTurn）。
	// 未显式传入时在这里生成，交给 CreateTask 复用，保证两者一致。
	taskID := in.TaskID
	if taskID <= 0 {
		taskID = utils.GenerateID()
	}

	// 追加 user 消息。
	if _, err := s.messages.Create(ctx, MessageCreateParams{
		SessionID: sess.ID,
		Role:      RoleUser,
		Kind:      KindUser,
		UserID:    in.UserID,
		Content:   in.Message,
		TaskID:    taskID,
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
	// 思考模式 / 推理强度由 Profile 在 run 时覆盖（见 applyProfileOverrides）。
	// 会话工作目录：<项目目录>/sessions/<sessionID>，必须创建成功，否则本轮直接失败。
	workingDir, dirErr := sess.EnsureWorkingDir(s.baseDir)
	if dirErr != nil {
		l.Unlock()
		return nil, Session{}, fmt.Errorf("failed to resolve session working directory: %w", dirErr)
	}

	facts := TaskFacts{
		TaskID:     taskID,
		UserID:     in.UserID,
		SessionID:  sess.ID,
		ProjectID:  sess.ProjectID,
		WorkingDir: workingDir,
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

	// 新建会话：异步用 Agent 根据用户问题生成标题并回写。
	// 不阻塞本轮执行；失败时保留上面的占位标题。
	if isNewSession {
		s.scheduleSessionTitle(ctx, sess, in, workingDir)
	}

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

	// 时间线（reasoning / message / tool_call / tool_result）与兜底 assistant
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
		case "", KindAssistant, KindToolCall, KindToolResult:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// ListSessions 分页查询指定项目下的所有会话（不限定发起人）。
//
// 会话按项目隔离：projectID 由调用方（HTTP 层）按用户当前激活项目提供，
// 因此同一项目内不同用户发起的会话都会返回（带 username）。
// keyword 会同时匹配会话标题与发起人用户名，结果按 updated_at 倒序。
func (s *ChatService) ListSessions(ctx context.Context, projectID int64, keyword string, offset, limit int) ([]Session, int64, error) {
	if projectID <= 0 {
		return nil, 0, fmt.Errorf("project id is required to list sessions")
	}
	params := SessionListParams{
		ProjectID: projectID,
		Keyword:   keyword,
		Offset:    offset,
		Limit:     limit,
	}
	return s.sessions.List(ctx, params)
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
		// 按任务回填只取 assistant 侧时间线：user 消息是这一轮的输入，不作为执行事实回放。
		if m.TaskID != taskID || m.Role != RoleAssistant {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// ErrTurnRunning 表示该轮任务仍在执行，不能删除消息或重试。
var ErrTurnRunning = errors.New("session: turn is still running")

// isTurnRunning 判断任务是否仍在执行（只有本进程的活跃轮次可判定）。
func (s *ChatService) isTurnRunning(taskID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.turns[taskID]
	return ok
}

// GetTaskTimelineOwner 返回任务所属的会话 ID（任务不存在 / 不属于任何会话时报错）。
//
// 供「只传 task_id」的接口（删除 / 重试某一轮）反查会话，再做项目可见性校验。
func (s *ChatService) GetTaskTimelineOwner(ctx context.Context, taskID int64) (int64, error) {
	if taskID <= 0 {
		return 0, ErrTaskNotFound
	}
	task, err := s.agent.GetTask(ctx, taskID)
	if err != nil {
		return 0, err
	}
	if task == nil || task.SessionID <= 0 || task.Facts().UserID == 0 {
		return 0, ErrTaskNotFound
	}
	return task.SessionID, nil
}

// DeleteTaskMessages 删除某个任务关联的全部消息（含该轮的 user 消息与时间线）。
//
// 只删消息，不删任务记录：任务仍可用于审计 / 时间线查询，只是会话里不再展示这一轮。
func (s *ChatService) DeleteTaskMessages(ctx context.Context, taskID int64) error {
	sessionID, err := s.GetTaskTimelineOwner(ctx, taskID)
	if err != nil {
		return err
	}
	if s.isTurnRunning(taskID) {
		return ErrTurnRunning
	}
	if err := s.messages.DeleteByTask(ctx, taskID); err != nil {
		return err
	}
	s.clearCurrentTask(ctx, sessionID, taskID)
	return nil
}

// RetryTurn 重试某一轮：删除该任务关联的消息（含 user 消息），用原来的用户输入
// 在该会话上重新发起一轮（新任务由 Agent 重新生成）。
func (s *ChatService) RetryTurn(ctx context.Context, in TurnInput, taskID int64) (*Task, Session, error) {
	sessionID, err := s.GetTaskTimelineOwner(ctx, taskID)
	if err != nil {
		return nil, Session{}, err
	}
	if s.isTurnRunning(taskID) {
		return nil, Session{}, ErrTurnRunning
	}

	// 原用户输入取自该任务的 user 消消息；没有则说明这一轮没有可重试的提问。
	msgs, err := s.messages.ListByTask(ctx, taskID)
	if err != nil {
		return nil, Session{}, err
	}
	message := ""
	for _, m := range msgs {
		if m.Role == RoleUser {
			message = m.Content
			break
		}
	}
	if message == "" {
		return nil, Session{}, ErrTaskNotFound
	}

	if err := s.messages.DeleteByTask(ctx, taskID); err != nil {
		return nil, Session{}, err
	}
	s.clearCurrentTask(ctx, sessionID, taskID)

	in.SessionID = sessionID
	in.Message = message
	return s.CreateTurn(ctx, in)
}

// clearCurrentTask 清理会话上指向 taskID 的「当前活跃轮次」，避免前端把已删除 /
// 已重试的轮次误判为仍在执行。
func (s *ChatService) clearCurrentTask(ctx context.Context, sessionID, taskID int64) {
	sess, err := s.sessions.Get(ctx, sessionID)
	if err != nil || sess.CurrentTaskID != taskID {
		return
	}
	sess.CurrentTaskID = 0
	_, _ = s.sessions.Save(ctx, sess)
}

// ForkTurn 从某一轮 fork 出一个新会话，并把该轮重新交给 Agent 生成。
//
// 与 ForkSession 的区别：新会话只携带截止到 upToSeq 的历史，且其中有任务归属的消息
// 被重新映射到「为新会话重建的任务」，源会话不受影响。
//
// 执行顺序：先在事务里建好新会话与历史消息，事务提交后再创建新任务并启动，
// 避免把 Agent 调用放进长事务；创建失败时不启动，历史保持完整。
func (s *ChatService) ForkTurn(ctx context.Context, in TurnInput, upToSeq int64, copyFiles bool) (*Task, Session, error) {
	if in.SessionID <= 0 {
		return nil, Session{}, ErrSessionNotFound
	}
	source, err := s.sessions.Get(ctx, in.SessionID)
	if err != nil {
		return nil, Session{}, err
	}
	l := s.lockFor(source.ID)
	l.Lock()

	var (
		taskMap  = make(map[int64]int64)
		keptMsgs []Message
	)
	params := SessionForkParams{
		SourceSessionID: source.ID,
		UserID:          in.UserID,
		CopyFiles:       copyFiles,
		CopyResources:   true,
	}
	if upToSeq > 0 {
		seq := int(upToSeq)
		params.UpToSeq = &seq
	}

	forked, err := s.sessions.ForkWithMessages(ctx, params, func(tx *gorm.DB, target *Session) error {
		msgs, err := s.messages.List(ctx, source.ID)
		if err != nil {
			return err
		}
		seqNo := 0
		for _, m := range msgs {
			if upToSeq > 0 && int64(m.Seq) > upToSeq {
				continue
			}
			if m.Kind == KindError {
				continue
			}
			newTaskID := int64(0)
			if m.TaskID > 0 {
				// 同一源任务的所有消息归到同一个新任务 ID，保留轮次边界。
				newTaskID = taskMap[m.TaskID]
				if newTaskID == 0 {
					newTaskID = utils.GenerateID()
					taskMap[m.TaskID] = newTaskID
				}
			}
			copied := &Message{
				SessionID: target.ID,
				Seq:       seqNo,
				Role:      m.Role,
				Kind:      m.Kind,
				TaskID:    newTaskID,
				UserID:    m.UserID,
				Content:   m.Content,
				Data:      m.Data,
				CreatedAt: target.CreatedAt,
			}
			seqNo++
			if err := tx.Session(&gorm.Session{}).Create(copied).Error; err != nil {
				return err
			}
			keptMsgs = append(keptMsgs, *copied)
		}
		return nil
	})
	if err != nil {
		l.Unlock()
		return nil, Session{}, err
	}

	// 该轮的原始 user 输入（未显式给出时取截止历史里的最后一条用户消息）。
	message := strings.TrimSpace(in.Message)
	if message == "" {
		for i := len(keptMsgs) - 1; i >= 0; i-- {
			if keptMsgs[i].Role == RoleUser {
				message = keptMsgs[i].Content
				break
			}
		}
	}
	if message == "" {
		l.Unlock()
		return nil, Session{}, ErrTaskNotFound
	}

	in.SessionID = forked.ID
	in.Message = message
	in.TaskID = 0
	// CreateTurn 会在新会话上重新加同一把锁（newSessionID 的锁），与这里持有的源会话锁互不影响。
	task, forkedSess, err := s.CreateTurn(ctx, in)
	l.Unlock()
	if err != nil {
		return nil, Session{}, err
	}
	return task, forkedSess, nil
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
	// ActiveTask 是 CurrentTaskID 指向的任务状态快照（仅当 CurrentTaskID 非 0 时返回）。
	// 前端据此一次请求即可判断「运行中 / 等待授权 / 可续跑」，无需再按 taskId 查任务。
	ActiveTask *ActiveTaskStatus `json:"active_task,omitempty"`
}

// ActiveTaskStatus 是会话当前轮次的状态快照。
//
// 只包含前端判定 UI 所需的最小字段：任务 ID 复用 Session.CurrentTaskID，
// request / resume_state / 时间戳等详情仅供任务详情接口使用。
type ActiveTaskStatus struct {
	Status    TaskStatus `json:"status"`
	Resumable bool       `json:"resumable"`
	Error     string     `json:"error,omitempty"`
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
		ActiveTask: s.loadActiveTask(ctx, sess.CurrentTaskID),
	}, nil
}

// loadActiveTask 加载会话当前轮次的状态快照。
//
// 无活跃任务（CurrentTaskID 为 0）或查询失败时返回 nil：调用方按「无快照」处理，
// 由前端保持现状而不是误判为已结束。
func (s *ChatService) loadActiveTask(ctx context.Context, taskID int64) *ActiveTaskStatus {
	if taskID <= 0 || s.agent == nil {
		return nil
	}
	task, err := s.agent.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return nil
	}
	return &ActiveTaskStatus{
		Status:    task.Status,
		Resumable: task.Resumable,
		Error:     task.Error,
	}
}

// GetSessionMeta 只返回会话本体（不含历史消息），用于鉴权等轻量场景。
func (s *ChatService) GetSessionMeta(ctx context.Context, id int64) (Session, error) {
	return s.sessions.Get(ctx, id)
}

// DeleteSession 删除会话，并级联清理其全部关联数据（含子会话）：
// 消息、文件历史、任务、权限 / 提问请求、会话资源关联与归属该会话的记忆。
// 资源关联只删除「会话 ↔ 资源」的关联行，不删除脚本 / 工作流等资源本身。
//
// 会话仍有正在执行的一轮任务时拒绝删除：执行中的轮次会继续向该会话写入消息，
// 删除后会产生孤儿数据。前端需先取消本轮任务，或等本轮结束后再删除。
func (s *ChatService) DeleteSession(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrSessionNotFound
	}
	sess, err := s.sessions.Get(ctx, id)
	if err != nil {
		return err
	}
	if sess.CurrentTaskID != 0 {
		// 只拦截本进程内仍在执行的轮次；failed 且可续跑的任务不在 turns 中，允许删除。
		s.mu.Lock()
		_, running := s.turns[sess.CurrentTaskID]
		s.mu.Unlock()
		if running {
			return ErrSessionBusy
		}
	}
	return s.sessions.Delete(ctx, id)
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

// getOrCreate 加载已有会话；SessionID 为 0 时在 in.ProjectID 下创建新会话。
func (s *ChatService) getOrCreate(ctx context.Context, in TurnInput) (Session, error) {
	if in.SessionID == 0 {
		if in.ProjectID <= 0 {
			return Session{}, fmt.Errorf("project id is required to create session")
		}
		return s.sessions.Create(ctx, SessionCreateParams{
			UserID:    in.UserID,
			ProjectID: in.ProjectID,
			Provider:  in.Provider,
			Model:     in.Model,
		})
	}
	return s.sessions.Get(ctx, in.SessionID)
}
