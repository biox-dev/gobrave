package types

import (
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// ErrReportPlanNotFound 表示报告大纲不存在。
var ErrReportPlanNotFound = errors.New("report_plan: not found")

// ReportPlanSource 标识大纲章节的内容来源。
//
// 取值与 SessionResourceType 一致（custom 除外），因此大纲章节可以直接引用
// 「某个会话产出的资源」，在报告撰写会话里按来源去读取真实结果。
type ReportPlanSource string

const (
	// ReportPlanSourceCustom 是自定义章节（不引用外部资源）。
	ReportPlanSourceCustom ReportPlanSource = "custom"
	// ReportPlanSourceAnalysis 指向一次分析运行。
	ReportPlanSourceAnalysis ReportPlanSource = "analysis"
	// ReportPlanSourceAnalysisNode 指向一个分析节点。
	ReportPlanSourceAnalysisNode ReportPlanSource = "analysis_node"
	// ReportPlanSourceScript 指向一个脚本。
	ReportPlanSourceScript ReportPlanSource = "script"
	// ReportPlanSourceWorkflow 指向一个工作流。
	ReportPlanSourceWorkflow ReportPlanSource = "workflow"
)

// Valid 报告来源类型是否是受支持的取值。
func (s ReportPlanSource) Valid() bool {
	switch s {
	case ReportPlanSourceCustom, ReportPlanSourceAnalysis, ReportPlanSourceAnalysisNode,
		ReportPlanSourceScript, ReportPlanSourceWorkflow:
		return true
	default:
		return false
	}
}

// ReportPlanSection 是大纲中的一个章节（可嵌套）。
//
// ParentID 指向同一大纲内的父章节（0 表示根），SortOrder 为同级顺序：前端据此渲染
// 可拖拽的树，agent 据此按顺序撰写报告章节。
type ReportPlanSection struct {
	ID        int64            `json:"id,string"`
	ParentID  int64            `json:"parent_id,string"`
	Title     string           `json:"title"`
	Summary   string           `json:"summary,omitempty"`
	Source    ReportPlanSource `json:"source"`
	SourceID  int64            `json:"source_id,string"`
	SortOrder int              `json:"sort_order"`
}

// ReportPlan 是报告大纲：报告要写哪些章节、每节依据哪些会话资源。
//
// 大纲是项目级、跨会话共享的（复杂任务由多个会话产出资源，再由独立的报告会话撰写），
// 因此它独立于 ProjectReport 存在；报告通过 ProjectReport.PlanID 引用大纲。
type ReportPlan struct {
	ID        int64               `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	ProjectID int64               `json:"project_id,string" gorm:"column:project_id;type:bigint;not null;index"`
	Title     string              `json:"title" gorm:"column:title;type:varchar(255)"`
	Goal      string              `json:"goal" gorm:"column:goal;type:text"`
	Sections  []ReportPlanSection `json:"sections" gorm:"column:sections;serializer:json"`
	CreatedAt time.Time           `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time           `json:"updated_at" gorm:"column:updated_at"`
}

// TableName 返回报告大纲表的表名。
func (ReportPlan) TableName() string { return "agent_report_plans" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (p *ReportPlan) BeforeCreate(_ *gorm.DB) error {
	if p.ID == 0 {
		p.ID = utils.GenerateID()
	}
	return nil
}
