package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// AgentAnalysis 状态。
const (
	AgentAnalysisPending  = "pending"
	AgentAnalysisRunning  = "running"
	AgentAnalysisDone     = "done"
	AgentAnalysisFailed   = "failed"
	AgentAnalysisTimeout  = "timeout"
	AgentAnalysisCanceled = "canceled"
)

// AgentAnalysis 是一次由 Agent（LLM）发起的一次性容器任务。
//
// 与分析节点不同，它没有脚本 / io_schema / params.json 契约：模型直接给出
// 名称、容器模板与一段 shell 命令，系统在独立工作目录生成 run.sh 后交给
// 容器运行时执行，并把日志回传给模型。
//
// 容器实例通过 (OwnerType=agent_analysis, OwnerID=AgentAnalysis.ID) 关联，
// 工作目录默认是 <projectDir>/agent-analysis/<id>。
type AgentAnalysis struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	ProjectID int64 `json:"project_id,string" gorm:"column:project_id;type:bigint;index;not null"`

	Name string `json:"name" gorm:"type:varchar(255);not null"`

	// TemplateID 是模型选择的容器模板（go_container_template_definition.id）。
	TemplateID int64 `json:"template_id,string" gorm:"index;not null"`

	// Command 是模型给出的 shell 命令，写入 WorkspaceDir/run.sh 后执行。
	Command string `json:"command" gorm:"type:longtext"`

	// ContainerInstanceID 是本次任务对应的容器实例（创建后回填）。
	ContainerInstanceID int64 `json:"container_instance_id,string" gorm:"index"`

	Status string `json:"status" gorm:"type:varchar(32);index;not null;default:pending"`

	ExitCode     int    `json:"exit_code"`
	ErrorMessage string `json:"error_message" gorm:"type:text"`

	// WorkspaceDir 是任务根目录。落库时相对 storage.base_dir 存储，
	// 读取时由 repository 还原成绝对路径；其余路径全部由它派生。
	WorkspaceDir string `json:"workspace_dir" gorm:"type:varchar(1024)"`

	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`

	// 以下是 WorkspaceDir 的派生路径，不落库（gorm:"-"）。
	CommandPath string `json:"command_path" gorm:"-"`
	LogPath     string `json:"log_path" gorm:"-"`
}

func (AgentAnalysis) TableName() string {
	return "go_agent_analysis"
}

func (a *AgentAnalysis) BeforeCreate(_ *gorm.DB) error {
	if a.ID == 0 {
		a.ID = utils.GenerateID()
	}
	return nil
}

// HydrateDerivedPaths 从 WorkspaceDir 推导出 run.sh / 日志路径。
func (a *AgentAnalysis) HydrateDerivedPaths() {
	if a == nil {
		return
	}
	l := utils.NodeLayoutFor(a.WorkspaceDir)
	a.CommandPath = l.CommandPath
	a.LogPath = l.LogPath
}
