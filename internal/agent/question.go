package agent

import (
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// QuestionStatus 是用户提问（AskUser）的生命周期状态。
//
// 状态机与 PermissionStatus 同构：
//
//	pending → answered
//	pending → canceled / expired
type QuestionStatus string

const (
	QuestionPending  QuestionStatus = "pending"  // 等待用户作答
	QuestionAnswered QuestionStatus = "answered" // 用户已作答
	QuestionCanceled QuestionStatus = "canceled" // 已取消（任务被取消等）
	QuestionExpired  QuestionStatus = "expired"  // 已过期
)

// 提问相关错误。
var (
	// ErrQuestionNotFound 表示提问请求不存在。
	ErrQuestionNotFound = errors.New("agent: question request not found")
	// ErrQuestionNotPending 表示尝试对非 pending 的提问请求作答。
	ErrQuestionNotPending = errors.New("agent: question request is not pending")
	// ErrQuestionCanceled 表示等待的提问已被取消。
	ErrQuestionCanceled = errors.New("agent: question request canceled")
	// ErrQuestionExpired 表示等待的提问已过期。
	ErrQuestionExpired = errors.New("agent: question request expired")
	// ErrInvalidQuestionTransition 表示非法的提问状态迁移。
	ErrInvalidQuestionTransition = errors.New("agent: invalid question state transition")
	// ErrNoUserInputResolver 表示当前 Runtime 无法向用户提问（无 UI）。
	ErrNoUserInputResolver = errors.New("agent: no user input resolver in runtime")
)

// AskUserToolName 是 ask_user 工具名（模型据此发起调用）。
const AskUserToolName = "ask_user"

// ---- 工具入参 / 出参（模型 <-> 用户界面之间的数据契约） ----

// AskUserInput 是 ask_user 工具的入参，也是持久化到 QuestionRequest 的提问内容。
//
// 结构对齐前端的提问卡片：一个 request 可携带多个 questions，每个 question
// 支持单选 / 多选 / 文本，并可允许用户自定义输入。
type AskUserInput struct {
	RequestID   string     `json:"request_id,omitempty"`  // 模型侧请求 ID（可选，用于回填）
	Title       string     `json:"title,omitempty"`       // 提问卡片标题
	Description string     `json:"description,omitempty"` // 补充说明
	Questions   []Question `json:"questions"`             // 需要用户回答的问题列表
}

// Question 是单个问题。
type Question struct {
	ID          string           `json:"id"`                     // 问题标识（答案按此 ID 回填）
	Type        string           `json:"type"`                   // single_choice / multiple_choice / text
	Title       string           `json:"title"`                  // 问题文本
	Description string           `json:"description,omitempty"`  // 问题补充说明
	Required    bool             `json:"required,omitempty"`     // 是否必答
	Options     []QuestionOption `json:"options,omitempty"`      // 选项（choice 类型）
	AllowCustom bool             `json:"allow_custom,omitempty"` // 是否允许自定义输入
	Placeholder string           `json:"placeholder,omitempty"`  // 自定义 / 文本输入的占位提示
}

// QuestionOption 是单个选项。
type QuestionOption struct {
	Label       string `json:"label"`                 // 展示文案
	Value       string `json:"value"`                 // 提交值
	Description string `json:"description,omitempty"` // 选项说明
}

// 问题类型常量。
const (
	QuestionTypeSingleChoice   = "single_choice"
	QuestionTypeMultipleChoice = "multiple_choice"
	QuestionTypeText           = "text"
)

// AskUserAnswer 是用户对一次提问的全部作答，也是 ask_user 工具回传给模型的结果。
type AskUserAnswer struct {
	RequestID string           `json:"request_id,omitempty"`
	Answers   []QuestionAnswer `json:"answers"`
}

// QuestionAnswer 是单个问题的答案。
//
//   - choice：单选填 Value，多选填 Values；
//   - text：填 Value（Text 为兼容别名，等价于 Value）；
//   - 自定义输入：填 Custom。
type QuestionAnswer struct {
	ID     string   `json:"id"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
	Custom string   `json:"custom,omitempty"`
}

// QuestionRequest 记录一次用户提问及其作答结果，是提问域的持久化对象。
//
// 与 PermissionRequest 一样遵循“数据库才是状态源”原则：Agent 阻塞等待的只是内存
// channel，真正的状态保存在 Repository 中，因此浏览器刷新 / 后端重启后仍可恢复。
type QuestionRequest struct {
	ID          int64          `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	TaskID      int64          `json:"task_id,string" gorm:"column:task_id;type:bigint;index"`
	SessionID   string         `json:"session_id,omitempty" gorm:"column:session_id;type:varchar(64)"`
	RequestID   string         `json:"request_id,omitempty" gorm:"column:request_id;type:varchar(64)"`
	Title       string         `json:"title,omitempty" gorm:"column:title;type:text"`
	Description string         `json:"description,omitempty" gorm:"column:description;type:text"`
	Questions   []Question     `json:"questions" gorm:"serializer:json"`
	Status      QuestionStatus `json:"status" gorm:"column:status;type:varchar(32);index"`
	Answer      *AskUserAnswer `json:"answer,omitempty" gorm:"serializer:json"`

	CreatedAt  time.Time  `json:"created_at" gorm:"column:created_at"`
	UpdatedAt  time.Time  `json:"updated_at" gorm:"column:updated_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty" gorm:"column:resolved_at"`
	ResolvedBy *string    `json:"resolved_by,omitempty" gorm:"column:resolved_by;type:varchar(64)"`
}

// TableName 返回提问请求表的表名。
func (QuestionRequest) TableName() string { return "agent_question_requests" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (q *QuestionRequest) BeforeCreate(_ *gorm.DB) error {
	if q.ID == 0 {
		q.ID = utils.GenerateID()
	}
	return nil
}

// NewQuestionRequest 构建一个 pending 状态的提问请求。
func NewQuestionRequest(taskID int64, sessionID string, input AskUserInput) *QuestionRequest {
	now := time.Now()
	return &QuestionRequest{
		TaskID:      taskID,
		SessionID:   sessionID,
		RequestID:   input.RequestID,
		Title:       input.Title,
		Description: input.Description,
		Questions:   input.Questions,
		Status:      QuestionPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// TransitionTo 校验并执行提问状态迁移；非法迁移返回 false 且不改变状态。
func (q *QuestionRequest) TransitionTo(next QuestionStatus) bool {
	if !canTransitionQuestion(q.Status, next) {
		return false
	}
	q.Status = next
	q.UpdatedAt = time.Now()
	return true
}

func canTransitionQuestion(from, to QuestionStatus) bool {
	switch from {
	case QuestionPending:
		return to == QuestionAnswered ||
			to == QuestionCanceled ||
			to == QuestionExpired
	default:
		// answered / canceled / expired 均为终态。
		return false
	}
}

// AskUserToolDefinition 返回 ask_user 工具的定义（名称 / 描述 / 入参 schema）。
//
// 各 Provider 用它把 ask_user 暴露给模型：模型发起调用时，Provider 解析入参为
// AskUserInput，并调用 Runtime.AskUser 阻塞等待用户作答，再把结果回填给模型。
func AskUserToolDefinition() tool.Definition {
	return tool.Definition{
		Name:        AskUserToolName,
		Description: "当需要用户在多个方案间做决策、或需要补充关键信息才能继续时，调用本工具向用户提问。支持单选、多选与文本输入；一次可提出多个问题。",
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
			"description":  map[string]any{"type": "string", "description": "问题补充说明"},
			"required":     map[string]any{"type": "boolean", "description": "是否必答"},
			"allow_custom": map[string]any{"type": "boolean", "description": "是否允许用户自定义输入"},
			"placeholder":  map[string]any{"type": "string", "description": "自定义 / 文本输入的占位提示"},
			"options": map[string]any{
				"type":  "array",
				"items": optionSchema,
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
				"description": "需要用户回答的问题列表",
				"items":       questionSchema,
			},
		},
		"required": []string{"title", "questions"},
	}
}
