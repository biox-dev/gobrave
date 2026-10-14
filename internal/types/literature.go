package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// DefaultLiteratureFilename 是文献工作目录下约定的全文文件名。
// 与 ProjectReport 的 output.md 类似，全文不再存入数据库，而是保存在
// <projectDir>/literature/<literatureId>/ 目录下的该文件中。
const DefaultLiteratureFilename = "literature.md"

// Literature maps to the t_literature table. It represents a reference paper /
// literature entry. Only metadata (title) is persisted; the full text is stored
// as a file on disk, mirroring how ProjectReport stores its content.
//
// The relationship between literature and projects is maintained manually via the
// project_literature mapping table; we intentionally do NOT use GORM association
// tags/relations here.
type Literature struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	Title     string    `json:"title" gorm:"type:varchar(255)"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (t *Literature) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

func (Literature) TableName() string {
	return "t_literature"
}

// ProjectLiterature is a manual many-to-many mapping table between projects and
// literatures. We intentionally do not use GORM association tags/relations.
type ProjectLiterature struct {
	ID           uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	ProjectID    int64     `json:"project_id,string" gorm:"type:bigint;not null;index:idx_project_literature_project;uniqueIndex:idx_project_literature_unique,priority:1"`
	LiteratureID int64     `json:"literature_id,string" gorm:"type:bigint;not null;index:idx_project_literature_literature;uniqueIndex:idx_project_literature_unique,priority:2"`
	CreatedAt    time.Time `json:"created_at"`
}

func (ProjectLiterature) TableName() string {
	return "project_literature"
}

// LiteraturePoolItem carries a Literature together with whether it is already
// bound to the queried project. Used by the bind-literature picker.
type LiteraturePoolItem struct {
	Literature
	Bound bool `json:"bound"`
}

// LiteratureDetail 是文献详情：元信息 + 全文文件内容。
type LiteratureDetail struct {
	Literature *Literature
	Content    string
}
