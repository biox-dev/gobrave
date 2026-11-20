package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// SessionResourceType 是「会话结果」的资源类型。
type SessionResourceType string

const (
	// SessionResourceTypeScript 对应 create_script / update_script 产出或修改的脚本。
	SessionResourceTypeScript SessionResourceType = "script"
	// SessionResourceTypeWorkflow 对应 create_workflow / update_workflow 产出或修改的工作流。
	SessionResourceTypeWorkflow SessionResourceType = "workflow"
	// SessionResourceTypeAnalysisNode 对应 create_analysis_node_* / update_analysis_node_* 产出或修改的分析节点。
	SessionResourceTypeAnalysisNode SessionResourceType = "analysis_node"
	// SessionResourceTypeAnalysis 对应 create_analysis_workflow / update_analysis_workflow /
	// run_analysis_workflow 创建或修改的分析运行（工作流的一次提交）。
	SessionResourceTypeAnalysis SessionResourceType = "analysis"
)

// Valid 报告资源类型是否是受支持的取值。
func (t SessionResourceType) Valid() bool {
	switch t {
	case SessionResourceTypeScript, SessionResourceTypeWorkflow,
		SessionResourceTypeAnalysisNode, SessionResourceTypeAnalysis:
		return true
	default:
		return false
	}
}

// SessionResource 记录「会话 ↔ 资源」的关联：一个会话可以有多个结果，
// 一个资源也可以被多个会话产生 / 修改（例如别的会话更新了同一脚本）。
//
// (session_id, resource_type, resource_id) 上有唯一索引：重复记录同一关联时
// 由 INSERT ... ON CONFLICT DO NOTHING 静默跳过，保证关联不重复。
type SessionResource struct {
	ID           int64               `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	SessionID    int64               `json:"session_id,string" gorm:"column:session_id;type:bigint;uniqueIndex:uk_agent_session_resource,priority:1;index:idx_agent_session_resource_session_type,priority:1"`
	ResourceType SessionResourceType `json:"resource_type" gorm:"column:resource_type;type:varchar(32);uniqueIndex:uk_agent_session_resource,priority:2;index:idx_agent_session_resource_session_type,priority:2"`
	ResourceID   int64               `json:"resource_id,string" gorm:"column:resource_id;type:bigint;uniqueIndex:uk_agent_session_resource,priority:3"`
	CreatedAt    time.Time           `json:"created_at" gorm:"column:created_at"`
	UpdatedAt    time.Time           `json:"updated_at" gorm:"column:updated_at"`
}

// TableName 返回会话资源关联表的表名。
func (SessionResource) TableName() string { return "agent_session_resources" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (r *SessionResource) BeforeCreate(_ *gorm.DB) error {
	if r.ID == 0 {
		r.ID = utils.GenerateID()
	}
	return nil
}
