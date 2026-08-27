package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// SummaryStatus 摘要生成状态。
type SummaryStatus string

const (
	SummaryStatusPending SummaryStatus = "pending"
	// SummaryStatusGenerating 生成中
	SummaryStatusGenerating SummaryStatus = "generating"
	// SummaryStatusSuccess 生成成功
	SummaryStatusSuccess SummaryStatus = "success"
	// SummaryStatusFailed 生成失败
	SummaryStatusFailed SummaryStatus = "failed"
)

// SummaryOwnerType 摘要所属对象的类型。
type SummaryOwnerType string

const (
	// SummaryOwnerAnalysis 摘要归属于 Analysis（nextflow 表）。
	SummaryOwnerAnalysis SummaryOwnerType = "analysis"
	// SummaryOwnerAnalysisNode 摘要归属于 AnalysisNode（analysis_nodes 表）。
	SummaryOwnerAnalysisNode SummaryOwnerType = "analysis_node"
)

// AISummary 存储 AI 为 Analysis 或 AnalysisNode 的 output 内容生成的摘要。
type AISummary struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	// OwnerID 摘要所属对象的 ID，主要为 Analysis.ID 或 AnalysisNode.ID。
	OwnerID int64 `json:"owner_id,string" gorm:"column:owner_id;type:bigint;index:idx_ai_summaries_owner"`
	// OwnerType 摘要所属对象的类型：analysis 或 analysis_node。
	OwnerType SummaryOwnerType `json:"owner_type" gorm:"column:owner_type;type:varchar(32);index:idx_ai_summaries_owner"`
	// Content AI 生成的摘要内容。
	Content string `json:"content" gorm:"column:content;type:longtext"`
	// Status 生成状态：生成中 / 生成成功 / 生成失败。
	Status SummaryStatus `json:"status" gorm:"column:status;type:varchar(32);default:generating"`

	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

func (t *AISummary) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

func (AISummary) TableName() string {
	return "ai_summaries"
}

// AISummaryGeneratePayload 是 AISummary 生成请求的 outbox 事件载荷。
type AISummaryGeneratePayload struct {
	SummaryID int64 `json:"summary_id,string"`
}
