package types

import (
	coretypes "github.com/biox-dev/gobrave/internal/types"
)

// 权限与用户 Agent 配置的基础类型定义在 internal/types（避免 types → agent 的循环依赖：
// internal/types.User 需要直接持有 AgentConfig，而 agent 又需要读取这些类型）。
// 这里用别名保持 agent 包内的既有引用与对外 API 不变。
type (
	// OperationType 描述 Agent 想要执行的操作类别。
	OperationType = coretypes.OperationType
	// PermissionDecision 是权限策略 / 决策结果。
	PermissionDecision = coretypes.PermissionDecision
	// UserAgentConfig 是存储在用户记录中的 Agent 配置快照。
	UserAgentConfig = coretypes.UserAgentConfig
)

// OperationType 取值（别名指向 internal/types 中的定义）。
const (
	OperationRead         = coretypes.OperationRead         // 读取文件
	OperationWrite        = coretypes.OperationWrite        // 写入 / 修改文件
	OperationDelete       = coretypes.OperationDelete       // 删除文件
	OperationMove         = coretypes.OperationMove         // 移动 / 重命名文件
	OperationExecute      = coretypes.OperationExecute      // 执行命令 / 脚本
	OperationNetwork      = coretypes.OperationNetwork      // 网络访问
	OperationAnalysisNode = coretypes.OperationAnalysisNode // 运行分析节点（区别于直接执行 shell / 脚本）
)

// PermissionDecision 取值（别名指向 internal/types 中的定义）。
const (
	DecisionAllow = coretypes.DecisionAllow // 允许执行
	DecisionDeny  = coretypes.DecisionDeny  // 拒绝执行
	DecisionAsk   = coretypes.DecisionAsk   // 需要人工确认
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
