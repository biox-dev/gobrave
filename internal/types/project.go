package types

import (
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

// ProjectReport content source values.
const (
	ProjectReportContentSourceFile     = "file"
	ProjectReportContentSourceDatabase = "database"
)

const DefaultProjectReportFilename = "output.md"

type ProjectReport struct {
	ID        int64  `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	ProjectID string `json:"project_id" gorm:"type:varchar(255);not null;index"`
	Title     string `json:"title" gorm:"type:varchar(255)"`
	Content   string `json:"content" gorm:"type:longtext"`
	SortOrder int    `json:"sort_order" gorm:"default:0"`
	// ContentSource indicates where Content is stored: "file" or "database".
	ContentSource string `json:"content_source" gorm:"type:varchar(16);default:file"`
	// Filename is the report file name under the project report directory.
	Filename  string    `json:"filename" gorm:"type:varchar(255);default:output.md"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (t *ProjectReport) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	if t.ContentSource == "" {
		t.ContentSource = ProjectReportContentSourceFile
	}
	if t.Filename == "" {
		t.Filename = DefaultProjectReportFilename
	}
	return nil
}

func (ProjectReport) TableName() string {
	return "t_project_report"
}
