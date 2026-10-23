package agent

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

// 权限与用户 Agent 配置的基础类型定义在 internal/types（避免 types → agent 的循环依赖：
// internal/types.User 需要直接持有 AgentConfig，而 agent 又需要读取这些类型）。
// 这里用别名保持 agent 包内的既有引用与对外 API 不变。
type (
	// OperationType 描述 Agent 想要执行的操作类别。
	OperationType = types.OperationType
	// PermissionDecision 是权限策略 / 决策结果。
	PermissionDecision = types.PermissionDecision
	// UserAgentConfig 是存储在用户记录中的 Agent 配置快照。
	UserAgentConfig = types.UserAgentConfig
)

// OperationType 取值（别名指向 types 中的定义）。
const (
	OperationRead         = types.OperationRead         // 读取文件
	OperationWrite        = types.OperationWrite        // 写入 / 修改文件
	OperationDelete       = types.OperationDelete       // 删除文件
	OperationMove         = types.OperationMove         // 移动 / 重命名文件
	OperationExecute      = types.OperationExecute      // 执行命令 / 脚本
	OperationNetwork      = types.OperationNetwork      // 网络访问
	OperationAnalysisNode = types.OperationAnalysisNode // 运行分析节点（区别于直接执行 shell / 脚本）
)

// PermissionDecision 取值（别名指向 types 中的定义）。
const (
	DecisionAllow = types.DecisionAllow // 允许执行
	DecisionDeny  = types.DecisionDeny  // 拒绝执行
	DecisionAsk   = types.DecisionAsk   // 需要人工确认
)

// Operation 描述 Agent 想要执行的一个原子操作。
//
// 它刻意与 PermissionRequest 分离：Operation 只描述“要做什么”，
// PermissionRequest 记录“这个操作是否被允许、当前处于什么状态”。
type Operation struct {
	ID   string        `json:"id,omitempty"`
	Type OperationType `json:"type"`

	// read / write / delete / move
	Path string `json:"path,omitempty"`
	// write
	Content string `json:"content,omitempty"`
	// execute
	Command string `json:"command,omitempty"`

	// Description 是人类可读的操作摘要（如 "Execute command: ls -la" /
	// "Replace content in file /a/b.go"）。Provider 有更精确的描述时填入，
	// 供 UI 在批准前展示；为空时前端回退到 Path / Command / Content。
	Description string `json:"description,omitempty"`

	// Metadata 承载操作相关的扩展信息（如命令参数、网络地址等）。
	Metadata map[string]any `json:"metadata,omitempty"`
}

// DefaultUserAgentConfig 返回默认用户配置（选中内置默认 Profile、权限全部走默认策略）。
func DefaultUserAgentConfig() UserAgentConfig {
	return UserAgentConfig{ProfileID: BuiltinDefaultProfileID}
}

// UserAgentConfigProvider 按 userID 读取用户的 Agent 配置（用于权限决策）。
type UserAgentConfigProvider interface {
	GetAgentConfig(ctx context.Context, userID int64) (UserAgentConfig, error)
}

// PermissionPolicy 判断一个 Operation 应被允许、拒绝，还是需要人工确认。
//
// Agent 在执行前把 Operation 交给策略；策略返回 ask 时，由 PermissionManager
// 持久化一个 pending 的 PermissionRequest 并等待 UI 确认。
type PermissionPolicy interface {
	Check(ctx context.Context, userID int64, operation Operation) PermissionDecision
}

// defaultPermissionPolicy 是框架默认策略：
//   - network：直接允许；
//   - read / write / delete / move / execute / analysis_node：需要人工确认；
//   - 未知类型：保守地要求人工确认。
type defaultPermissionPolicy struct{}

// DefaultPermissionPolicy 返回框架内置的默认策略实例。
func DefaultPermissionPolicy() PermissionPolicy { return defaultPermissionPolicy{} }

func (defaultPermissionPolicy) Check(_ context.Context, _ int64, operation Operation) PermissionDecision {
	switch operation.Type {
	case OperationNetwork:
		return DecisionAllow
	case OperationRead, OperationWrite, OperationDelete, OperationMove, OperationExecute, OperationAnalysisNode:
		return DecisionAsk
	default:
		return DecisionAsk
	}
}

// allowAllPermissionPolicy 用于无 UI 的独立调用场景（standalone Runtime）：
// 策略本身仍然存在，但 ask 被降级为 allow，保证链路可跑通。
type allowAllPermissionPolicy struct{}

// AllowAllPermissionPolicy 返回一个“全部放行”的策略实例。
func AllowAllPermissionPolicy() PermissionPolicy { return allowAllPermissionPolicy{} }

func (allowAllPermissionPolicy) Check(_ context.Context, _ int64, _ Operation) PermissionDecision {
	return DecisionAllow
}

// nopUserAgentConfigProvider 是空配置提供者：任何 userID 都返回零值配置，
// 使 userPermissionPolicy 在未注入真实提供者时退化为纯默认策略。
type nopUserAgentConfigProvider struct{}

func (nopUserAgentConfigProvider) GetAgentConfig(context.Context, int64) (UserAgentConfig, error) {
	return UserAgentConfig{}, nil
}

// userPermissionPolicy 先按用户自定义的许可策略决策，未命中时回退到默认策略。
type userPermissionPolicy struct {
	provider UserAgentConfigProvider
	fallback PermissionPolicy
}

// NewUserPermissionPolicy 创建用户感知的权限策略。
//
//   - provider 为 nil 时退化为空配置（全部走默认策略）；
//   - Check 读取 userID 对应的 UserAgentConfig.Permissions，命中则采用该决策，
//     否则（无配置 / 无该类型条目 / 读取失败）回退到 defaultPermissionPolicy。
func NewUserPermissionPolicy(provider UserAgentConfigProvider) PermissionPolicy {
	if provider == nil {
		provider = nopUserAgentConfigProvider{}
	}
	return &userPermissionPolicy{provider: provider, fallback: DefaultPermissionPolicy()}
}

func (p *userPermissionPolicy) Check(ctx context.Context, userID int64, operation Operation) PermissionDecision {
	cfg, err := p.provider.GetAgentConfig(ctx, userID)
	if err == nil {
		if decision, ok := cfg.Permissions[operation.Type]; ok {
			return decision
		}
	}
	return p.fallback.Check(ctx, userID, operation)
}
