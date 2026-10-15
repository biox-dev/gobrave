package agent

import "context"

// OperationType 描述 Agent 想要执行的操作类别。
//
// 未来可继续扩展（install_package / git_push / docker / kubernetes ...），
// 权限策略按类型决定 allow / deny / ask。
type OperationType string

const (
	OperationRead         OperationType = "read"          // 读取文件
	OperationWrite        OperationType = "write"         // 写入 / 修改文件
	OperationDelete       OperationType = "delete"        // 删除文件
	OperationMove         OperationType = "move"          // 移动 / 重命名文件
	OperationExecute      OperationType = "execute"       // 执行命令 / 脚本
	OperationNetwork      OperationType = "network"       // 网络访问
	OperationAnalysisNode OperationType = "analysis_node" // 运行分析节点（区别于直接执行 shell / 脚本）
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

// PermissionDecision 是权限策略 / 决策结果。
type PermissionDecision string

const (
	DecisionAllow PermissionDecision = "allow" // 允许执行
	DecisionDeny  PermissionDecision = "deny"  // 拒绝执行
	DecisionAsk   PermissionDecision = "ask"   // 需要人工确认
)

// UserAgentConfig 是存储在用户记录中的 Agent 配置快照（以 JSON 形式落库）。
//
// 它把「用户选择的 Profile」「用户自定义的权限策略」以及「当前业务上下文」合并为一处：
//   - ProfileID：该用户选中的 AgentProfile ID（0 表示未选择，解析时走默认）；
//   - Permissions：按 OperationType 配置的决策。空 / 未命中某项时，该项回退到
//     defaultPermissionPolicy 的默认决策；
//   - EnvID / EnvType：当前业务上下文（llmEnv）。由前端在进入某个业务页面时通过
//     /auth/agent-config 更新到用户记录上，后端解析系统提示词模板变量与工作目录时
//     直接读取该用户配置，不再由请求体传入，保证上下文与登录用户一一对应。
type UserAgentConfig struct {
	ProfileID   int64                                `json:"profile_id,omitempty,string"`
	Permissions map[OperationType]PermissionDecision `json:"permissions,omitempty"`
	// EnvID 是业务上下文对象 ID（如 project report id / analysis node id / analysis id）。
	EnvID string `json:"env_id,omitempty"`
	// EnvType 是业务上下文类型（见 handler 的 EnvType* 枚举，如 "projectReport" / "analysisNode"）。
	EnvType string `json:"env_type,omitempty"`
}

// DefaultUserAgentConfig 返回默认用户配置（选中内置默认 Profile、权限全部走默认策略）。
func DefaultUserAgentConfig() UserAgentConfig {
	return UserAgentConfig{ProfileID: BuiltinDefaultProfileID}
}

// Env 把用户当前的业务上下文还原为 {id,type} 形式，供 RuntimeContextResolver 解析。
// EnvType 为空时返回 nil，表示当前无业务上下文（回退到默认工作目录）。
func (c UserAgentConfig) Env() map[string]any {
	if c.EnvType == "" {
		return nil
	}
	return map[string]any{"id": c.EnvID, "type": c.EnvType}
}

// HasEnv 报告用户当前是否绑定了业务上下文。
func (c UserAgentConfig) HasEnv() bool {
	return c.EnvType != ""
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
