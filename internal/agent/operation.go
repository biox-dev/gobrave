package agent

import "context"

// OperationType 描述 Agent 想要执行的操作类别。
//
// 未来可继续扩展（install_package / git_push / docker / kubernetes ...），
// 权限策略按类型决定 allow / deny / ask。
type OperationType string

const (
	OperationRead    OperationType = "read"    // 读取文件
	OperationWrite   OperationType = "write"   // 写入 / 修改文件
	OperationDelete  OperationType = "delete"  // 删除文件
	OperationMove    OperationType = "move"    // 移动 / 重命名文件
	OperationExecute OperationType = "execute" // 执行命令 / 脚本
	OperationNetwork OperationType = "network" // 网络访问
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

// PermissionPolicy 判断一个 Operation 应被允许、拒绝，还是需要人工确认。
//
// Agent 在执行前把 Operation 交给策略；策略返回 ask 时，由 PermissionManager
// 持久化一个 pending 的 PermissionRequest 并等待 UI 确认。
type PermissionPolicy interface {
	Check(ctx context.Context, operation Operation) PermissionDecision
}

// defaultPermissionPolicy 是框架默认策略：
//   - read：直接允许；
//   - write / delete / move / execute / network：需要人工确认；
//   - 未知类型：保守地要求人工确认。
type defaultPermissionPolicy struct{}

// DefaultPermissionPolicy 返回框架内置的默认策略实例。
func DefaultPermissionPolicy() PermissionPolicy { return defaultPermissionPolicy{} }

func (defaultPermissionPolicy) Check(_ context.Context, operation Operation) PermissionDecision {
	switch operation.Type {
	case OperationNetwork:
		return DecisionAllow
	case OperationRead, OperationWrite, OperationDelete, OperationMove, OperationExecute:
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

func (allowAllPermissionPolicy) Check(_ context.Context, _ Operation) PermissionDecision {
	return DecisionAllow
}
