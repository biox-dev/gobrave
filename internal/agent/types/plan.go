package types

import (
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// ErrPlanNotFound 表示计划不存在。
var ErrPlanNotFound = errors.New("plan: not found")

// PlanSource 标识计划节点的内容来源类型。
//
// 取值与 SessionResourceType 一致（custom 除外），因此计划节点可以直接引用
// 「某个会话产出的资源」；计划只保存来源类型与资源 ID，不保存资源详情——
// agent 拿到 ID 后自行调用对应工具（get_analysis_node / get_script 等）查询详情。
type PlanSource string

const (
	// PlanSourceCustom 是自定义节点（不引用外部资源）。
	PlanSourceCustom PlanSource = "custom"
	// PlanSourceAnalysis 指向一次分析运行（Analysis）。
	PlanSourceAnalysis PlanSource = "analysis"
	// PlanSourceAnalysisNode 指向一个分析节点（AnalysisNode）。
	PlanSourceAnalysisNode PlanSource = "analysis_node"
	// PlanSourceScript 指向一个脚本（Script）。
	PlanSourceScript PlanSource = "script"
	// PlanSourceWorkflow 指向一个工作流（Workflow）。
	PlanSourceWorkflow PlanSource = "workflow"
)

// Valid 计划来源类型是否是受支持的取值。
func (s PlanSource) Valid() bool {
	switch s {
	case PlanSourceCustom, PlanSourceAnalysis, PlanSourceAnalysisNode,
		PlanSourceScript, PlanSourceWorkflow:
		return true
	default:
		return false
	}
}

// PlanItem 是计划中的一个节点（可嵌套）。
//
// ParentID 指向同一计划内的父节点（0 表示根），SortOrder 为同级顺序：前端据此渲染
// 可拖拽的树，agent 据此按顺序执行 / 撰写。Status 透传给后续「plan 驱动 agent 循环」。
type PlanItem struct {
	ID        int64      `json:"id,string"`
	ParentID  int64      `json:"parent_id,string"`
	Title     string     `json:"title"`
	Summary   string     `json:"summary,omitempty"`
	Source    PlanSource `json:"source"`
	SourceID  int64      `json:"source_id,string"`
	SortOrder int        `json:"sort_order"`
	// Status 是节点执行状态（pending / running / done），当前仅存储与透传。
	Status string `json:"status,omitempty"`
}

// AgentPlan 是 agent 的计划：一组按树组织、可引用会话资源的步骤。
//
// 计划本身不生成报告，也不保存资源详情：它只描述「要做哪些步骤、每步依据哪个资源」，
// 因此 agent 可以跨会话查询它，再按节点来源 ID 去查询资源详情。
// 报告撰写只是计划的一种消费方式：ProjectReport.PlanID 指向计划，撰写时按计划取素材。
type AgentPlan struct {
	ID        int64      `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	ProjectID int64      `json:"project_id,string" gorm:"column:project_id;type:bigint;not null;index"`
	SessionID int64      `json:"session_id,string" gorm:"column:session_id;type:bigint;index"`
	Title     string     `json:"title" gorm:"column:title;type:varchar(255)"`
	Goal      string     `json:"goal" gorm:"column:goal;type:text"`
	Items     []PlanItem `json:"items" gorm:"column:items;serializer:json"`
	CreatedAt time.Time  `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time  `json:"updated_at" gorm:"column:updated_at"`
}

// TableName 返回计划表的表名。
func (AgentPlan) TableName() string { return "agent_plans" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (p *AgentPlan) BeforeCreate(_ *gorm.DB) error {
	if p.ID == 0 {
		p.ID = utils.GenerateID()
	}
	return nil
}
