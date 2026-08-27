package types

import (
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type File struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	FileID string `json:"file_id" gorm:"type:varchar(255);uniqueIndex;not null"`

	FileName string `json:"file_name" gorm:"type:varchar(255)"`

	Path string `json:"path" gorm:"type:text;not null"`

	Format string `json:"format" gorm:"type:varchar(64)"`

	Size int64 `json:"size"`

	MD5 string `json:"md5" gorm:"type:varchar(64);index"`

	Storage string `json:"storage" gorm:"type:varchar(32);default:LOCAL"`

	Description string `json:"description" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`
}

func (File) TableName() string {
	return "go_file"
}
func (t *File) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

type DatasetFile struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	DatasetID int64 `json:"dataset_id,string" gorm:"index;not null"`

	FileID int64 `json:"file_id,string" gorm:"index;not null"`

	Role string `json:"role" gorm:"type:varchar(64)"`

	CreatedAt time.Time `json:"created_at"`
}

func (DatasetFile) TableName() string {
	return "go_dataset_file"
}

func (t *DatasetFile) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

// UpdateDatasetFileRequest DTO: lookup by DatasetID + FileID, only Role is updatable
type UpdateDatasetFileRequest struct {
	DatasetID int64  `json:"dataset_id,string" binding:"required"`
	FileID    int64  `json:"file_id,string" binding:"required"`
	Role      string `json:"role"`
}

type AddFileToDatasetRequest struct {
	DatasetID int64  `json:"dataset_id,string" binding:"required"`
	ProjectID string `json:"-"`
	Path      string `json:"path" binding:"required"`
	Role      string `json:"role"`
	FileName  string `json:"file_name" gorm:"type:varchar(255)"`
	IsCopy    bool   `json:"is_copy"` // if true, copy the file to the dataset, otherwise just link it
	// data analysis external
	Source string `json:"source"`
}

type AddFileToDatasetResponse struct {
	File        *File        `json:"file"`
	DatasetFile *DatasetFile `json:"dataset_file"`
}

type FileWithDatasetInfo struct {
	ID int64 `json:"id,string"`

	FileID string `json:"file_id"`

	FileName string `json:"file_name"`

	Path string `json:"path"`

	Format string `json:"format"`

	Size int64 `json:"size"`

	MD5 string `json:"md5"`

	Storage string `json:"storage"`

	Description string `json:"description"`

	CreatedAt time.Time `json:"created_at"`

	UpdatedAt time.Time `json:"updated_at"`

	DatasetID string `json:"dataset_id"`

	DatasetName string `json:"dataset_name"`

	Role string `json:"role"`
}

type FileByProjectRoleGroup struct {
	Role string `json:"role"`

	Items []*FileWithDatasetInfo `json:"items"`
}
