package types

import (
	"fmt"
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
	ID     uint  `json:"id" gorm:"primaryKey;autoIncrement"`
	UserID int64 `json:"user_id,string" gorm:"type:bigint;not null;index:idx_user_project_user;uniqueIndex:idx_user_project_unique,priority:1"`
	// ProjectID 指向 t_project.id。
	ProjectID int64 `json:"project_id,string" gorm:"column:project_id;type:bigint;not null;index:idx_user_project_project;uniqueIndex:idx_user_project_unique,priority:2"`
	IsActive  bool  `json:"is_active" gorm:"default:false"`

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

// ProjectReport 是一个报告容器，只保存标题等元信息，不再承载文件内容。
// 报告的内容由 ProjectReportItem 列表按顺序拼接而成。
type ProjectReport struct {
	ID        int64     `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	ProjectID int64     `json:"project_id,string" gorm:"type:bigint;not null;index"`
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
	// ProjectReportItemOwnerCustom 是自定义内容条目，用于章节占位/自定义 markdown 段落。
	// 它不引用外部 owner，标题与正文由用户直接编辑并持久化在本表。
	ProjectReportItemOwnerCustom ProjectReportItemOwnerType = "custom"
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
	case ProjectReportItemOwnerCustom:
		return ProjectReportItemOwnerCustom, true
	default:
		return "", false
	}
}

// ProjectReportItem 是 ProjectReport 下的一个内容条目，按 SortOrder 排序后拼接成报告正文。
type ProjectReportItem struct {
	ID              int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	ProjectReportID int64 `json:"project_report_id,string" gorm:"column:project_report_id;type:bigint;index:idx_project_report_items_report"`
	// ParentID 指向同一报告下的父条目 ID，0 表示根节点。用于组织树形结构。
	ParentID  int64                      `json:"parent_id,string" gorm:"column:parent_id;type:bigint;index:idx_project_report_items_parent"`
	OwnerType ProjectReportItemOwnerType `json:"owner_type" gorm:"column:owner_type;type:varchar(32);index:idx_project_report_items_owner"`
	// OwnerID 指向 OwnerType 对应的主键。
	OwnerID   int64     `json:"owner_id,string" gorm:"column:owner_id;type:bigint;index:idx_project_report_items_owner"`
	SortOrder int       `json:"sort_order" gorm:"column:sort_order;default:0"`
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`

	// Title 是展示用标题。仅 OwnerType=custom 时由用户编辑并持久化；
	// 其他引用类型的标题由 owner 推导（读取时派生），写入时会清空，不落库。
	Title string `json:"title" gorm:"type:varchar(255)"`
	// Content 是自定义 markdown 正文，仅 OwnerType=custom 时持久化；
	// 其他引用类型不存储内容。
	Content string `json:"content" gorm:"type:longtext"`
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

// ProjectReportItemOrder 描述拖拽重排后单个条目的目标位置（父节点与同级顺序）。
type ProjectReportItemOrder struct {
	ID        int64
	ParentID  int64
	SortOrder int
}

// ProjectReportGitDiff 是报告工作目录（<baseDir>/data/<projectId>/report/<reportId>）的本地变化。
//
// 报告没有发布目标（store），因此只有「工作区相对本地 HEAD 的未提交改动」这一段，
// 不包含组件目录 diff 里的 Unpublished。字段命名与前端 git 组件保持一致，
// 便于报告与组件共用同一套 diff 视图与操作按钮。
type ProjectReportGitDiff struct {
	// ReportID / ProjectID 是报告与项目的数值主键（对外统一为字符串）。
	ReportID  string `json:"report_id"`
	ProjectID string `json:"project_id"`
	// RepoDir 是报告工作目录（绝对路径）。
	RepoDir string `json:"repo_dir"`
	// Initialized 报告目录是否已是 git 仓库；false 时没有可展示的 diff。
	Initialized bool `json:"initialized"`
	// HeadCommit 本地 HEAD 提交 hash（尚无提交时为空）。
	HeadCommit string `json:"head_commit,omitempty"`
	// HasChanges 工作区是否有未提交改动。
	HasChanges bool `json:"has_changes"`
	// Worktree 工作区相对本地 HEAD 的未提交改动（含未跟踪文件）。
	Worktree utils.GitDiffSection `json:"worktree"`
	// GitState 实时的本地 git 状态（是否初始化 / 是否有未提交改动）。
	GitState utils.ReportWorktreeGitState `json:"git_state"`
}

// ProjectReportCommitResult 是「提交报告工作目录改动」的结果。
type ProjectReportCommitResult struct {
	ReportID  string `json:"report_id"`
	ProjectID string `json:"project_id"`
	// Committed 本次是否真的产生了新提交（工作区干净时为 false，不产生空提交）。
	Committed bool `json:"committed"`
	// Commit 提交后的 HEAD hash。
	Commit string `json:"commit,omitempty"`
	// CommitMessage 本次提交实际使用的 message（入参为空时为后端默认文案）。
	CommitMessage string `json:"commit_message"`
}

// ProjectReportItemContent 是 ProjectReportItem 渲染后的内容片段。
// Title 为展示标题，Content 为正文。
//
// 正文里的图片一律是以数据目录为根的绝对路径（如 /34/analysis_node/100/200/output/a.png）：
// 节点完成时会把 output.md 里的相对图片路径改写掉（见 dag.NodeCompletionCoordinator），
// 展示侧只需统一补上 /data-analysis 前缀，因此这里不再需要 Prefix。
type ProjectReportItemContent struct {
	Title     string                     `json:"title"`
	Content   string                     `json:"content"`
	OwnerType ProjectReportItemOwnerType `json:"owner_type"`
}

// Render 将片段渲染为 markdown 小节；正文为空时返回空串。
// 正文已包含可供展示的图片绝对路径，渲染时不再做任何路径改写。
func (c ProjectReportItemContent) Render() string {
	content := c.Content
	if strings.TrimSpace(content) == "" {
		return ""
	}
	title := strings.TrimSpace(c.Title)
	if title == "" {
		title = "Untitled"
	}
	return fmt.Sprintf("# %s\n\n%s\n\n", title, content)
}
