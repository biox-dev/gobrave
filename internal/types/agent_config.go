package types

// OperationType 描述 Agent 想要执行的操作类别。
//
// 未来可继续扩展（install_package / git_push / docker / kubernetes ...），
// 权限策略按类型决定 allow / deny / ask。
//
// 定义在 types 包是为了让 User.AgentConfig 能直接持有权限配置，
// 同时避免 types → agent 的循环依赖；agent 包以别名复用这些类型。
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
//     agent.defaultPermissionPolicy 的默认决策；
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
