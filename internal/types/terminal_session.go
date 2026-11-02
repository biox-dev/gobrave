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
type TerminalSession struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	// SessionKey 是稳定的会话标识：term-<userID>-<projectID>-<templateID>。
	SessionKey string `json:"session_key" gorm:"type:varchar(128);uniqueIndex;not null"`

	UserID     int64 `json:"user_id,string" gorm:"index;not null"`
	ProjectID  int64 `json:"project_id,string" gorm:"index;not null"`
	TemplateID int64 `json:"template_id,string" gorm:"index;not null"`

	// RuntimeName 记录创建时使用的运行时（docker / k8s），重启后据此解析运行时实现。
	RuntimeName string `json:"runtime_name" gorm:"type:varchar(64);not null"`
	// RuntimeID 是 docker 容器 ID / k8s workload 标识，用于认领与删除。
	RuntimeID string `json:"runtime_id" gorm:"type:varchar(255);index"`
	// ContainerName 便于排查（docker 容器名 / k8s workload 名）。
	ContainerName string `json:"container_name" gorm:"type:varchar(255)"`

	Status TerminalSessionStatus `json:"status" gorm:"type:varchar(32);index;not null;default:running"`

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
