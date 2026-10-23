// Package agent 保留对上层（handler / container / manager / providers / native）的
// 兼容导入面。
//
// 实际分层：
//   - 数据库实体与领域值对象：internal/agent/types
//   - Repository / Service 接口：internal/agent/types/interface
//   - Repository 实现：internal/agent/application/repository
//   - Service 实现：internal/agent/application/service
//   - Client / Registry：internal/agent/client
//   - 运行时（Standalone Runtime / ToolRunner / 工具上下文）：internal/agent/runtime
//   - 权限策略：internal/agent/policy
//
// 本包只做类型别名与函数转发，不包含任何实现，便于旧代码平滑迁移。
package agent

import (
	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
	agentpolicy "github.com/biox-dev/gobrave/internal/agent/policy"
	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// ---- 领域实体 / 值对象（types） ----

type (
	Request         = agenttypes.Request
	Usage           = agenttypes.Usage
	Result          = agenttypes.Result
	StreamEvent     = agenttypes.StreamEvent
	StreamEventType = agenttypes.StreamEventType
	StreamHandler   = agenttypes.StreamHandler

	ReasoningBlock = agenttypes.ReasoningBlock
	ToolCall       = agenttypes.ToolCall
	MessageBlock   = agenttypes.MessageBlock
	TurnBlock      = agenttypes.TurnBlock

	AgentEventType = agenttypes.AgentEventType
	AgentEvent     = agenttypes.AgentEvent
	EventHandler   = agenttypes.EventHandler

	OperationType      = agenttypes.OperationType
	PermissionDecision = agenttypes.PermissionDecision
	UserAgentConfig    = agenttypes.UserAgentConfig
	Operation          = agenttypes.Operation

	PermissionStatus  = agenttypes.PermissionStatus
	PermissionRequest = agenttypes.PermissionRequest

	QuestionStatus  = agenttypes.QuestionStatus
	AskUserInput    = agenttypes.AskUserInput
	Question        = agenttypes.Question
	QuestionOption  = agenttypes.QuestionOption
	AskUserAnswer   = agenttypes.AskUserAnswer
	QuestionAnswer  = agenttypes.QuestionAnswer
	QuestionRequest = agenttypes.QuestionRequest

	Profile       = agenttypes.Profile
	ContextConfig = agenttypes.ContextConfig

	MemoryKind          = agenttypes.MemoryKind
	Memory              = agenttypes.Memory
	MemoryTurn          = agenttypes.MemoryTurn
	MemoryExtractor     = agenttypes.MemoryExtractor
	MemoryExtractorFunc = agenttypes.MemoryExtractorFunc
	MemoryQuery         = agenttypes.MemoryQuery

	TaskStatus          = agenttypes.TaskStatus
	TaskFinishedHandler = agenttypes.TaskFinishedHandler
	Task                = agenttypes.Task

	TaskResumeState  = agenttypes.TaskResumeState
	ResumeToolCall   = agenttypes.ResumeToolCall
	ResumeToolResult = agenttypes.ResumeToolResult
	ResumeMessage    = agenttypes.ResumeMessage
	ResumeTurn       = agenttypes.ResumeTurn

	Session             = agenttypes.Session
	SessionCreateParams = agenttypes.SessionCreateParams
	SessionChildParams  = agenttypes.SessionChildParams
	SessionListParams   = agenttypes.SessionListParams

	Message             = agenttypes.Message
	MessageCreateParams = agenttypes.MessageCreateParams

	File = agenttypes.File
)

// ---- 接口（types/interface） ----

type (
	TaskRepository       = agentinterfaces.TaskRepository
	PermissionRepository = agentinterfaces.PermissionRepository
	QuestionRepository   = agentinterfaces.QuestionRepository
	MemoryRepository     = agentinterfaces.MemoryRepository
	ProfileRepository    = agentinterfaces.ProfileRepository

	SessionStore = agentinterfaces.SessionStore
	MessageStore = agentinterfaces.MessageStore
	HistoryStore = agentinterfaces.HistoryStore

	SessionService = agentinterfaces.SessionService
	MessageService = agentinterfaces.MessageService
	HistoryService = agentinterfaces.HistoryService

	Agent                   = agentinterfaces.Agent
	Runtime                 = agentinterfaces.Runtime
	CheckpointRuntime       = agentinterfaces.CheckpointRuntime
	EventBus                = agentinterfaces.EventBus
	PermissionPolicy        = agentinterfaces.PermissionPolicy
	UserAgentConfigProvider = agentinterfaces.UserAgentConfigProvider
)

// ---- 客户端 / Provider（client） ----

type (
	Provider            = agentclient.Provider
	Options             = agentclient.Options
	ModelProviderConfig = agentclient.ModelProviderConfig
	Registry            = agentclient.Registry
	Client              = agentclient.Client
)

// ---- 服务实现（application/service） ----

type (
	AgentService      = agentservice.AgentService
	ServiceConfig     = agentservice.ServiceConfig
	ChatService       = agentservice.ChatService
	TurnInput         = agentservice.TurnInput
	MemoryConfig      = agentservice.MemoryConfig
	MemoryManager     = agentservice.MemoryManager
	PermissionManager = agentservice.PermissionManager
	QuestionManager   = agentservice.QuestionManager
	ProfileManager    = agentservice.ProfileManager
	TimelineRecorder  = agentservice.TimelineRecorder
)

// ---- 运行时（runtime） ----

type (
	ToolRunner             = agentruntime.ToolRunner
	ToolPermissionResolver = agentruntime.ToolPermissionResolver
	ToolLoop               = agentruntime.ToolLoop
	ToolResultEvent        = agentruntime.ToolResultEvent
)

// ---- 常量（types） ----

const (
	ProviderMock       = agenttypes.ProviderMock
	ProviderClaudeCode = agenttypes.ProviderClaudeCode
	ProviderCodex      = agenttypes.ProviderCodex
	ProviderCopilot    = agenttypes.ProviderCopilot
	ProviderCustom     = agenttypes.ProviderCustom
	DefaultProvider    = agenttypes.DefaultProvider

	RoleSystem    = agenttypes.RoleSystem
	RoleUser      = agenttypes.RoleUser
	RoleAssistant = agenttypes.RoleAssistant
	RoleTool      = agenttypes.RoleTool

	MessageKindUser           = agenttypes.MessageKindUser
	MessageKindAssistant      = agenttypes.MessageKindAssistant
	MessageKindAssistantFinal = agenttypes.MessageKindAssistantFinal
	MessageKindReasoning      = agenttypes.MessageKindReasoning
	MessageKindToolCall       = agenttypes.MessageKindToolCall
	MessageKindToolResult     = agenttypes.MessageKindToolResult

	StreamEventText             = agenttypes.StreamEventText
	StreamEventReasoningDelta   = agenttypes.StreamEventReasoningDelta
	StreamEventTurnStart        = agenttypes.StreamEventTurnStart
	StreamEventTurnEnd          = agenttypes.StreamEventTurnEnd
	StreamEventReasoning        = agenttypes.StreamEventReasoning
	StreamEventMessage          = agenttypes.StreamEventMessage
	StreamEventToolCall         = agenttypes.StreamEventToolCall
	StreamEventToolResult       = agenttypes.StreamEventToolResult
	StreamEventPermission       = agenttypes.StreamEventPermission
	StreamEventPermissionResult = agenttypes.StreamEventPermissionResult
	StreamEventDone             = agenttypes.StreamEventDone
	StreamEventError            = agenttypes.StreamEventError

	OperationRead         = agenttypes.OperationRead
	OperationWrite        = agenttypes.OperationWrite
	OperationDelete       = agenttypes.OperationDelete
	OperationMove         = agenttypes.OperationMove
	OperationExecute      = agenttypes.OperationExecute
	OperationNetwork      = agenttypes.OperationNetwork
	OperationAnalysisNode = agenttypes.OperationAnalysisNode

	DecisionAllow = agenttypes.DecisionAllow
	DecisionDeny  = agenttypes.DecisionDeny
	DecisionAsk   = agenttypes.DecisionAsk

	PermissionPending  = agenttypes.PermissionPending
	PermissionApproved = agenttypes.PermissionApproved
	PermissionDenied   = agenttypes.PermissionDenied
	PermissionExpired  = agenttypes.PermissionExpired
	PermissionCanceled = agenttypes.PermissionCanceled
	PermissionConsumed = agenttypes.PermissionConsumed

	QuestionPending  = agenttypes.QuestionPending
	QuestionAnswered = agenttypes.QuestionAnswered
	QuestionCanceled = agenttypes.QuestionCanceled
	QuestionExpired  = agenttypes.QuestionExpired

	QuestionTypeSingleChoice   = agenttypes.QuestionTypeSingleChoice
	QuestionTypeMultipleChoice = agenttypes.QuestionTypeMultipleChoice
	QuestionTypeText           = agenttypes.QuestionTypeText

	AskUserToolName = agenttypes.AskUserToolName

	MemoryKindFact    = agenttypes.MemoryKindFact
	MemoryKindSummary = agenttypes.MemoryKindSummary
	MemoryKindNote    = agenttypes.MemoryKindNote
	MemoryKindEvent   = agenttypes.MemoryKindEvent

	TaskCreated           = agenttypes.TaskCreated
	TaskRunning           = agenttypes.TaskRunning
	TaskWaitingPermission = agenttypes.TaskWaitingPermission
	TaskCompleted         = agenttypes.TaskCompleted
	TaskFailed            = agenttypes.TaskFailed
	TaskCanceled          = agenttypes.TaskCanceled

	EventTaskCreated        = agenttypes.EventTaskCreated
	EventTaskStarted        = agenttypes.EventTaskStarted
	EventTaskWaiting        = agenttypes.EventTaskWaiting
	EventTaskCompleted      = agenttypes.EventTaskCompleted
	EventTaskFailed         = agenttypes.EventTaskFailed
	EventTaskCanceled       = agenttypes.EventTaskCanceled
	EventPermissionCreated  = agenttypes.EventPermissionCreated
	EventPermissionResolved = agenttypes.EventPermissionResolved
	EventQuestionCreated    = agenttypes.EventQuestionCreated
	EventQuestionResolved   = agenttypes.EventQuestionResolved
	EventMemorySaved        = agenttypes.EventMemorySaved
	EventMemoryDeleted      = agenttypes.EventMemoryDeleted
	EventStream             = agenttypes.EventStream

	BuiltinDefaultProfileID   = agenttypes.BuiltinDefaultProfileID
	BuiltinAnalysisCoderID    = agenttypes.BuiltinAnalysisCoderID
	BuiltinArticleWriterID    = agenttypes.BuiltinArticleWriterID
	BuiltinSummaryProfileID   = agenttypes.BuiltinSummaryProfileID
	BuiltinDefaultProfileName = agenttypes.BuiltinDefaultProfileName

	BuiltinPromptDefault       = agenttypes.BuiltinPromptDefault
	BuiltinPromptAnalysisCoder = agenttypes.BuiltinPromptAnalysisCoder
	BuiltinPromptArticleWriter = agenttypes.BuiltinPromptArticleWriter
	BuiltinPromptSummary       = agenttypes.BuiltinPromptSummary
	BuiltinPromptBaseCoder     = agenttypes.BuiltinPromptBaseCoder

	ResumeStateVersion          = agenttypes.ResumeStateVersion
	OperationMetadataToolCallID = agenttypes.OperationMetadataToolCallID
	TitleSessionTitle           = agenttypes.TitleSessionTitle
	InitialVersion              = agenttypes.InitialVersion
)

// ---- 错误 ----

var (
	ErrNotImplemented              = agenttypes.ErrNotImplemented
	ErrPermissionDenied            = agenttypes.ErrPermissionDenied
	ErrPermissionNotFound          = agenttypes.ErrPermissionNotFound
	ErrPermissionNotPending        = agenttypes.ErrPermissionNotPending
	ErrPermissionExpired           = agenttypes.ErrPermissionExpired
	ErrPermissionCanceled          = agenttypes.ErrPermissionCanceled
	ErrInvalidPermissionTransition = agenttypes.ErrInvalidPermissionTransition
	ErrQuestionNotFound            = agenttypes.ErrQuestionNotFound
	ErrQuestionNotPending          = agenttypes.ErrQuestionNotPending
	ErrQuestionCanceled            = agenttypes.ErrQuestionCanceled
	ErrQuestionExpired             = agenttypes.ErrQuestionExpired
	ErrInvalidQuestionTransition   = agenttypes.ErrInvalidQuestionTransition
	ErrNoUserInputResolver         = agenttypes.ErrNoUserInputResolver
	ErrUnknownQuestionID           = agenttypes.ErrUnknownQuestionID
	ErrMemoryNotFound              = agenttypes.ErrMemoryNotFound
	ErrMemoryNotConfigured         = agenttypes.ErrMemoryNotConfigured
	ErrProfileNotFound             = agenttypes.ErrProfileNotFound
	ErrProfileNameRequired         = agenttypes.ErrProfileNameRequired
	ErrTaskNotFound                = agenttypes.ErrTaskNotFound
	ErrTaskAlreadyRunning          = agenttypes.ErrTaskAlreadyRunning
	ErrTaskNotResumable            = agenttypes.ErrTaskNotResumable
	ErrInvalidTaskTransition       = agenttypes.ErrInvalidTaskTransition
	ErrSessionNotFound             = agenttypes.ErrSessionNotFound
	ErrInvalidParentSession        = agenttypes.ErrInvalidParentSession
	ErrMessageNotFound             = agenttypes.ErrMessageNotFound
	ErrHistoryNotFound             = agenttypes.ErrHistoryNotFound
	ErrDuplicateVersion            = agenttypes.ErrDuplicateVersion
	ErrNoPermissionResolver        = agentruntime.ErrNoPermissionResolver
)

// ---- 函数 / 构造器 ----

var (
	NewTask                = agenttypes.NewTask
	NewPermissionRequest   = agenttypes.NewPermissionRequest
	NewQuestionRequest     = agenttypes.NewQuestionRequest
	NewMemory              = agenttypes.NewMemory
	MockMemoryExtractor    = agenttypes.MockMemoryExtractor
	BuildMemoryContext     = agenttypes.BuildMemoryContext
	DefaultUserAgentConfig = agenttypes.DefaultUserAgentConfig
	IsBuiltinProfileID     = agenttypes.IsBuiltinProfileID
	WithToolCallID         = agenttypes.WithToolCallID
	ToolCallIDFromContext  = agenttypes.ToolCallIDFromContext

	NewService            = agentservice.NewService
	NewChatService        = agentservice.NewChatService
	NewMemoryManager      = agentservice.NewMemoryManager
	NewPermissionManager  = agentservice.NewPermissionManager
	NewQuestionManager    = agentservice.NewQuestionManager
	NewProfileManager     = agentservice.NewProfileManager
	NewTimelineRecorder   = agentservice.NewTimelineRecorder
	NewEventBus           = agentservice.NewEventBus
	BuiltinProfiles       = agentservice.BuiltinProfiles
	EnsureBuiltinProfiles = agentservice.EnsureBuiltinProfiles
	SetBuiltinPromptDir   = agentservice.SetBuiltinPromptDir
	ReadBuiltinPrompt     = agentservice.ReadBuiltinPrompt
	ProjectTimelineEvent  = agentservice.ProjectTimelineEvent
	ReasoningBlockContent = agentservice.ReasoningBlockContent
	MessageBlockContent   = agentservice.MessageBlockContent
	TimelineEventSummary  = agentservice.TimelineEventSummary

	NewClient   = agentclient.NewClient
	NewRegistry = agentclient.NewRegistry

	NewStandaloneRuntime          = agentruntime.NewStandaloneRuntime
	NewToolRunner                 = agentruntime.NewToolRunner
	NewToolLoop                   = agentruntime.NewToolLoop
	AskUserToolDefinition         = agentruntime.AskUserToolDefinition
	RenderSystemPromptTemplate    = agentruntime.RenderSystemPromptTemplate
	ToolRuntimeContextFromRequest = agentruntime.ToolRuntimeContextFromRequest
	WithToolPermission            = agentruntime.WithToolPermission
	WithToolRuntimeContext        = agentruntime.WithToolRuntimeContext
	WithToolRuntime               = agentruntime.WithToolRuntime
	WithAgentAnalysisRunner       = agentruntime.WithAgentAnalysisRunner
	WithLSPClients                = agentruntime.WithLSPClients
	WithSkills                    = agentruntime.WithSkills
	WithProjectResourceBrowser    = agentruntime.WithProjectResourceBrowser
	WithRequestFacts              = agentruntime.WithRequestFacts

	DefaultPermissionPolicy  = agentpolicy.DefaultPermissionPolicy
	AllowAllPermissionPolicy = agentpolicy.AllowAllPermissionPolicy
	NewUserPermissionPolicy  = agentpolicy.NewUserPermissionPolicy
)

// RequestUserID / RequestSessionID 是 Request 的方法，别名类型自动继承。
