package service

import (
	"context"
	"fmt"
	"strconv"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// sessionRef 把 Task 上的 int64 会话主键转为权限 / 提问子系统使用的字符串会话标识；
// 0 表示不关联会话。
func sessionRef(sessionID int64) string {
	if sessionID <= 0 {
		return ""
	}
	return strconv.FormatInt(sessionID, 10)
}

// taskRuntime 是绑定到具体任务的 Runtime：事件持久化 + 广播，权限持久化 + 阻塞等待。
//
// 它只持有任务事实的只读快照（TaskFacts），而不是 *Task：状态迁移仍然每次从仓库
// 读取最新任务，避免用陈旧的 Status 做非法迁移。
type taskRuntime struct {
	svc   *AgentService
	facts TaskFacts
	// autoApprove 为真表示“无人值守”执行：权限 ask 降级为 allow，不落 pending、
	// 不进入 waiting_permission、不阻塞等待人工决策（见 WithAutoApprove）。
	autoApprove bool
}

// RuntimeOption 定制 TaskRuntime 的行为（如权限是否自动放行）。
type RuntimeOption func(*taskRuntime)

// WithAutoApprove 开关「无人值守权限放行」。
//
// 开启后：策略判定为 ask（需人工确认）的操作直接按 allow 放行，既不持久化 pending
// 权限请求，也不把任务置为 waiting_permission；策略明确判定为 deny 的操作仍然拒绝。
// 关闭（默认）时完全保留原有行为：ask → 持久化 pending → 阻塞等待 UI 决策。
//
// 适用场景：没有 UI 承接权限确认的同步调用（RunTaskSync / RunTaskSyncByID）。
func WithAutoApprove(auto bool) RuntimeOption {
	return func(r *taskRuntime) { r.autoApprove = auto }
}

func NewTaskRuntime(svc *AgentService, task *Task, opts ...RuntimeOption) Runtime {
	r := &taskRuntime{svc: svc, facts: task.Facts()}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// Facts 实现 agentinterfaces.TaskAware：返回本次执行绑定的只读任务事实快照。
func (r *taskRuntime) Facts() TaskFacts { return r.facts }

func (r *taskRuntime) Emit(ctx context.Context, event StreamEvent) error {
	// 事件只有一个出口（publish）：实时推送走 EventBus，完整块事件持久化走 TimelineRecorder。
	// 任务 / 会话 / 用户归属直接取自事实快照，订阅方据此按 UserID 路由，无需再查任务。
	r.svc.publish(ctx, r.facts, EventStream, event, IsBlockEvent(event.Type))
	return nil
}

func (r *taskRuntime) RequestPermission(ctx context.Context, operation Operation) (PermissionDecision, error) {
	// 0) 恢复重放：同一个 tool call 已经在本任务下创建过权限请求（后端重启后重试未完成的工具）。
	//    复用持久化决策，既不重复弹确认，也不会把已批准的操作再次置为 pending。
	if callID := ToolCallIDFromContext(ctx); callID != "" {
		existing, err := r.svc.perms.FindByToolCall(ctx, r.facts.TaskID, callID)
		if err != nil {
			return "", err
		}
		if existing != nil {
			return r.resolveExistingPermission(ctx, existing)
		}
	}

	// 把工具调用 ID 写入 Operation 元数据，供后续恢复路径精确匹配。
	operation = withToolCallMetadata(ctx, operation)

	// 1) 策略先行：直接放行 / 直接拒绝。归属用户来自任务事实，无需调用方传入。
	if decision := r.svc.policy.Check(ctx, r.facts.UserID, operation); decision != DecisionAsk {
		if decision == DecisionDeny {
			return DecisionDeny, ErrPermissionDenied
		}
		return DecisionAllow, nil
	}

	// 2) 无人值守执行（同步调用 / 无 UI）：ask 降级为 allow，直接放行——
	// 不产生 pending 权限请求，也不阻塞等待人工决策。策略 deny 已在上面拒绝。
	if r.autoApprove {
		return DecisionAllow, nil
	}

	// 3) 持久化 pending 权限请求。
	task, err := r.svc.tasks.Get(ctx, r.facts.TaskID)
	if err != nil {
		return "", err
	}
	perm, err := r.svc.perms.Create(ctx, r.facts.TaskID, sessionRef(r.facts.SessionID), operation)
	if err != nil {
		return "", err
	}

	// 4) 任务进入等待权限状态，并广播事件。
	if err := r.svc.transitionTask(ctx, task, TaskWaitingPermission, ""); err != nil {
		return "", err
	}
	r.svc.emitEvent(ctx, r.facts, EventPermissionCreated, perm)

	// 5) 阻塞等待 UI 决策。
	decision, err := r.svc.perms.Wait(ctx, perm.ID)
	if err != nil {
		r.emitPermissionResult(ctx, fmt.Sprintf("error: %v", err))
		return "", err
	}

	// 6) 恢复任务为 running（deny 时 Agent 会返回错误，由 execute 置为 failed）。
	_ = r.svc.transitionTask(ctx, task, TaskRunning, "")
	return decision, nil
}

// resolveExistingPermission 复用一份已持久化的权限决策（恢复执行专用）。
//
// 状态语义与 PermissionManager.Wait 的快路径一致，只是额外负责：
//   - approved → 先 consume 再放行，保证再次崩溃也不会重复确认；
//   - pending → 直接等待原请求（不新建），用户点击允许后即可继续；
//   - denied / expired / canceled → 返回拒绝或错误。
func (r *taskRuntime) resolveExistingPermission(ctx context.Context, p *PermissionRequest) (PermissionDecision, error) {
	task, err := r.svc.tasks.Get(ctx, r.facts.TaskID)
	if err != nil {
		return "", err
	}

	switch p.Status {
	case PermissionApproved:
		if err := r.svc.perms.Consume(ctx, p.ID); err != nil {
			return "", err
		}
		r.restoreRunning(ctx, task)
		return DecisionAllow, nil
	case PermissionConsumed:
		r.restoreRunning(ctx, task)
		return DecisionAllow, nil
	case PermissionDenied:
		r.restoreRunning(ctx, task)
		return DecisionDeny, nil
	case PermissionExpired:
		return DecisionDeny, ErrPermissionExpired
	case PermissionCanceled:
		return DecisionDeny, ErrPermissionCanceled
	case PermissionPending:
		if task.Status != TaskWaitingPermission {
			if err := r.svc.transitionTask(ctx, task, TaskWaitingPermission, ""); err != nil {
				return "", err
			}
		}
		decision, err := r.svc.perms.Wait(ctx, p.ID)
		if err != nil {
			r.emitPermissionResult(ctx, fmt.Sprintf("error: %v", err))
			return "", err
		}
		_ = r.svc.transitionTask(ctx, task, TaskRunning, "")
		return decision, nil
	default:
		return DecisionDeny, nil
	}
}

// restoreRunning 在恢复路径中把“等待权限”的任务改回 running。
func (r *taskRuntime) restoreRunning(ctx context.Context, task *Task) {
	if task != nil && task.Status == TaskWaitingPermission {
		_ = r.svc.transitionTask(ctx, task, TaskRunning, "")
	}
}

// withToolCallMetadata 把当前工具调用 ID 写入 Operation.Metadata（幂等）。
func withToolCallMetadata(ctx context.Context, operation Operation) Operation {
	callID := ToolCallIDFromContext(ctx)
	if callID == "" {
		return operation
	}
	if operation.Metadata == nil {
		operation.Metadata = make(map[string]any, 1)
	}
	if existing, _ := operation.Metadata[OperationMetadataToolCallID].(string); existing == "" {
		operation.Metadata[OperationMetadataToolCallID] = callID
	}
	return operation
}

// emitPermissionResult 把权限决策结果透传给 UI（StreamEventPermissionResult）。
func (r *taskRuntime) emitPermissionResult(ctx context.Context, content string) {
	_ = r.Emit(ctx, StreamEvent{Type: StreamEventPermissionResult, Content: content})
}

// marshalOperation 把 Operation 序列化为 JSON 字符串，作为权限通知事件的 content。
// func marshalOperation(op Operation) string {
// 	data, err := json.Marshal(op)
// 	if err != nil {
// 		return fmt.Sprintf("type=%s", op.Type)
// 	}
// 	return string(data)
// }

func (r *taskRuntime) WaitPermission(ctx context.Context, permissionID int64) (PermissionDecision, error) {
	return r.svc.perms.Wait(ctx, permissionID)
}

// AskUser 创建提问请求（pending）→ 任务置为等待 → 广播事件 → 阻塞等待用户作答。
//
// 与 RequestPermission 同构：持久化 pending 提问后，Agent 阻塞在 QuestionManager.Wait，
// 用户在 UI 作答 → AnswerQuestion 更新 DB 并唤醒 Agent → 这里返回答案继续执行。
func (r *taskRuntime) AskUser(ctx context.Context, input AskUserInput) (AskUserAnswer, error) {
	// 无人值守执行（同步调用 / 无 UI）：没人会作答，快速失败而不是永久阻塞在 Wait 上。
	if r.autoApprove {
		return AskUserAnswer{}, ErrNoUserInputResolver
	}

	callID := ToolCallIDFromContext(ctx)

	// 恢复重放：该 tool call 已经创建过提问（重启后重新执行 ask_user）。
	if callID != "" {
		existing, err := r.svc.questions.FindByToolCall(ctx, r.facts.TaskID, callID)
		if err != nil {
			return AskUserAnswer{}, err
		}
		if existing != nil {
			return r.resolveExistingQuestion(ctx, existing)
		}
	}

	task, err := r.svc.tasks.Get(ctx, r.facts.TaskID)
	if err != nil {
		return AskUserAnswer{}, err
	}
	question, err := r.svc.questions.CreateForToolCall(ctx, r.facts.TaskID, sessionRef(r.facts.SessionID), input, callID)
	if err != nil {
		return AskUserAnswer{}, err
	}

	// 任务进入等待状态，并广播提问事件（UI 据此渲染提问卡片）。
	if err := r.svc.transitionTask(ctx, task, TaskWaitingPermission, ""); err != nil {
		return AskUserAnswer{}, err
	}
	r.svc.emitEvent(ctx, r.facts, EventQuestionCreated, question)

	// 阻塞等待用户作答。
	answer, err := r.svc.questions.Wait(ctx, question.ID)
	if err != nil {
		return AskUserAnswer{}, err
	}

	// 恢复任务为 running（后续结果由 run 决定终态）。
	_ = r.svc.transitionTask(ctx, task, TaskRunning, "")
	return answer, nil
}

// resolveExistingQuestion 复用一份已持久化的提问（恢复执行专用）。
func (r *taskRuntime) resolveExistingQuestion(ctx context.Context, q *QuestionRequest) (AskUserAnswer, error) {
	task, err := r.svc.tasks.Get(ctx, r.facts.TaskID)
	if err != nil {
		return AskUserAnswer{}, err
	}

	switch q.Status {
	case QuestionAnswered:
		// 已作答：直接复用，避免重启后重复创建提问。
		if q.Answer != nil {
			return *q.Answer, nil
		}
		return AskUserAnswer{}, nil
	case QuestionCanceled:
		return AskUserAnswer{}, ErrQuestionCanceled
	case QuestionExpired:
		return AskUserAnswer{}, ErrQuestionExpired
	case QuestionPending:
		if task.Status != TaskWaitingPermission {
			if err := r.svc.transitionTask(ctx, task, TaskWaitingPermission, ""); err != nil {
				return AskUserAnswer{}, err
			}
		}
		answer, err := r.svc.questions.Wait(ctx, q.ID)
		if err != nil {
			return AskUserAnswer{}, err
		}
		_ = r.svc.transitionTask(ctx, task, TaskRunning, "")
		return answer, nil
	default:
		return AskUserAnswer{}, nil
	}
}

// ---- CheckpointRuntime：把 Agent 循环的可恢复检查点持久化到任务行 ----

// LoadCheckpoint 读取当前任务的检查点；不存在时返回 nil。
func (r *taskRuntime) LoadCheckpoint(ctx context.Context) *TaskResumeState {
	if r.svc == nil || r.svc.tasks == nil {
		return nil
	}
	task, err := r.svc.tasks.Get(ctx, r.facts.TaskID)
	if err != nil {
		return nil
	}
	return task.ResumeState
}

// SaveCheckpoint 覆盖写入检查点。
//
// 检查点写入属于“事后必须完成”的持久化动作：使用 WithoutCancel 解绑 Provider / 工具
// 的取消信号，避免工具返回后 ctx 被取消导致检查点静默丢失。
func (r *taskRuntime) SaveCheckpoint(ctx context.Context, state *TaskResumeState) error {
	if r.svc == nil || r.svc.tasks == nil {
		return nil
	}
	return r.svc.tasks.UpdateResumeState(context.WithoutCancel(ctx), r.facts.TaskID, state)
}

// ClearCheckpoint 清理检查点（任务正常结束 / 取消时）。
func (r *taskRuntime) ClearCheckpoint(ctx context.Context) error {
	if r.svc == nil || r.svc.tasks == nil {
		return nil
	}
	return r.svc.tasks.UpdateResumeState(context.WithoutCancel(ctx), r.facts.TaskID, nil)
}
