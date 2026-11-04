package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// TerminalSessionStatus 是终端会话在持久层的状态。
type TerminalSessionStatus string

const (
	// TerminalSessionStatusRunning 表示会话对应的容器仍然存活。
	TerminalSessionStatusRunning TerminalSessionStatus = "running"
	// TerminalSessionStatusClosed 表示会话已被显式关闭或回收。
	TerminalSessionStatusClosed TerminalSessionStatus = "closed"
)

// TerminalSession 记录「容器模板终端」的持久会话。
//
// 背景：会话注册表本身在进程内存里，进程重启后无法知道自己创建过哪些探针容器。
// 这张表把 (userID, projectID, templateID) → runtimeID 的映射落库，使新进程可以
// 「认领」原容器并重新打开 shell（容器与挂载卷都还在，但 shell 是新的）。
//
// 该表不承载 PTY 状态：真正的终端流仍然绑定在服务端 exec 连接上，重启必然结束；
// 历史输出由滚动缓冲文件（container.terminal_buffer_dir）回放。
//
// 一个容器模板可以有多个终端会话：
//   - 用户从 UI 手动创建（POST /container/terminal/session/create）：AgentSessionID=0，
//     同一模板每点一次「新建」都建一条新会话（各自独立容器 / PTY）；
//   - Agent 通过 terminal_exec 创建：AgentSessionID=该 Agent 会话主键（agent_sessions.id），
//     同一 (templateID, agentSessionID) 唯一，保证同一 Agent 会话在同一个模板上只用一个终端。
type TerminalSession struct {
	// ID 是终端会话主键：前端 / 接口对外暴露的 session_id 就是它（字符串形式）。
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	// SessionKey 是稳定的会话标识（<userID>-<projectID>-<templateID>-<agentSessionID>-<id>）。
	// 仅作为唯一键与滚动缓冲文件名的来源，对外不使用。
	SessionKey string `json:"session_key" gorm:"type:varchar(128);uniqueIndex;not null"`

	// Name 是会话展示名：优先取容器模板的 DisplayName，为空时回退模板名 / ID。
	Name string `json:"name" gorm:"type:varchar(255)"`

	// AgentSessionID 是创建该终端会话的 Agent 会话 ID（agent_sessions.id）。
	//
	// 0 表示「用户从 UI 手动创建」；>0 表示由 Agent 的 terminal_exec 创建，
	// 同一 (template_id, agent_session_id) 只会有一条记录（见复合索引）。
	AgentSessionID int64 `gorm:"index:idx_terminal_session_agent,priority:2;not null;default:0"`

	UserID    int64 `json:"user_id,string" gorm:"index;not null"`
	ProjectID int64 `json:"project_id,string" gorm:"index;not null"`
	// TemplateID 与 AgentSessionID 组成复合索引，保证「一个 Agent 会话 + 一个模板」只有一个终端会话。
	TemplateID int64 `json:"template_id,string" gorm:"index;index:idx_terminal_session_agent,priority:1;not null"`

	// RuntimeName 记录创建时使用的运行时（docker / k8s），重启后据此解析运行时实现。
	RuntimeName string `json:"runtime_name" gorm:"type:varchar(64);not null"`
	// RuntimeID 是 docker 容器 ID / k8s workload 标识，用于认领与删除。
	RuntimeID string `json:"runtime_id" gorm:"type:varchar(255);index"`
	// ContainerName 便于排查（docker 容器名 / k8s workload 名）。
	ContainerName string `json:"container_name" gorm:"type:varchar(255)"`

	Status TerminalSessionStatus `json:"status" gorm:"type:varchar(32);index;not null;default:running"`

	// Persistent 标记会话是否为「持久会话」。
	//
	// true  = 只能由空闲 TTL 到期（terminal_session_ttl_minutes）或用户显式关闭
	//         （DELETE /container/terminal/session/{id}）回收；
	// false = 由 Agent 创建的一次性会话：最后一个订阅者（terminal_exec）离开后立即回收。
	//
	// 用户从 UI 创建的会话一律持久；Agent 创建的一次性会话经前端打开后也会被置为 true，
	// 并且此后不会再退回 false。
	Persistent bool `json:"persistent" gorm:"not null;default:false"`

	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LastActiveAt time.Time `json:"last_active_at" gorm:"index"`
}

// TableName 对应表 go_terminal_session（与其它 go_ 前缀表保持一致）。
func (TerminalSession) TableName() string {
	return "go_terminal_session"
}

// BeforeCreate 使用雪花 ID，与其它实体一致。
func (t *TerminalSession) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}
