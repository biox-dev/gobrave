package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// Literature content source values.
const (
	LiteratureContentSourceFile     = "file"
	LiteratureContentSourceDatabase = "database"
)

// DefaultLiteratureFilename is the default full-text file name stored under the
// project literature directory.
const DefaultLiteratureFilename = "fulltext.md"

// Literature maps to the t_literature table. It represents a reference paper /
// literature entry whose full text is (by default) stored as a file on disk.
//
// The relationship between literature and projects is maintained manually via the
// project_literature mapping table; we intentionally do NOT use GORM association
// tags/relations here.
type Literature struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	Title string `json:"title" gorm:"type:varchar(255)"`
	// Content is only authoritative when ContentSource == "database". For
	// file-backed literature this column stays empty and the real content lives
	// in the file referenced by Filename under the project literature directory.
	Content string `json:"content" gorm:"type:longtext"`
	// ContentSource indicates where the full text is stored: "file" or "database".
	ContentSource string `json:"content_source" gorm:"type:varchar(16);default:file"`
	// Filename is the full-text file name under the project literature directory.
	Filename string `json:"filename" gorm:"type:varchar(255);default:fulltext.md"`
	// OwnerProjectID is the project whose literature directory owns the full-text
	// file. It is set when the literature is first created.
	OwnerProjectID string    `json:"owner_project_id" gorm:"type:varchar(255);index"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (t *Literature) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	if t.ContentSource == "" {
		t.ContentSource = LiteratureContentSourceFile
	}
	if t.Filename == "" {
		t.Filename = DefaultLiteratureFilename
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
	ProjectID    string    `json:"project_id" gorm:"type:varchar(255);not null;index:idx_project_literature_project;uniqueIndex:idx_project_literature_unique,priority:1"`
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
