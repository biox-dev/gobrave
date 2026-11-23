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
// 它把「用户选择的 Profile」与「用户自定义的权限策略」合并为一处：
//   - ProfileID：该用户选中的 AgentProfile ID（0 表示未选择，解析时走默认）；
//   - Permissions：按 OperationType 配置的决策。空 / 未命中某项时，该项回退到
//     agent.defaultPermissionPolicy 的默认决策。
type UserAgentConfig struct {
	ProfileID   int64                                `json:"profile_id,omitempty,string"`
	Permissions map[OperationType]PermissionDecision `json:"permissions,omitempty"`
}
