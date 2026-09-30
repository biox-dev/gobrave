package types

import (
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Project maps to Python brave's t_project table.
type Project struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	// ID           uint           `json:"id" gorm:"primaryKey;autoIncrement"`
	ProjectID    string         `json:"project_id" gorm:"type:varchar(255);uniqueIndex;not null"`
	ProjectName  string         `json:"project_name" gorm:"type:varchar(255)"`
	MetadataForm string         `json:"metadata_form" gorm:"type:text"`
	Research     string         `json:"research" gorm:"type:text"`
	Parameter    string         `json:"parameter" gorm:"type:text"`
	Mounts       datatypes.JSON `json:"mounts" gorm:"type:json"`
	Env          datatypes.JSON `json:"env" gorm:"type:json"`

	Description string    `json:"description" gorm:"type:text"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (Project) TableName() string {
	return "t_project"
}
func (t *Project) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

// ProjectListItem carries a Project together with the sharing settings of the
// current user, taken from the user_project mapping table.
type ProjectListItem struct {
	Project
	ShareCode    string `json:"share_code"`
	ShareEnabled bool   `json:"share_enabled"`
}

// UserProject is a manual many-to-many mapping table between users and projects.
// We intentionally do not use GORM association tags/relations.
type UserProject struct {
	ID     uint   `json:"id" gorm:"primaryKey;autoIncrement"`
	UserID string `json:"user_id" gorm:"type:varchar(36);not null;index:idx_user_project_user;uniqueIndex:idx_user_project_unique,priority:1"`
	// ProjectID int64  `json:"project_id,string" gorm:"column:project_id;type:bigint"`
	ProjectID string `json:"project_id" gorm:"type:varchar(255);not null;index:idx_user_project_project;uniqueIndex:idx_user_project_unique,priority:2"`
	IsActive  bool   `json:"is_active" gorm:"default:false"`

	// ShareCode is generated when the owner enables project sharing. Other users
	// can use it to look up the project and gain access.
	ShareCode string `json:"share_code" gorm:"type:varchar(64);index"`
	// ShareEnabled toggles whether the project can be shared via ShareCode.
	ShareEnabled bool      `json:"share_enabled" gorm:"default:false"`
	CreatedAt    time.Time `json:"created_at"`
}

func (UserProject) TableName() string {
	return "user_project"
}

// DefaultProjectReportFilename 是项目报告工作目录下约定的输出文件名。
// 它仍然被 LLM 运行时（projectReport 环境）用来定位报告输出文件。
const DefaultProjectReportFilename = "output.md"

// DefaultProjectReportItemFilename 是 File 类型报告条目在磁盘上的固定文件名。
const DefaultProjectReportItemFilename = "output.md"

// ProjectReport 是一个报告容器，只保存标题等元信息，不再承载文件内容。
// 报告的内容由 ProjectReportItem 列表按顺序拼接而成。
type ProjectReport struct {
	ID        int64     `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	ProjectID string    `json:"project_id" gorm:"type:varchar(255);not null;index"`
	Title     string    `json:"title" gorm:"type:varchar(255)"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (t *ProjectReport) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

func (ProjectReport) TableName() string {
	return "t_project_report"
}

// ProjectReportItemOwnerType 标识 ProjectReportItem 指向的内容来源类型。
type ProjectReportItemOwnerType string

const (
	// ProjectReportItemOwnerAnalysis 指向 Analysis（nextflow 表）的 ID。
	ProjectReportItemOwnerAnalysis ProjectReportItemOwnerType = "analysis"
	// ProjectReportItemOwnerAnalysisNode 指向 AnalysisNode（analysis_nodes 表）的 ID。
	ProjectReportItemOwnerAnalysisNode ProjectReportItemOwnerType = "analysis_node"
	// ProjectReportItemOwnerAISummary 指向 AISummary 的 ID。
	ProjectReportItemOwnerAISummary ProjectReportItemOwnerType = "ai_summary"
	// ProjectReportItemOwnerFile 表示内容由用户在报告目录内手工编辑的文件，OwnerID 无意义。
	ProjectReportItemOwnerFile ProjectReportItemOwnerType = "file"
)

// NormalizeProjectReportItemOwnerType 校验并归一化 OwnerType。
func NormalizeProjectReportItemOwnerType(t string) (ProjectReportItemOwnerType, bool) {
	switch ProjectReportItemOwnerType(strings.ToLower(strings.TrimSpace(t))) {
	case ProjectReportItemOwnerAnalysis:
		return ProjectReportItemOwnerAnalysis, true
	case ProjectReportItemOwnerAnalysisNode:
		return ProjectReportItemOwnerAnalysisNode, true
	case ProjectReportItemOwnerAISummary:
		return ProjectReportItemOwnerAISummary, true
	case ProjectReportItemOwnerFile:
		return ProjectReportItemOwnerFile, true
	default:
		return "", false
	}
}

// ProjectReportItem 是 ProjectReport 下的一个内容条目，按 SortOrder 排序后拼接成报告正文。
type ProjectReportItem struct {
	ID              int64                      `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	ProjectReportID int64                      `json:"project_report_id,string" gorm:"column:project_report_id;type:bigint;index:idx_project_report_items_report"`
	OwnerType       ProjectReportItemOwnerType `json:"owner_type" gorm:"column:owner_type;type:varchar(32);index:idx_project_report_items_owner"`
	// OwnerID 指向 OwnerType 对应的主键；OwnerType 为 file 时该字段无意义。
	OwnerID   int64     `json:"owner_id,string" gorm:"column:owner_id;type:bigint;index:idx_project_report_items_owner"`
	SortOrder int       `json:"sort_order" gorm:"column:sort_order;default:0"`
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`

	// Title 是展示用标题。File 类型取自文件名，其它类型由 owner 推导；不落库。
	Title string `json:"title" gorm:"-"`
	// Content 仅在 File 类型时按需从磁盘读取；不落库。
	Content string `json:"content,omitempty" gorm:"-"`
}

func (t *ProjectReportItem) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

func (ProjectReportItem) TableName() string {
	return "t_project_report_items"
}
